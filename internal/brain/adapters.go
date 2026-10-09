package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chenhg5/bot-connect/internal/agentcli"
	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/worker"
)

// Built-in adapters. Each invokes the agent's own CLI directly.
func init() {
	Register("claudecode", func(c config.Brain) (Adapter, error) { return &claudeAdapter{c}, nil })
	Register("codex", func(c config.Brain) (Adapter, error) { return &codexAdapter{c}, nil })
	Register("command", func(c config.Brain) (Adapter, error) {
		if len(c.Command) == 0 {
			return nil, fmt.Errorf("brain.agent = \"command\" requires brain.command")
		}
		return &commandAdapter{c}, nil
	})
}

func toolEnv(req Request) []string {
	return append([]string{"BOT_CONNECT_API=" + req.Tools.APIURL}, req.Env...)
}

func envMap(m map[string]string) []string {
	var out []string
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// ---- Claude Code ----
// Tools via --mcp-config (strict: bot-connect + the user's servers only),
// built-in tools limited by builtin_tools (default none), system prompt via
// --append-system-prompt, one session per conversation via --resume.

type claudeAdapter struct{ cfg config.Brain }

func (a *claudeAdapter) Name() string { return "claudecode" }
func (a *claudeAdapter) Caps() Caps   { return Caps{Sessions: true, SystemPrompt: true} }

func (a *claudeAdapter) Run(ctx context.Context, req Request) (Response, error) {
	servers := map[string]any{"bot": map[string]any{"type": "http", "url": req.Tools.MCPURL}}
	allowed := []string{"mcp__bot"}
	for _, t := range a.cfg.BuiltinTools {
		// File-reading tools are NOT pre-approved: they then work only inside
		// work_dir + read_dirs (Claude Code's own scoping); elsewhere they
		// would need an approval that never comes.
		if !fileReadTools[t] {
			allowed = append(allowed, t)
		}
	}
	for _, m := range req.Tools.Extra {
		if m.URL != "" {
			servers[m.Name] = map[string]any{"type": "http", "url": m.URL}
		} else {
			servers[m.Name] = map[string]any{"type": "stdio", "command": m.Command, "args": m.Args, "env": m.Env}
		}
		allowed = append(allowed, "mcp__"+m.Name)
	}
	mcp, _ := json.Marshal(map[string]any{"mcpServers": servers})
	args := []string{"-p", req.Prompt, "--output-format", "stream-json", "--verbose",
		"--append-system-prompt", req.SystemPrompt,
		"--mcp-config", string(mcp), "--strict-mcp-config",
		"--tools", strings.Join(a.cfg.BuiltinTools, ","),
		"--allowedTools", strings.Join(allowed, ","),
	}
	if a.cfg.Isolate == nil || *a.cfg.Isolate {
		// Keep the owner's own Claude setup (~/.claude/CLAUDE.md, memory,
		// hooks, plugins, settings) out of the brain. --bare is the most
		// thorough but only authenticates with an API key; without one, load
		// no setting sources, which also drops the user CLAUDE.md and keeps
		// the OAuth login working.
		if hasEnv(req.Env, "ANTHROPIC_API_KEY") {
			args = append(args, "--bare")
		} else {
			args = append(args, "--setting-sources", "")
		}
	}
	for _, d := range a.cfg.ReadDirs {
		args = append(args, "--add-dir", d)
	}
	if a.cfg.DenyRead != nil && len(*a.cfg.DenyRead) > 0 {
		args = append(args, "--settings", worker.DenyReadSettings(*a.cfg.DenyRead))
	}
	if a.cfg.Model != "" {
		args = append(args, "--model", a.cfg.Model)
	}
	if req.SessionID != "" {
		args = append(args, "--resume", req.SessionID)
	}
	args = append(args, a.cfg.ExtraArgs...)
	res, err := agentcli.Claude(ctx, agentcli.Call{Dir: a.cfg.WorkDir, Env: toolEnv(req), Args: args})
	return Response{Text: res.Text, SessionID: res.SessionID}, err
}

func hasEnv(env []string, key string) bool {
	for _, e := range env {
		if k, v, _ := strings.Cut(e, "="); k == key && v != "" {
			return true
		}
	}
	return false
}

var fileReadTools = map[string]bool{"Read": true, "Grep": true, "Glob": true, "LS": true, "NotebookRead": true}

// ---- Codex ----
// Tools via mcp_servers overrides (pre-approved: privilege is enforced by
// bot-connect), read-only sandbox, system prompt inlined by the framework.

type codexAdapter struct{ cfg config.Brain }

func (a *codexAdapter) Name() string { return "codex" }
func (a *codexAdapter) Caps() Caps   { return Caps{Sessions: true} }

func (a *codexAdapter) Run(ctx context.Context, req Request) (Response, error) {
	overrides := []string{
		fmt.Sprintf("mcp_servers.bot.url=%q", req.Tools.MCPURL),
		`mcp_servers.bot.default_tools_approval_mode="approve"`,
		`approval_policy="never"`,
	}
	for _, m := range req.Tools.Extra {
		k := "mcp_servers." + m.Name
		if m.URL != "" {
			overrides = append(overrides, fmt.Sprintf("%s.url=%q", k, m.URL))
		} else {
			cmd, _ := json.Marshal(m.Command)
			argv, _ := json.Marshal(m.Args)
			overrides = append(overrides, fmt.Sprintf("%s.command=%s", k, cmd), fmt.Sprintf("%s.args=%s", k, argv))
			for _, kv := range envMap(m.Env) {
				ek, ev, _ := strings.Cut(kv, "=")
				overrides = append(overrides, fmt.Sprintf("%s.env.%s=%q", k, ek, ev))
			}
		}
	}
	sandbox := a.cfg.Sandbox
	if a.cfg.Confine == nil || *a.cfg.Confine {
		var deny []string
		if a.cfg.DenyRead != nil {
			deny = *a.cfg.DenyRead
		}
		overrides = append(overrides, worker.CodexProfile(a.cfg.WorkDir, false, a.cfg.ReadDirs, nil, deny)...)
		sandbox = "" // profiles and sandbox_mode are mutually exclusive
	}
	env := toolEnv(req)
	if a.cfg.Isolate == nil || *a.cfg.Isolate {
		// Keep the owner's Codex setup out: own CODEX_HOME (no ~/.codex
		// config, global AGENTS.md, plugins, MCP) and no project AGENTS.md.
		home := filepath.Join(filepath.Dir(a.cfg.WorkDir), "brain-codex-home") // outside what the brain can read
		if err := agentcli.EnsureCodexHome(home); err != nil {
			return Response{}, err
		}
		env = append(env, "CODEX_HOME="+home)
		overrides = append(overrides, "project_doc_max_bytes=0")
	}
	args := worker.CodexArgs(req.SessionID, sandbox, a.cfg.Model, overrides, req.Prompt)
	args = append(args[:len(args)-1], append(append([]string(nil), a.cfg.ExtraArgs...), req.Prompt)...)
	res, err := agentcli.Codex(ctx, agentcli.Call{Dir: a.cfg.WorkDir, Env: env, Args: args})
	return Response{Text: res.Text, SessionID: res.SessionID}, err
}

// ---- Any CLI (pi, cursor-agent, …) ----
// Stateless; the framework inlines system prompt and history. The process
// reaches tools with `bot-connect tool …` via BOT_CONNECT_API.

type commandAdapter struct{ cfg config.Brain }

func (a *commandAdapter) Name() string { return "command" }
func (a *commandAdapter) Caps() Caps   { return Caps{} }

func (a *commandAdapter) Run(ctx context.Context, req Request) (Response, error) {
	args := make([]string, len(a.cfg.Command))
	usedArg := false
	for i, s := range a.cfg.Command {
		if strings.Contains(s, "{prompt}") {
			s = strings.ReplaceAll(s, "{prompt}", req.Prompt)
			usedArg = true
		}
		args[i] = s
	}
	stdin := ""
	if !usedArg {
		stdin = req.Prompt
	}
	res, err := agentcli.Plain(ctx, agentcli.Call{Dir: a.cfg.WorkDir, Env: toolEnv(req), Args: args}, stdin)
	return Response{Text: res.Text}, err
}
