package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Claude answers judge questions with a small Claude model through the
// local CLI (`claude -p`). Slower than a System One model and its
// probabilities are self-reported, so it is a fallback — for evals and for
// when no judgment service is available — not a replacement.
type Claude struct {
	Model   string // default haiku
	Timeout time.Duration
}

func (c Claude) Ask(ctx context.Context, state any, qs map[string]Question) (map[string]Answer, error) {
	st, _ := json.MarshalIndent(state, "", "  ")
	ids := make([]string, 0, len(qs))
	for id := range qs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("You judge a state. Answer every question below and output ONLY one JSON object, no prose, no code fence.\n" +
		"For a \"noul\" question output {\"p\": <probability 0..1 that the answer is yes>}.\n" +
		"For a \"choice\" question output {\"choice\": \"<one option key>\", \"probs\": {\"<option>\": <probability>, …}} (probabilities sum to 1).\n" +
		"For a \"score\" question output {\"score\": <position between 0 and the last level index, may be fractional>}.\n" +
		"Output shape: {\"<question id>\": {…}, …}. Be calibrated: 0.5 means unsure.\n\nSTATE:\n")
	b.Write(st)
	b.WriteString("\n\nQUESTIONS:\n")
	for _, id := range ids {
		q := qs[id]
		crit, _ := json.Marshal(q.Criteria)
		fmt.Fprintf(&b, "- id %q, type %s: %v", id, q.Type, q.Instructions)
		if q.Criteria != nil {
			fmt.Fprintf(&b, " Criteria: %s", crit)
		}
		b.WriteString("\n")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "claude", "-p", b.String(), "--model", firstNonEmptyJ(c.Model, "haiku"), "--output-format", "text", "--tools", "", "--setting-sources", "")
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "ANTHROPIC_") && !strings.HasPrefix(e, "CLAUDECODE") {
			env = append(env, e)
		}
	}
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("claude judge: %w", err)
	}
	text := strings.TrimSpace(string(out))
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		text = text[i : j+1]
	}
	var raw map[string]struct {
		P      *float64           `json:"p"`
		Choice string             `json:"choice"`
		Probs  map[string]float64 `json:"probs"`
		Score  *float64           `json:"score"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, fmt.Errorf("claude judge: unreadable answer: %v", err)
	}
	ans := map[string]Answer{}
	for _, id := range ids {
		r, ok := raw[id]
		if !ok {
			return nil, fmt.Errorf("claude judge: no answer for %s", id)
		}
		a := Answer{Type: qs[id].Type}
		switch qs[id].Type {
		case "noul":
			if r.P != nil {
				a.Noul = clamp01(*r.P)
			}
		case "choice":
			a.Choice, a.Probabilities = r.Choice, r.Probs
			if a.Probabilities == nil {
				a.Probabilities = map[string]float64{r.Choice: 1}
			}
			a.Confidence = a.Probabilities[r.Choice]
		case "score":
			if r.Score != nil {
				a.Score = *r.Score
			}
		}
		ans[id] = a
	}
	return ans, nil
}

func clamp01(x float64) float64 { return min(max(x, 0), 1) }

func firstNonEmptyJ(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}
