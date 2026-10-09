// Package config loads bot-connect's TOML configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config holds one or more bots sharing one worker pool.
//
// Multi-bot:  [[bots]] … [bots.brain] … [bots.feishu] …
// Single bot: [bot] + [brain] + [feishu] (shorthand, used when no [[bots]]).
type Config struct {
	DataDir string `toml:"data_dir"` // default ~/.bot-connect
	Server  Server `toml:"server"`
	// Named workers (sessions) shared by all bots; one queue per session.
	Workers []Worker `toml:"workers"`
	// Model providers for Claude Code processes (brain and workers), the same
	// way cc-connect does it: ANTHROPIC_BASE_URL / AUTH_TOKEN / MODEL env.
	Providers []Provider `toml:"providers"`

	Bots []BotConfig `toml:"bots"`

	// single-bot shorthand
	Bot    Bot    `toml:"bot"`
	Brain  Brain  `toml:"brain"`
	Feishu Feishu `toml:"feishu"`
}

// BotConfig is one bot: identity, brain, channel, and what it may reach.
type BotConfig struct {
	Bot
	Brain  Brain  `toml:"brain"`
	Feishu Feishu `toml:"feishu"`

	Dir string `toml:"-"` // this bot's state dir (conversations, brain workdir)
}

type Provider struct {
	Name      string `toml:"name"`
	BaseURL   string `toml:"base_url"`
	APIKey    string `toml:"api_key"`
	APIKeyEnv string `toml:"api_key_env"`
	Model     string `toml:"model"`
}

// ClaudeEnv returns the env vars that point a Claude Code process at p.
// AUTH_TOKEN (Bearer) is used instead of API_KEY, and API_KEY is blanked,
// because Claude Code validates API keys against api.anthropic.com.
func (p Provider) ClaudeEnv() []string {
	env := []string{"ANTHROPIC_BASE_URL=" + p.BaseURL, "ANTHROPIC_API_KEY="}
	if p.APIKey != "" {
		env = append(env, "ANTHROPIC_AUTH_TOKEN="+p.APIKey)
	}
	if p.Model != "" {
		env = append(env, "ANTHROPIC_MODEL="+p.Model)
	}
	return env
}

type Bot struct {
	Name      string `toml:"name"`
	OwnerName string `toml:"owner_name"`
	// Who the bot belongs to / trusts. Match platform user id, union id or
	// email. Empty owners = the IM app's owner (if the platform can tell).
	Owners  []string `toml:"owners"`
	Admins  []string `toml:"admins"`  // may command workers like the owner
	Members []string `toml:"members"` // own isolated workspaces + ask read-only workers; "*" = everyone
	// Roles that may hand questions to read-only shared workers
	// (default ["member"]; add "visitor" to let anyone ask).
	AskRoles []string `toml:"ask_roles"`
	// "shared" (default): one conversation per chat. "per_user": in group
	// chats too, every person has their own conversation and brain session.
	Isolation string `toml:"isolation"`
	DataDir   string `toml:"data_dir"` // single-bot shorthand only; prefer top-level data_dir
	// Workers this bot may use (empty = all configured workers). A bot can
	// only reach sessions that one of its workers covers — nothing else on
	// the machine.
	Workers            []string `toml:"workers"`
	MaxConcurrentTurns int      `toml:"max_concurrent_turns"`
	TurnTimeout        Duration `toml:"turn_timeout"`
	Debounce           Duration `toml:"debounce"`
	HistoryLimit       int      `toml:"history_limit"`
}

// Brain is the pluggable front agent. bot-connect has no model loop of its
// own: it runs an existing agent CLI per turn and gives it tools + context.
type Brain struct {
	// "claudecode" | "codex" | "command"
	Agent   string `toml:"agent"`
	WorkDir string `toml:"work_dir"` // the brain's own scratch directory
	Model   string `toml:"model"`

	// claudecode: built-in tools the brain keeps (default none: it only gets
	// bot-connect's tools). e.g. ["Read", "Grep", "Glob"] or ["WebSearch"].
	BuiltinTools []string `toml:"builtin_tools"`
	// claudecode: directories its file tools may read (besides its own
	// work_dir). Reads elsewhere need an approval nobody gives, so they fail.
	ReadDirs []string `toml:"read_dirs"`
	// claudecode: paths its file tools may never read (default: same as workers).
	DenyRead *[]string `toml:"deny_read"`
	// claudecode: keep the owner's Claude setup (~/.claude/CLAUDE.md, memory,
	// hooks, plugins, settings) out of the brain. Default true. Uses --bare
	// when an API key is available, else --setting-sources "".
	Isolate *bool `toml:"isolate"`
	// codex: confine the brain with a permission profile (minimal system
	// reads + work_dir + read_dirs; deny_read unreadable), OS-enforced.
	// Default true.
	Confine *bool `toml:"confine"`
	// codex: sandbox for the brain process (default read-only).
	Sandbox string `toml:"sandbox"`
	// command: argv of any agent CLI. "{prompt}" is replaced by the turn
	// prompt; without it the prompt goes to stdin. stdout is the reply. The
	// process gets BOT_CONNECT_API and can call `bot-connect tool ...`.
	Command []string `toml:"command"`
	// Extra args appended for claudecode / codex.
	ExtraArgs []string `toml:"extra_args"`
	// claudecode: provider name from [[providers]].
	Provider string `toml:"provider"`

	// ---- user-defined behaviour ----
	// The brain's own instructions (persona, style, rules). Replaces the
	// default persona; the framework protocol (how tools, tasks and context
	// work) is always included.
	SystemPrompt     string `toml:"system_prompt"`
	SystemPromptFile string `toml:"system_prompt_file"`
	// bot-connect tools this brain may use (default: all).
	Tools []string `toml:"tools"`
	// Extra MCP servers for the brain, next to bot-connect's own.
	MCPServers []MCPServer `toml:"mcp_servers"`
	// Extra environment for the brain process.
	EnvVars map[string]string `toml:"env"`

	Env []string `toml:"-"` // resolved: provider env + EnvVars
}

// MCPServer is an extra MCP server handed to the brain: either remote (URL)
// or local (Command + Args).
type MCPServer struct {
	Name    string            `toml:"name"`
	URL     string            `toml:"url"`
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
}

type Server struct {
	Listen string `toml:"listen"` // tool server address, default 127.0.0.1:0 (random port)
}

type Feishu struct {
	AppID     string `toml:"app_id"`
	AppSecret string `toml:"app_secret"`
	Domain    string `toml:"domain"` // empty = feishu.cn; "https://open.larksuite.com" for Lark
	Reaction  string `toml:"reaction"`
	// Use an app held by lark-cli instead of app_id/app_secret: messages flow
	// through `lark-cli event consume` / `lark-cli api` with this profile.
	LarkCLIProfile string `toml:"larkcli_profile"`
}

type Worker struct {
	Name        string `toml:"name" json:"name"`
	Agent       string `toml:"agent" json:"agent"` // "claudecode" | "codex"
	WorkDir     string `toml:"work_dir" json:"work_dir"`
	Description string `toml:"description" json:"description"`
	Public      bool   `toml:"public" json:"public"` // visible (status only) to visitors
	// What the worker may do, enforced per agent (Claude Code: permission
	// mode + tools + OS sandbox + deny rules; Codex: sandbox):
	//   readonly  — read work_dir (+read_dirs), no writes, no side effects
	//   workspace — write only work_dir (+write_dirs); shell sandboxed (default)
	//   full      — no sandbox (deny_read still applies to file tools)
	Access    string   `toml:"access" json:"access,omitempty"`
	ReadDirs  []string `toml:"read_dirs" json:"read_dirs,omitempty"`
	WriteDirs []string `toml:"write_dirs" json:"write_dirs,omitempty"`
	// Per-user isolation: members who use this worker get their own
	// instance (own session) in their own directory: "dir" (an empty
	// directory under per_user_root) or "worktree" (a git worktree of work_dir).
	PerUser     string `toml:"per_user" json:"per_user,omitempty"`
	PerUserRoot string `toml:"per_user_root" json:"per_user_root,omitempty"`
	// Advanced, agent-native overrides (take precedence over access):
	Tools          []string `toml:"tools" json:"tools,omitempty"` // claudecode: built-in tool allowlist
	PermissionMode string   `toml:"permission_mode" json:"permission_mode,omitempty"`
	Sandbox        string   `toml:"sandbox" json:"sandbox,omitempty"`
	Model          string   `toml:"model" json:"model,omitempty"`
	// Which agent sessions this worker covers (default: only the ones
	// bot-connect starts for it):
	//   session_id   — the session it continues by default
	//   sessions     — more existing session ids it may read and continue
	//   dir_sessions — every session of this agent whose directory is work_dir
	SessionID   string   `toml:"session_id" json:"session_id,omitempty"`
	Sessions    []string `toml:"sessions" json:"sessions,omitempty"`
	DirSessions bool     `toml:"dir_sessions" json:"dir_sessions,omitempty"`
	TaskTimeout Duration `toml:"task_timeout" json:"task_timeout,omitempty"`
	Provider    string   `toml:"provider" json:"provider,omitempty"` // claudecode only
	// claudecode: paths the worker's file tools may not read (Claude Code
	// permission deny rules). Default: other agents' session stores and
	// common secret dirs. Set to [] to disable. Shell commands are only
	// covered when the worker can't run them unapproved (acceptEdits).
	DenyRead *[]string `toml:"deny_read" json:"deny_read,omitempty"`
	// claudecode: run the worker's shell inside Claude Code's sandbox (OS-level,
	// seatbelt on macOS), so deny_read also holds for scripts and commands, and
	// writes stay in work_dir. Default true; set false if a workflow needs
	// broader access.
	Confine *bool `toml:"confine" json:"confine,omitempty"`
	// Don't load the owner's agent setup into this worker (Claude:
	// ~/.claude/CLAUDE.md, settings, hooks, MCP connectors; Codex: an isolated
	// CODEX_HOME without ~/.codex config, global AGENTS.md, plugins). Default: true for
	// workers others can use (per_user instances, readonly), false otherwise.
	Isolate *bool `toml:"isolate" json:"isolate,omitempty"`

	Env       []string `toml:"-" json:"-"` // resolved from Provider
	CodexHome string   `toml:"-" json:"-"` // isolated CODEX_HOME (set by the worker manager)
}

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

func Load(path string) (*Config, error) {
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return nil, err
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	if err := c.resolve(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) normalize() error {
	if c.DataDir == "" {
		c.DataDir = c.Bot.DataDir
	}
	if c.DataDir == "" {
		c.DataDir = "~/.bot-connect"
	}
	c.DataDir = ExpandHome(c.DataDir)
	single := len(c.Bots) == 0
	if single {
		c.Bots = []BotConfig{{Bot: c.Bot, Brain: c.Brain, Feishu: c.Feishu}}
	}
	for i := range c.Bots {
		b := &c.Bots[i]
		if b.Name == "" {
			if !single {
				return fmt.Errorf("[[bots]] #%d needs a name", i+1)
			}
			b.Name = "Bot"
		}
		if single {
			b.Dir = c.DataDir // keep pre-multi-bot state where it was
		} else {
			b.Dir = filepath.Join(c.DataDir, "bots", safeName(b.Name))
		}
		applyBotDefaults(b)
	}
	if c.Server.Listen == "" {
		c.Server.Listen = "127.0.0.1:0"
	}
	for i := range c.Workers {
		w := &c.Workers[i]
		w.WorkDir = ExpandHome(w.WorkDir)
		ApplyWorkerDefaults(w)
	}
	return nil
}

func applyBotDefaults(b *BotConfig) {
	if b.OwnerName == "" {
		b.OwnerName = "owner"
	}
	if b.MaxConcurrentTurns <= 0 {
		b.MaxConcurrentTurns = 3
	}
	if b.TurnTimeout.Duration <= 0 {
		b.TurnTimeout.Duration = 5 * time.Minute
	}
	if b.Debounce.Duration <= 0 {
		b.Debounce.Duration = 800 * time.Millisecond
	}
	if b.HistoryLimit <= 0 {
		b.HistoryLimit = 30
	}
	if b.AskRoles == nil {
		b.AskRoles = []string{"member"}
	}
	if b.Isolation == "" {
		b.Isolation = "shared"
	}
	if b.Brain.Agent == "" {
		b.Brain.Agent = "claudecode"
	}
	if b.Brain.WorkDir == "" {
		b.Brain.WorkDir = filepath.Join(b.Dir, "brain")
	}
	b.Brain.WorkDir = ExpandHome(b.Brain.WorkDir)
	if b.Brain.Sandbox == "" {
		b.Brain.Sandbox = "read-only"
	}
	if b.Brain.DenyRead == nil {
		d := append([]string(nil), DefaultDenyRead...)
		b.Brain.DenyRead = &d
	}
	if b.Brain.Isolate == nil {
		t := true
		b.Brain.Isolate = &t
	}
	if b.Brain.Confine == nil {
		t := true
		b.Brain.Confine = &t
	}
	for i, d := range b.Brain.ReadDirs {
		b.Brain.ReadDirs[i] = ExpandHome(d)
	}
	if b.Feishu.Reaction == "" {
		b.Feishu.Reaction = "OnIt"
	}
}

// safeName makes a bot name usable as a directory name.
func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == ' ' {
			return '-'
		}
		return r
	}, s)
}

// DefaultDenyRead keeps workers out of other sessions and credentials.
var DefaultDenyRead = []string{"~/.claude/projects/**", "~/.codex/sessions/**", "~/.codex/auth.json",
	"~/.ssh/**", "~/.aws/**", "~/.config/gcloud/**", "~/.netrc"}

func ApplyWorkerDefaults(w *Worker) {
	if w.DenyRead == nil {
		d := append([]string(nil), DefaultDenyRead...)
		w.DenyRead = &d
	}
	if w.Confine == nil {
		// OS-enforced by default: Claude Code's sandbox, Codex's permission profile.
		t := w.Access != "full"
		w.Confine = &t
	}
	if w.Isolate == nil {
		t := w.PerUser != "" || w.Access == "readonly"
		w.Isolate = &t
	}
	if w.Agent == "" {
		w.Agent = "claudecode"
	}
	if w.TaskTimeout.Duration <= 0 {
		w.TaskTimeout.Duration = 30 * time.Minute
	}
	if w.Access == "" {
		w.Access = "workspace"
	}
	if w.Agent == "codex" && w.Sandbox == "" {
		w.Sandbox = map[string]string{"readonly": "read-only", "workspace": "workspace-write", "full": "danger-full-access"}[w.Access]
	}
	for i, d := range w.ReadDirs {
		w.ReadDirs[i] = ExpandHome(d)
	}
	for i, d := range w.WriteDirs {
		w.WriteDirs[i] = ExpandHome(d)
	}
	w.PerUserRoot = ExpandHome(w.PerUserRoot)
}

func (c *Config) validate() error {
	seen := map[string]bool{}
	for _, w := range c.Workers {
		if w.Name == "" || w.WorkDir == "" {
			return fmt.Errorf("worker requires name and work_dir")
		}
		if strings.ContainsAny(w.Name, "@#") {
			return fmt.Errorf("worker %q: name may not contain @ or #", w.Name)
		}
		switch w.Access {
		case "readonly", "workspace", "full":
		default:
			return fmt.Errorf("worker %s: access must be readonly, workspace or full", w.Name)
		}
		switch w.PerUser {
		case "", "dir", "worktree":
		default:
			return fmt.Errorf("worker %s: per_user must be \"dir\" or \"worktree\"", w.Name)
		}
		if seen[w.Name] {
			return fmt.Errorf("duplicate worker name %q", w.Name)
		}
		seen[w.Name] = true
	}
	bots, apps := map[string]bool{}, map[string]string{}
	for _, b := range c.Bots {
		if b.Isolation != "shared" && b.Isolation != "per_user" {
			return fmt.Errorf("bot %s: isolation must be shared or per_user", b.Name)
		}
		if bots[b.Name] {
			return fmt.Errorf("duplicate bot name %q", b.Name)
		}
		bots[b.Name] = true
		for _, w := range b.Workers {
			if !seen[w] {
				return fmt.Errorf("bot %s: unknown worker %q", b.Name, w)
			}
		}
		// Two bots on one IM app would split its events between them.
		for _, app := range []string{b.Feishu.AppID, "larkcli:" + b.Feishu.LarkCLIProfile} {
			if app == "" || app == "larkcli:" {
				continue
			}
			if other, ok := apps[app]; ok {
				return fmt.Errorf("bots %s and %s use the same Feishu app (%s)", other, b.Name, app)
			}
			apps[app] = b.Name
		}
	}
	return nil
}

func (c *Config) resolve() error {
	all := c.Providers
	find := func(name string) (Provider, error) {
		for _, p := range all {
			if p.Name == name {
				if p.APIKey == "" && p.APIKeyEnv != "" {
					p.APIKey = os.Getenv(p.APIKeyEnv)
				}
				if p.BaseURL == "" {
					return p, fmt.Errorf("provider %q has no base_url", name)
				}
				return p, nil
			}
		}
		return Provider{}, fmt.Errorf("unknown provider %q", name)
	}
	for i := range c.Bots {
		b := &c.Bots[i]
		if err := resolveBrain(&b.Brain, find); err != nil {
			return fmt.Errorf("bot %s: %w", b.Name, err)
		}
	}
	for i := range c.Workers {
		w := &c.Workers[i]
		if w.Provider == "" {
			continue
		}
		if w.Agent != "claudecode" {
			return fmt.Errorf("worker %s: provider applies to claudecode only", w.Name)
		}
		p, err := find(w.Provider)
		if err != nil {
			return fmt.Errorf("worker %s: %w", w.Name, err)
		}
		w.Env = p.ClaudeEnv()
		if w.Isolate != nil && *w.Isolate && p.APIKey != "" {
			w.Env = append(w.Env, "ANTHROPIC_API_KEY="+p.APIKey)
		}
	}
	return nil
}

func resolveBrain(br *Brain, find func(string) (Provider, error)) error {
	if br.SystemPromptFile != "" {
		b, err := os.ReadFile(ExpandHome(br.SystemPromptFile))
		if err != nil {
			return fmt.Errorf("brain.system_prompt_file: %w", err)
		}
		if br.SystemPrompt != "" {
			br.SystemPrompt += "\n\n"
		}
		br.SystemPrompt += string(b)
	}
	for k, v := range br.EnvVars {
		br.Env = append(br.Env, k+"="+v)
	}
	for _, m := range br.MCPServers {
		if m.Name == "" || m.Name == "bot" || (m.URL == "") == (m.Command == "") {
			return fmt.Errorf("brain.mcp_servers: each needs a unique name (not \"bot\") and exactly one of url / command")
		}
	}
	if br.Provider != "" {
		if br.Agent == "codex" {
			return fmt.Errorf("brain.provider applies to claudecode only; configure codex providers in ~/.codex/config.toml")
		}
		p, err := find(br.Provider)
		if err != nil {
			return fmt.Errorf("brain: %w", err)
		}
		br.Env = append(br.Env, p.ClaudeEnv()...)
		if *br.Isolate && p.APIKey != "" {
			// --bare only reads ANTHROPIC_API_KEY.
			br.Env = append(br.Env, "ANTHROPIC_API_KEY="+p.APIKey)
		}
	}
	return nil
}

func ExpandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}
