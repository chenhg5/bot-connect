package brain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/bot-connect/internal/config"
)

func TestPiNeedsProviderWhenIsolated(t *testing.T) {
	if _, err := newAdapter(config.Brain{Agent: "pi", WorkDir: t.TempDir()}); err == nil {
		t.Fatal("isolated pi without a provider must be refused")
	}
	off := false
	if _, err := newAdapter(config.Brain{Agent: "pi", WorkDir: t.TempDir(), Isolate: &off}); err != nil {
		t.Fatalf("isolate=false uses the user's pi setup: %v", err)
	}
}

func TestPiIsolatedSetup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "brain")
	ad, err := newAdapter(config.Brain{Agent: "pi", WorkDir: dir,
		ProviderSpec: &config.Provider{BaseURL: "https://example.test/anthropic", APIKey: "k", Model: "fast-1"}})
	if err != nil {
		t.Fatal(err)
	}
	env, args, err := ad.(*piAdapter).prepare()
	if err != nil {
		t.Fatal(err)
	}
	a := strings.Join(args, " ")
	for _, want := range []string{"--no-context-files", "--no-extensions -e ", "--no-skills", "--provider bot-connect", "--model fast-1"} {
		if !strings.Contains(a, want) {
			t.Fatalf("args lack %q: %s", want, a)
		}
	}
	e := strings.Join(env, " ")
	if !strings.Contains(e, "PI_CODING_AGENT_DIR=") || !strings.Contains(e, "BOT_CONNECT_PI_KEY=k") {
		t.Fatalf("env: %s", e)
	}
	models, err := os.ReadFile(filepath.Join(filepath.Dir(dir), "pi", "agent", "models.json"))
	if err != nil || !strings.Contains(string(models), `"anthropic-messages"`) || strings.Contains(string(models), `"k"`) {
		t.Fatalf("models.json must reference the key by env name, not inline: %s %v", models, err)
	}
}
