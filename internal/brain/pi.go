package brain

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/bot-connect/internal/agentcli"
	"github.com/chenhg5/bot-connect/internal/config"
)

//go:embed pi_tools.ts
var piToolsExtension []byte

func init() {
	Register("pi", func(c config.Brain) (Adapter, error) {
		iso := c.Isolate == nil || *c.Isolate
		if iso && c.ProviderSpec == nil {
			return nil, fmt.Errorf("a pi brain runs isolated from your own pi setup, so it needs brain.provider " +
				"(an Anthropic-compatible endpoint from [[providers]]); or set isolate = false to use your pi login")
		}
		return &piAdapter{cfg: c}, nil
	})
}

// piAdapter drives pi (github.com/badlogic/pi-mono). pi has no MCP; bot-connect
// ships an extension that registers its tools, and `--tools` allows only
// those (plus builtin_tools), so the brain has no shell or file access unless
// configured. Context files, extensions, skills and templates are not loaded;
// with isolate (default) pi also gets its own config dir whose only model is
// the configured provider.
type piAdapter struct{ cfg config.Brain }

func (a *piAdapter) Name() string { return "pi" }
func (a *piAdapter) Caps() Caps   { return Caps{Sessions: true, SystemPrompt: true} }

func (a *piAdapter) home() string { return filepath.Join(filepath.Dir(a.cfg.WorkDir), "pi") }

func (a *piAdapter) prepare() (env []string, args []string, err error) {
	home := a.home()
	ext := filepath.Join(home, "bot-connect-tools.ts")
	if err := os.MkdirAll(filepath.Join(home, "agent"), 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(ext, piToolsExtension, 0o600); err != nil {
		return nil, nil, err
	}
	args = []string{"--no-context-files", "--no-extensions", "-e", ext, "--no-skills", "--no-prompt-templates", "--offline",
		"--session-dir", filepath.Join(home, "sessions")}
	if a.cfg.Isolate == nil || *a.cfg.Isolate {
		p := a.cfg.ProviderSpec
		model := firstNonEmpty(a.cfg.Model, p.Model)
		models := map[string]any{"providers": map[string]any{"bot-connect": map[string]any{
			"baseUrl": p.BaseURL, "api": "anthropic-messages", "apiKey": "BOT_CONNECT_PI_KEY",
			"models": []any{map[string]any{"id": model, "name": model, "contextWindow": 200000, "maxTokens": 8192}},
		}}}
		b, _ := json.Marshal(models)
		if err := os.WriteFile(filepath.Join(home, "agent", "models.json"), b, 0o600); err != nil {
			return nil, nil, err
		}
		env = append(env, "PI_CODING_AGENT_DIR="+filepath.Join(home, "agent"), "BOT_CONNECT_PI_KEY="+p.APIKey)
		args = append(args, "--provider", "bot-connect", "--model", model)
	} else if a.cfg.Model != "" {
		args = append(args, "--model", a.cfg.Model)
	}
	return env, args, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func (a *piAdapter) Run(ctx context.Context, req Request) (Response, error) {
	env, args, err := a.prepare()
	if err != nil {
		return Response{}, err
	}
	tools := append(append([]string(nil), req.ToolNames...), a.cfg.BuiltinTools...)
	args = append([]string{"-p", req.Prompt, "--mode", "json", "--append-system-prompt", req.SystemPrompt,
		"--tools", strings.Join(tools, ",")}, args...)
	if req.SessionID != "" {
		args = append(args, "--session", req.SessionID)
	}
	args = append(args, a.cfg.ExtraArgs...)
	res, err := agentcli.Pi(ctx, agentcli.Call{Dir: a.cfg.WorkDir, Env: append(toolEnv(req), env...), Args: args})
	return Response{Text: res.Text, SessionID: res.SessionID}, err
}
