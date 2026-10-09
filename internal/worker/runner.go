package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chenhg5/bot-connect/internal/agentcli"
	"github.com/chenhg5/bot-connect/internal/config"
)

// Runner executes one instruction in a worker's agent session and returns the
// agent's final message plus the (possibly new) session id to resume next time.
type Runner interface {
	Run(ctx context.Context, spec config.Worker, sessionID, prompt string) (result, newSessionID string, err error)
}

func RunnerFor(agent string) (Runner, error) {
	switch agent {
	case "claudecode":
		return claudeRunner{}, nil
	case "codex":
		return codexRunner{}, nil
	}
	return nil, fmt.Errorf("unsupported agent %q (supported: claudecode, codex)", agent)
}

type claudeRunner struct{}

func (claudeRunner) Run(ctx context.Context, spec config.Worker, sessionID, prompt string) (string, string, error) {
	args := append([]string{"-p", prompt, "--output-format", "stream-json", "--verbose"}, ClaudeAccessArgs(spec)...)
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	res, err := agentcli.Claude(ctx, agentcli.Call{Dir: spec.WorkDir, Env: spec.Env, Args: args})
	return res.Text, firstNonEmpty(res.SessionID, sessionID), err
}

// ClaudeAccessArgs translates a worker's access level into Claude Code flags:
//
//	readonly  — permission mode "default" (nothing is approved unattended),
//	            only Read/Grep/Glob, reads limited to work_dir + read_dirs
//	workspace — bypassPermissions inside the OS sandbox: shell and edits run,
//	            writes stay in work_dir + write_dirs, deny_read holds for scripts too
//	full      — bypassPermissions, no sandbox
//
// permission_mode, tools and confine override the level when set.
func ClaudeAccessArgs(spec config.Worker) []string {
	mode, tools := spec.PermissionMode, spec.Tools
	switch spec.Access {
	case "readonly":
		if mode == "" {
			mode = "default"
		}
		if tools == nil {
			tools = []string{"Read", "Grep", "Glob"}
		}
	default:
		if mode == "" {
			mode = "bypassPermissions"
		}
	}
	args := []string{"--permission-mode", mode}
	if spec.Isolate != nil && *spec.Isolate {
		// Keep the owner's Claude setup out: no ~/.claude/CLAUDE.md, settings,
		// hooks, plugins or MCP connectors (--bare needs an API key).
		if hasEnv(spec.Env, "ANTHROPIC_API_KEY") {
			args = append(args, "--bare")
		} else {
			args = append(args, "--setting-sources", "")
		}
		args = append(args, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`)
	}
	if tools != nil {
		args = append(args, "--tools", strings.Join(tools, ","))
	}
	for _, d := range append(append([]string(nil), spec.ReadDirs...), spec.WriteDirs...) {
		args = append(args, "--add-dir", d)
	}
	if st := WorkerSettings(spec); st != "" {
		args = append(args, "--settings", st)
	}
	return args
}

type codexRunner struct{}

func (codexRunner) Run(ctx context.Context, spec config.Worker, sessionID, prompt string) (string, string, error) {
	var overrides []string
	sandbox := spec.Sandbox
	if spec.Confine != nil && *spec.Confine && spec.Access != "full" {
		var deny []string
		if spec.DenyRead != nil {
			deny = *spec.DenyRead
		}
		overrides = CodexProfile(spec.WorkDir, spec.Access != "readonly", spec.ReadDirs, spec.WriteDirs, deny)
		sandbox = "" // profiles and sandbox_mode are mutually exclusive
	} else if len(spec.WriteDirs) > 0 && sandbox == "workspace-write" {
		roots, _ := json.Marshal(spec.WriteDirs)
		overrides = append(overrides, "sandbox_workspace_write.writable_roots="+string(roots))
	}
	env := append([]string(nil), spec.Env...)
	if spec.Isolate != nil && *spec.Isolate && spec.CodexHome != "" {
		if err := agentcli.EnsureCodexHome(spec.CodexHome); err != nil {
			return "", sessionID, err
		}
		env = append(env, "CODEX_HOME="+spec.CodexHome)
		overrides = append(overrides, "project_doc_max_bytes=0")
	}
	args := CodexArgs(sessionID, sandbox, spec.Model, overrides, prompt)
	res, err := agentcli.Codex(ctx, agentcli.Call{Dir: spec.WorkDir, Env: env, Args: args})
	return res.Text, firstNonEmpty(res.SessionID, sessionID), err
}

// CodexArgs builds `codex exec` / `codex exec resume` arguments. `exec resume`
// does not accept -s, so the sandbox is passed as a config override there.
func CodexArgs(sessionID, sandbox, model string, overrides []string, prompt string) []string {
	var args []string
	if sessionID == "" {
		args = []string{"exec", "--json", "--skip-git-repo-check"}
		if sandbox != "" {
			args = append(args, "-s", sandbox)
		}
	} else {
		args = []string{"exec", "resume", sessionID, "--json", "--skip-git-repo-check"}
		if sandbox != "" {
			args = append(args, "-c", fmt.Sprintf("sandbox_mode=%q", sandbox))
		}
	}
	for _, o := range overrides {
		args = append(args, "-c", o)
	}
	if model != "" {
		args = append(args, "-m", model)
	}
	return append(args, prompt)
}

// DenyReadSettings renders Claude Code permission deny rules for paths.
func DenyReadSettings(paths []string) string {
	b, _ := json.Marshal(map[string]any{"permissions": map[string]any{"deny": denyRules(paths)}})
	return string(b)
}

// WorkerSettings renders a Claude Code worker's --settings: deny rules, and
// with confine the bash sandbox, which enforces them at the OS level.
func WorkerSettings(spec config.Worker) string {
	st := map[string]any{}
	if spec.DenyRead != nil && len(*spec.DenyRead) > 0 {
		st["permissions"] = map[string]any{"deny": denyRules(*spec.DenyRead)}
	}
	if spec.Confine != nil && *spec.Confine {
		st["sandbox"] = map[string]any{"enabled": true, "autoAllowBashIfSandboxed": true}
	}
	if len(st) == 0 {
		return ""
	}
	b, _ := json.Marshal(st)
	return string(b)
}

func denyRules(paths []string) []string {
	var deny []string
	for _, p := range paths {
		if strings.HasPrefix(p, "/") {
			p = "/" + p // Claude Code: "//abs/path" = absolute
			if !strings.Contains(p, "*") && filepath.Ext(p) == "" {
				p += "/**" // a directory: everything under it
			}
		}
		deny = append(deny, "Read("+p+")")
	}
	return deny
}

func hasEnv(env []string, key string) bool {
	for _, e := range env {
		if k, v, _ := strings.Cut(e, "="); k == key && v != "" {
			return true
		}
	}
	return false
}

// CodexProfile renders `-c` overrides that confine a Codex process with a
// permission profile (enforced by the OS sandbox, shell included): minimal
// system reads, work_dir as given, read_dirs readable, write_dirs writable,
// deny paths unreadable. Used instead of the legacy sandbox_mode.
func CodexProfile(workDir string, writable bool, readDirs, writeDirs, deny []string) []string {
	fs := []string{`":minimal"="read"`}
	add := func(path, mode string) {
		path = strings.TrimSuffix(strings.TrimSuffix(path, "/**"), "/*")
		fs = append(fs, fmt.Sprintf("%q=%q", path, mode))
	}
	if writable {
		add(workDir, "write")
	} else {
		add(workDir, "read")
	}
	for _, d := range readDirs {
		add(d, "read")
	}
	for _, d := range writeDirs {
		add(d, "write")
	}
	for _, d := range deny {
		add(d, "deny")
	}
	// The table goes in as one TOML value: `-c` would split dotted path keys.
	return []string{`default_permissions="bot-connect"`,
		"permissions.bot-connect.filesystem={" + strings.Join(fs, ", ") + "}"}
}
