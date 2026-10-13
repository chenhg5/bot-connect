package brain

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/chenhg5/bot-connect/internal/agentcli"
	"github.com/chenhg5/bot-connect/internal/config"
)

func init() {
	Register("kiro", func(c config.Brain) (Adapter, error) {
		return &kiroAdapter{cfg: c}, nil
	})
}

// kiroAdapter drives Kiro via the kiro-acp CLI (from kiro-acp-gateway project).
// kiro-acp calls kiro-cli acp (Agent Client Protocol), which provides tool access
// via the BOT_CONNECT_API environment variable. Kiro manages its own session
// storage in ~/.kiro/sessions/.
type kiroAdapter struct{ cfg config.Brain }

func (a *kiroAdapter) Name() string { return "kiro" }

// Caps: Kiro supports sessions via --session flag; system prompts must be inlined
// into the user prompt (kiro-acp doesn't have --append-system-prompt).
func (a *kiroAdapter) Caps() Caps { return Caps{Sessions: true, SystemPrompt: false} }

func (a *kiroAdapter) Run(ctx context.Context, req Request) (Response, error) {
	// Map model names: "sonnet" -> "claude-sonnet-4-20250514", etc.
	model := a.mapModel(a.cfg.Model)

	// Validate model to prevent flag injection
	if err := validateModel(model); err != nil {
		return Response{}, err
	}

	// Build prompt: inline system prompt if this is a new session or fresh turn
	prompt := req.Prompt
	if req.SystemPrompt != "" {
		prompt = req.SystemPrompt + "\n\n" + prompt
	}

	// Build args: kiro-acp prompt --model <model> --output json --cwd <workdir> [--session <id>] "<prompt>"
	args := []string{"prompt", "--output", "json", "--model", model}

	// Set workspace directory
	args = append(args, "--cwd", a.cfg.WorkDir)

	// Resume existing session if provided
	if req.SessionID != "" {
		args = append(args, "--session", req.SessionID)
	}

	// Set permissions policy (allow tools to run without prompting)
	args = append(args, "--permissions", "allow-all")

	// Add any extra args from config
	args = append(args, a.cfg.ExtraArgs...)

	// The prompt is the last argument
	args = append(args, prompt)

	// Prepare environment: BOT_CONNECT_API for tool access
	env := toolEnv(req)
	env = append(env, a.cfg.Env...)

	// Run kiro-acp
	res, err := agentcli.Kiro(ctx, agentcli.Call{
		Dir:  a.cfg.WorkDir,
		Env:  env,
		Args: args,
	})

	return Response{Text: res.Text, SessionID: res.SessionID}, err
}

// mapModel converts common model names to full Kiro model identifiers.
// Based on kiro-acp models output, common mappings:
// - "sonnet" -> "claude-sonnet-4-20250514"
// - "opus" -> "claude-opus-4-20250514"
// If the model is already a full name, it's returned as-is.
func (a *kiroAdapter) mapModel(model string) string {
	if model == "" {
		return "claude-sonnet-4-20250514" // default
	}

	// Common short names to full identifiers
	mappings := map[string]string{
		"sonnet":            "claude-sonnet-4-20250514",
		"opus":              "claude-opus-4-20250514",
		"claude-sonnet":     "claude-sonnet-4-20250514",
		"claude-opus":       "claude-opus-4-20250514",
		"haiku":             "claude-3-5-haiku-20241022",
		"claude-haiku":      "claude-3-5-haiku-20241022",
		"sonnet-3.5":        "claude-3-5-sonnet-20241022",
		"claude-sonnet-3.5": "claude-3-5-sonnet-20241022",
	}

	// Check if it's a short name we should map
	if mapped, ok := mappings[strings.ToLower(model)]; ok {
		return mapped
	}

	// If it contains "claude-" or looks like a full model ID, use as-is
	if strings.Contains(model, "claude-") || strings.Contains(model, "gpt-") {
		return model
	}

	// Otherwise, assume it's a partial match and return as-is (let kiro-acp validate)
	return model
}

// home returns the Kiro home directory for this brain's session storage.
// Kiro stores sessions in ~/.kiro/sessions/ by default.
func (a *kiroAdapter) home() string {
	return filepath.Join(filepath.Dir(a.cfg.WorkDir), "kiro")
}

// validateModel checks that a model name is safe to pass as a command argument.
// Rejects model names that start with - (flag injection) or contain invalid characters.
// Valid model names: alphanumerics, dots, hyphens (e.g., "claude-sonnet-4-20250514").
func validateModel(model string) error {
	if model == "" {
		return fmt.Errorf("model name cannot be empty")
	}
	if strings.HasPrefix(model, "-") {
		return fmt.Errorf("model name cannot start with - (potential flag injection): %q", model)
	}
	// Allow alphanumerics, dots, hyphens, underscores
	validPattern := regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	if !validPattern.MatchString(model) {
		return fmt.Errorf("model name contains invalid characters (allowed: a-z, A-Z, 0-9, ., -, _): %q", model)
	}
	return nil
}
