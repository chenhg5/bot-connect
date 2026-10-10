// Package agentcli runs coding-agent CLIs headlessly (Claude Code, Codex, or
// any command) and extracts the final message and session id. Both the brain
// and the workers are driven through it.
package agentcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type Result struct {
	Text      string // the agent's final message
	SessionID string // id to resume next time
}

type Call struct {
	Dir  string
	Env  []string // extra KEY=VALUE pairs on top of os.Environ()
	Args []string

	base []string // inherited env; nil = os.Environ()
}

// Env vars that mark "I am running inside another Claude Code session". A
// child claude that inherits them thinks it is nested and defers auth to a
// host that is not there.
var claudeHostEnv = []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID",
	"CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_HOST_SESSION_ID",
	"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_PID", "CLAUDE_CODE_EXECPATH"}

// Inherited auth/routing vars that must not leak into a provider-routed claude.
var claudeAuthPrefixes = []string{"ANTHROPIC_", "CLAUDE_CODE_OAUTH", "CLAUDE_CODE_USE_",
	"CLAUDE_CODE_SDK_HAS_HOST_AUTH_REFRESH", "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"}

// claudeBaseEnv prepares the inherited environment for a claude child, the
// way cc-connect does: drop nested-session markers always; when the call
// routes to a provider (ANTHROPIC_BASE_URL in extra), also drop inherited
// auth and mark the provider as host-managed.
func claudeBaseEnv(extra []string) ([]string, []string) {
	provider := false
	for _, e := range extra {
		if strings.HasPrefix(e, "ANTHROPIC_BASE_URL=") {
			provider = true
		}
	}
	var base []string
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if hasAny(key, claudeHostEnv, false) || (provider && hasAny(key, claudeAuthPrefixes, true)) {
			continue
		}
		base = append(base, e)
	}
	if provider {
		extra = append(extra, "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST=1")
	}
	return base, extra
}

func hasAny(key string, list []string, prefix bool) bool {
	for _, k := range list {
		if key == k || (prefix && strings.HasPrefix(key, k)) {
			return true
		}
	}
	return false
}

// Claude runs `claude <args>`; args must include -p ... --output-format stream-json --verbose.
func Claude(ctx context.Context, c Call) (Result, error) {
	c.base, c.Env = claudeBaseEnv(c.Env)
	var (
		res       Result
		lastText  string
		isError   bool
		gotResult bool
	)
	stderr, err := runJSONL(ctx, c, "claude", func(ev map[string]any) {
		if s, _ := ev["session_id"].(string); s != "" {
			res.SessionID = s
		}
		switch ev["type"] {
		case "assistant":
			if msg, ok := ev["message"].(map[string]any); ok {
				if blocks, ok := msg["content"].([]any); ok {
					for _, b := range blocks {
						if bm, ok := b.(map[string]any); ok && bm["type"] == "text" {
							if t, _ := bm["text"].(string); strings.TrimSpace(t) != "" {
								lastText = t
							}
						}
					}
				}
			}
		case "result":
			gotResult = true
			res.Text, _ = ev["result"].(string)
			isError, _ = ev["is_error"].(bool)
		}
	})
	if res.Text == "" {
		res.Text = lastText
	}
	if err != nil && !gotResult {
		return res, fmt.Errorf("claude: %v: %s", err, Tail(stderr, 600))
	}
	if isError {
		return res, fmt.Errorf("claude reported error: %s", Tail(res.Text, 600))
	}
	return res, nil
}

// Codex runs `codex <args>`; args must include exec ... --json.
func Codex(ctx context.Context, c Call) (Result, error) {
	var res Result
	var failure string
	stderr, err := runJSONL(ctx, c, "codex", func(ev map[string]any) {
		switch ev["type"] {
		case "thread.started":
			res.SessionID, _ = ev["thread_id"].(string)
		case "item.completed":
			if item, ok := ev["item"].(map[string]any); ok && item["type"] == "agent_message" {
				if t, _ := item["text"].(string); t != "" {
					res.Text = t
				}
			}
		case "turn.failed":
			if e, ok := ev["error"].(map[string]any); ok {
				failure, _ = e["message"].(string)
			}
		case "error":
			if failure == "" {
				failure, _ = ev["message"].(string)
			}
		}
	})
	if failure != "" && res.Text == "" {
		return res, fmt.Errorf("codex: %s", Tail(failure, 600))
	}
	if err != nil && res.Text == "" {
		return res, fmt.Errorf("codex: %v: %s", err, Tail(stderr, 600))
	}
	return res, nil
}

// Plain runs an arbitrary command and returns its trimmed stdout as the
// final message. stdin, if non-empty, is fed to the process.
func Plain(ctx context.Context, c Call, stdin string) (Result, error) {
	if len(c.Args) == 0 {
		return Result{}, fmt.Errorf("empty command")
	}
	cmd := command(ctx, c, c.Args[0], c.Args[1:])
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if err != nil {
		return Result{Text: strings.TrimSpace(out.String())}, fmt.Errorf("%s: %v: %s", c.Args[0], err, Tail(errb.String(), 600))
	}
	return Result{Text: strings.TrimSpace(out.String())}, nil
}

func command(ctx context.Context, c Call, bin string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = c.Dir
	base := c.base
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = append(append([]string(nil), base...), c.Env...)
	// Own process group so cancellation kills the agent's whole tree.
	prepareTree(cmd)
	cmd.Cancel = func() error { return KillTree(cmd) }
	return cmd
}

func runJSONL(ctx context.Context, c Call, bin string, onEvent func(map[string]any)) (string, error) {
	cmd := command(ctx, c, bin, c.Args)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	trace := openTrace(bin)
	if trace != nil {
		defer trace.Close()
	}
	r := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, rerr := r.ReadBytes('\n')
		if trace != nil {
			_, _ = trace.Write(line)
		}
		if len(bytes.TrimSpace(line)) > 0 {
			var ev map[string]any
			if json.Unmarshal(line, &ev) == nil {
				onEvent(ev)
			}
		}
		if rerr == io.EOF || rerr != nil {
			break
		}
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return stderr.String(), ctx.Err()
	}
	return stderr.String(), err
}

// openTrace returns a file to copy the raw event stream into when
// BOT_CONNECT_TRACE_DIR is set (debugging aid).
func openTrace(bin string) *os.File {
	dir := os.Getenv("BOT_CONNECT_TRACE_DIR")
	if dir == "" {
		return nil
	}
	_ = os.MkdirAll(dir, 0o700)
	f, err := os.CreateTemp(dir, bin+"-*.jsonl")
	if err != nil {
		return nil
	}
	return f
}

func Tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// Pi runs `pi <args>`; args must include -p … --mode json.
func Pi(ctx context.Context, c Call) (Result, error) {
	var res Result
	var failure string
	stderr, err := runJSONL(ctx, c, "pi", func(ev map[string]any) {
		switch ev["type"] {
		case "session":
			if id, _ := ev["id"].(string); id != "" {
				res.SessionID = id
			}
		case "message_end":
			if m, ok := ev["message"].(map[string]any); ok && m["role"] == "assistant" {
				if t := piText(m["content"]); t != "" {
					res.Text = t
				}
				if e, _ := m["errorMessage"].(string); e != "" {
					failure = e
				}
			}
		}
	})
	if res.Text == "" && failure != "" {
		return res, fmt.Errorf("pi: %s", Tail(failure, 600))
	}
	if err != nil && res.Text == "" {
		return res, fmt.Errorf("pi: %v: %s", err, Tail(stderr, 600))
	}
	return res, nil
}

func piText(content any) string {
	blocks, _ := content.([]any)
	var parts []string
	for _, b := range blocks {
		if bm, ok := b.(map[string]any); ok && bm["type"] == "text" {
			if t, _ := bm["text"].(string); strings.TrimSpace(t) != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n")
}
