// Package judge is the L1 judgment layer: narrow, typed judgments with
// probabilities (a System One model such as TypeSafe's Jev), used where
// code needs semantic understanding — what a message means, whether a reply
// leaks something, how well someone fits a need. Callers ask several
// independent questions over the same state in one request, act on the
// answers only when confident enough, and fall back to rules or the brain
// otherwise.
package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Question is one judgment.
//
//	noul   — the probability a condition holds (Answer.Noul)
//	choice — one option of Criteria (map option → description) (Answer.Choice)
//	score  — a position on ordered levels (Criteria: []string, low → high) (Answer.Score)
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

func Noul(instructions string) Question { return Question{Type: "noul", Instructions: instructions} }
func Choice(instructions string, options map[string]string) Question {
	return Question{Type: "choice", Instructions: instructions, Criteria: options}
}
func Score(instructions string, levels ...string) Question {
	return Question{Type: "score", Instructions: instructions, Criteria: levels}
}

type Answer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

// Judge asks questions about a state.
type Judge interface {
	Ask(ctx context.Context, state any, qs map[string]Question) (map[string]Answer, error)
}

// ErrUnavailable: no judge configured (callers fall back).
var ErrUnavailable = errors.New("no judge configured")

// None is the judge used when nothing is configured.
type None struct{}

func (None) Ask(context.Context, any, map[string]Question) (map[string]Answer, error) {
	return nil, ErrUnavailable
}

// Jev calls TypeSafe's System One API.
type Jev struct {
	URL     string // default https://api.typesafe.ai/v1/systemone
	Key     string
	Model   string // default jev-latest
	Timeout time.Duration
	Client  *http.Client
}

func (j *Jev) Ask(ctx context.Context, state any, qs map[string]Question) (map[string]Answer, error) {
	if j.Key == "" {
		return nil, ErrUnavailable
	}
	url, model := j.URL, j.Model
	if url == "" {
		url = "https://api.typesafe.ai/v1/systemone"
	}
	if model == "" {
		model = "jev-latest"
	}
	body, err := json.Marshal(map[string]any{"model": model, "state": state, "questions": qs})
	if err != nil {
		return nil, err
	}
	timeout := j.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	client := j.Client
	if client == nil {
		client = &http.Client{}
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 400 * time.Millisecond):
			}
		}
		rctx, cancel := context.WithTimeout(ctx, timeout)
		req, _ := http.NewRequestWithContext(rctx, "POST", url, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+j.Key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			last = err
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		cancel()
		switch {
		case resp.StatusCode == 429 || resp.StatusCode >= 500:
			last = fmt.Errorf("jev: HTTP %d", resp.StatusCode)
			continue
		case resp.StatusCode != 200:
			return nil, fmt.Errorf("jev: HTTP %d: %s", resp.StatusCode, clip(string(b), 300))
		}
		var out struct {
			Answers map[string]Answer `json:"answers"`
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, fmt.Errorf("jev: %v", err)
		}
		return out.Answers, nil
	}
	return nil, last
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
