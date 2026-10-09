package agentcli

import (
	"slices"
	"strings"
	"testing"
)

func TestClaudeBaseEnv(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SDK_HAS_HOST_AUTH_REFRESH", "1")
	t.Setenv("ANTHROPIC_BASE_URL", "https://api.anthropic.com")
	t.Setenv("KEEP_ME", "1")

	keys := func(env []string) []string {
		var out []string
		for _, e := range env {
			k, _, _ := strings.Cut(e, "=")
			out = append(out, k)
		}
		return out
	}

	// Subscription (no provider): only the nested-session marker goes.
	base, extra := claudeBaseEnv(nil)
	if k := keys(base); slices.Contains(k, "CLAUDECODE") || !slices.Contains(k, "ANTHROPIC_BASE_URL") || !slices.Contains(k, "CLAUDE_CODE_SDK_HAS_HOST_AUTH_REFRESH") {
		t.Fatalf("no-provider base env wrong: %v", k)
	}
	if len(extra) != 0 {
		t.Fatalf("unexpected extra: %v", extra)
	}

	// Provider: inherited auth/routing is dropped and ours is host-managed.
	base, extra = claudeBaseEnv([]string{"ANTHROPIC_BASE_URL=https://p", "ANTHROPIC_AUTH_TOKEN=x"})
	k := keys(base)
	for _, bad := range []string{"CLAUDECODE", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_SDK_HAS_HOST_AUTH_REFRESH"} {
		if slices.Contains(k, bad) {
			t.Fatalf("%s leaked into provider env", bad)
		}
	}
	if !slices.Contains(k, "KEEP_ME") || !slices.Contains(extra, "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST=1") {
		t.Fatalf("provider env wrong: base=%v extra=%v", k, extra)
	}
}
