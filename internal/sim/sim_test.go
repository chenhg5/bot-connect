package sim

import (
	"context"
	"strings"
	"testing"

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
