package worker

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/bot-connect/internal/config"
)

func TestCodexProfileIsOneTOMLValue(t *testing.T) {
	o := CodexProfile("/Users/me/.bot-connect/w", true, []string{"/data/ro"}, nil, []string{"~/.ssh/**"})
	if len(o) != 2 || o[0] != `default_permissions="bot-connect"` {
		t.Fatalf("unexpected overrides: %v", o)
	}
	k, v, _ := strings.Cut(o[1], "=")
	if k != "permissions.bot-connect.filesystem" {
		t.Fatalf("key %q", k)
	}
	var doc struct{ V map[string]string }
	if _, err := toml.Decode("V = "+v, &doc); err != nil {
		t.Fatalf("value is not valid TOML: %v\n%s", err, v)
	}
	want := map[string]string{":minimal": "read", "/Users/me/.bot-connect/w": "write", "/data/ro": "read", "~/.ssh": "deny"}
	for p, m := range want {
		if doc.V[p] != m {
			t.Fatalf("%s = %q, want %q (all: %v)", p, doc.V[p], m, doc.V)
		}
	}
}

func TestClaudeAccessLevels(t *testing.T) {
	ro := config.Worker{Name: "r", Agent: "claudecode", WorkDir: "/w", Access: "readonly"}
	config.ApplyWorkerDefaults(&ro)
	a := strings.Join(ClaudeAccessArgs(ro), " ")
	if !strings.Contains(a, "--permission-mode default") || !strings.Contains(a, "--tools Read,Grep,Glob") || strings.Contains(a, `"sandbox"`) != SandboxSupported() || !strings.Contains(a, "--strict-mcp-config") {
		t.Fatalf("readonly args: %s", a)
	}
	ws := config.Worker{Name: "w", Agent: "claudecode", WorkDir: "/w", WriteDirs: []string{"/out"}}
	config.ApplyWorkerDefaults(&ws)
	a = strings.Join(ClaudeAccessArgs(ws), " ")
	if !strings.Contains(a, "bypassPermissions") || !strings.Contains(a, "--add-dir /out") || strings.Contains(a, `"sandbox"`) != SandboxSupported() || strings.Contains(a, "--strict-mcp-config") {
		t.Fatalf("workspace args: %s", a)
	}
	full := config.Worker{Name: "f", Agent: "claudecode", WorkDir: "/w", Access: "full"}
	config.ApplyWorkerDefaults(&full)
	if a = strings.Join(ClaudeAccessArgs(full), " "); strings.Contains(a, `"sandbox"`) {
		t.Fatalf("full must not sandbox: %s", a)
	}
}

func TestClaudeRulePath(t *testing.T) {
	if got := claudeRulePath("/Users/a/x"); got != "/Users/a/x" {
		t.Fatalf("posix path changed: %s", got)
	}
	if SandboxSupported() {
		return // Windows volume syntax only parses on Windows
	}
	if got := claudeRulePath(`C:\Users\a\x`); got != "/c/Users/a/x" {
		t.Fatalf("windows path: %s", got)
	}
}
