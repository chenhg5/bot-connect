package brain

import (
	"testing"

	"github.com/chenhg5/bot-connect/internal/config"
)

func TestKiroAdapter_Name(t *testing.T) {
	a := &kiroAdapter{}
	if got := a.Name(); got != "kiro" {
		t.Errorf("Name() = %q, want %q", got, "kiro")
	}
}

func TestKiroAdapter_Caps(t *testing.T) {
	a := &kiroAdapter{}
	caps := a.Caps()
	if !caps.Sessions {
		t.Error("Caps().Sessions = false, want true")
	}
	if caps.SystemPrompt {
		t.Error("Caps().SystemPrompt = true, want false (kiro-acp requires inline system prompts)")
	}
}

func TestKiroAdapter_MapModel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "claude-sonnet-4-20250514"},
		{"sonnet", "claude-sonnet-4-20250514"},
		{"opus", "claude-opus-4-20250514"},
		{"haiku", "claude-3-5-haiku-20241022"},
		{"claude-sonnet", "claude-sonnet-4-20250514"},
		{"claude-opus", "claude-opus-4-20250514"},
		{"claude-haiku", "claude-3-5-haiku-20241022"},
		{"sonnet-3.5", "claude-3-5-sonnet-20241022"},
		{"claude-sonnet-3.5", "claude-3-5-sonnet-20241022"},
		{"claude-sonnet-4-20250514", "claude-sonnet-4-20250514"}, // already full name
		{"gpt-6-sol", "gpt-6-sol"},                               // non-claude model
		{"custom-model", "custom-model"},                         // unknown short name
	}

	a := &kiroAdapter{cfg: config.Brain{}}
	for _, tt := range tests {
		got := a.mapModel(tt.input)
		if got != tt.want {
			t.Errorf("mapModel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestKiroAdapter_Registration(t *testing.T) {
	// Test that the kiro adapter is registered
	cfg := config.Brain{
		Agent:   "kiro",
		WorkDir: "/tmp/test-kiro",
		Model:   "sonnet",
	}

	adapter, err := newAdapter(cfg)
	if err != nil {
		t.Fatalf("newAdapter(kiro) failed: %v", err)
	}

	if adapter.Name() != "kiro" {
		t.Errorf("adapter.Name() = %q, want %q", adapter.Name(), "kiro")
	}
}
