package workforce

import (
	"fmt"
	"strings"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// Dim is one dimension of a worker's state; state is several independent
// dimensions, not one enum.
type Dim string

const (
	Presence      Dim = "presence"      // available | busy | away | dnd | off_hours | offline | unavailable
	Load          Dim = "load"          // Num = open work
	Health        Dim = "health"        // ok | degraded (recent failures, quota, auth)
	Context       Dim = "context"       // what it already knows
	Record        Dim = "record"        // track record
	Receptiveness Dim = "receptiveness" // is now a good time to ask: high | normal | low
)

// Source of a fact, in decreasing trust.
type Source string

const (
	Declared Source = "declared"
	Observed Source = "observed"
	Inferred Source = "inferred" // a guess: short-lived, capped confidence, private
)

func rank(s Source) int {
	switch s {
	case Declared:
		return 3
	case Observed:
		return 2
	}
	return 1
}

// MaxInferredTTL caps guesses about a worker.
const MaxInferredTTL = 24 * time.Hour

type Fact struct {
	Dim        Dim       `json:"dim"`
	Value      string    `json:"value"`
	Num        float64   `json:"num,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	Source     Source    `json:"source"`
	Confidence float64   `json:"confidence,omitempty"`
	At         time.Time `json:"at"`
	Expires    time.Time `json:"expires,omitempty"`
}

func (f Fact) live(now time.Time) bool { return f.Expires.IsZero() || now.Before(f.Expires) }

// WorkerState is a separate aggregate from Worker: it changes all the time,
// from several sources, and parts of it expire.
type WorkerState struct {
	Worker WorkerID `json:"worker"`
	Facts  []Fact   `json:"facts"`
}

// Record adds a fact (replacing the same dimension from the same source).
// Inferred facts are capped: at most a day, at most 0.8 confidence.
func (s *WorkerState) Record(f Fact) []Event {
	if f.Source == Inferred {
		if f.Expires.IsZero() || f.Expires.Sub(f.At) > MaxInferredTTL {
			f.Expires = f.At.Add(MaxInferredTTL)
		}
		if f.Confidence <= 0 || f.Confidence > 0.8 {
			f.Confidence = 0.6
		}
	}
	replaced := false
	for i, o := range s.Facts {
		if o.Dim == f.Dim && o.Source == f.Source {
			s.Facts[i], replaced = f, true
		}
	}
	if !replaced {
		s.Facts = append(s.Facts, f)
	}
	return []Event{NewEvent("worker.fact_recorded", "worker:"+string(s.Worker), f.At, System("state"),
		"dim", string(f.Dim), "value", f.Value, "source", string(f.Source))}
}

// Merge returns a copy with observed facts added (they are not persisted:
// drivers report them fresh).
func (s WorkerState) Merge(observed []Fact) WorkerState {
	out := WorkerState{Worker: s.Worker, Facts: append([]Fact(nil), s.Facts...)}
	for _, f := range observed {
		f.Source = Observed
		out.Record(f)
	}
	return out
}

// Prune drops expired facts.
func (s *WorkerState) Prune(now time.Time) {
	kept := s.Facts[:0]
	for _, f := range s.Facts {
		if f.live(now) {
			kept = append(kept, f)
		}
	}
	s.Facts = kept
}

// Get returns the winning fact for a dimension: live, most trusted, newest.
func (s WorkerState) Get(d Dim, now time.Time) (Fact, bool) {
	var best Fact
	ok := false
	for _, f := range s.Facts {
		if f.Dim != d || !f.live(now) {
			continue
		}
		if !ok || rank(f.Source) > rank(best.Source) || (rank(f.Source) == rank(best.Source) && f.At.After(best.At)) {
			best, ok = f, true
		}
	}
	return best, ok
}

// Available reports whether hard facts say the worker can take work now.
// Guesses never block work.
func (s WorkerState) Available(now time.Time) (bool, string) {
	f, ok := s.Get(Presence, now)
	if !ok || f.Source == Inferred {
		return true, ""
	}
	switch f.Value {
	case "offline", "unavailable", "away":
		return false, strings.TrimSpace(f.Value + " " + f.Detail)
	}
	return true, ""
}

// LoadNum is the open-work count, if known.
func (s WorkerState) LoadNum(now time.Time) float64 {
	if f, ok := s.Get(Load, now); ok {
		return f.Num
	}
	return 0
}

// Summary is the one line a brain sees; guesses carry a "?".
func (s WorkerState) Summary(now time.Time) string {
	var parts []string
	for _, d := range []Dim{Presence, Load, Health, Receptiveness, Context, Record} {
		f, ok := s.Get(d, now)
		if !ok {
			continue
		}
		v := f.Value
		if f.Source == Inferred {
			v += "?"
		}
		p := fmt.Sprintf("%s %s", d, v)
		if f.Detail != "" {
			p += " (" + f.Detail + ")"
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, " · ")
}
