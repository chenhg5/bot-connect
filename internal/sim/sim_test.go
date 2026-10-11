package sim

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/evals"
	"github.com/chenhg5/bot-connect/internal/brain"
	"github.com/chenhg5/bot-connect/internal/config"
)

// fakeBrain answers every turn with a fixed text: enough to exercise the
// harness and the scaffold's own rules without a model.
type fakeBrain struct{}

func (fakeBrain) Name() string     { return "sim-fake" }
func (fakeBrain) Caps() brain.Caps { return brain.Caps{Sessions: false, SystemPrompt: true} }
func (fakeBrain) Run(ctx context.Context, req brain.Request) (brain.Response, error) {
	if strings.Contains(req.Prompt, "过期") {
		return brain.Response{Text: "王五两天没有回应，委托已过期，事项回到待分派。"}, nil
	}
	return brain.Response{Text: "NO_REPLY"}, nil
}

func init() {
	brain.Register("sim-fake", func(cfg config.Brain) (brain.Adapter, error) { return fakeBrain{}, nil })
}

func TestGoldenCasesParse(t *testing.T) {
	cases, err := LoadFS(evals.Golden, "golden")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < 10 {
		t.Fatalf("only %d cases", len(cases))
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.ID] {
			t.Fatalf("duplicate case %s", c.ID)
		}
		seen[c.ID] = true
	}
}

func TestHarnessRunsTheFollowUpCaseWithoutAModel(t *testing.T) {
	cases, _ := LoadFS(evals.Golden, "golden")
	var g9 Case
	for _, c := range cases {
		if c.ID == "G9" {
			g9 = c
		}
	}
	cfg := &config.Config{Bots: []config.BotConfig{{Brain: config.Brain{Agent: "sim-fake"}}}}
	h, err := New(context.Background(), cfg, g9)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	r := h.Run(context.Background())
	if !r.Pass {
		t.Fatalf("G9 with the scripted brain: %v\n%s", r.Failures, r.Transcript)
	}
}

func TestEveryCaseDeclaresCapabilities(t *testing.T) {
	known := map[string]bool{"triage": true, "planning": true, "explore": true, "delegation": true, "risk": true, "state": true,
		"pm": true, "boundaries": true, "communication": true, "judgment": true}
	cases, err := LoadFS(evals.Golden, "golden")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if len(c.Capabilities) == 0 {
			t.Errorf("%s declares no capabilities", c.ID)
		}
		for _, x := range c.Capabilities {
			if !known[x] {
				t.Errorf("%s: unknown capability %q", c.ID, x)
			}
		}
	}
}

func TestLongScenariosParse(t *testing.T) {
	cases, err := LoadFS(evals.Long, "long")
	if err != nil || len(cases) == 0 {
		t.Fatalf("long scenarios: %v (%d)", err, len(cases))
	}
}

func TestParseSpan(t *testing.T) {
	for in, want := range map[string]time.Duration{"4h": 4 * time.Hour, "2d": 48 * time.Hour, "1w": 7 * 24 * time.Hour, "": 0} {
		if got, err := parseSpan(in); err != nil || got != want {
			t.Fatalf("parseSpan(%q) = %v %v", in, got, err)
		}
	}
}
