package workforce

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Dim is one dimension of a worker's state. State is not one enum: a worker
// can be working on something, close to its quota, and already nudged twice
// today, all at once.
type Dim string

const (
	Presence      Dim = "presence"      // reachable now? available | busy | away | dnd | off_hours | offline | unavailable
	Load          Dim = "load"          // how much it has on its plate; Num = open assignments / queue
	Health        Dim = "health"        // recent failures, quota, auth
	Context       Dim = "context"       // what it already knows (session, branch, recent topics)
	Record        Dim = "record"        // track record: on-time rate, quality, typical duration
	Receptiveness Dim = "receptiveness" // is now a good time to ask? (people / bots) high | normal | low
	Consent       Dim = "consent"       // what it agreed to take, from whom
)

// Source is where a fact comes from, in decreasing order of trust.
type Source string

const (
	Declared Source = "declared" // the worker said so, or config ("on leave until Fri")
	Observed Source = "observed" // measured by the scaffold (queue, failures, read receipts)
	Inferred Source = "inferred" // the brain's reading of a conversation — soft, short-lived, private
)

// Fact is one piece of state with its provenance.
type Fact struct {
	Dim        Dim       `json:"dim"`
	Value      string    `json:"value"`
	Num        float64   `json:"num,omitempty"`
	Detail     string    `json:"detail,omitempty"` // evidence, in a few words
	Source     Source    `json:"source"`
	Confidence float64   `json:"confidence,omitempty"` // 0..1; 0 = unspecified (treated as 1 for declared/observed)
	At         time.Time `json:"at"`
	Expires    time.Time `json:"expires,omitempty"` // zero = until replaced
}

// MaxInferredTTL caps how long an inferred fact about a worker lives. Guesses
// about a person never harden into a long-term judgement.
const MaxInferredTTL = 24 * time.Hour

// Normalize applies the provenance rules: inferred facts expire within
// MaxInferredTTL and never claim full confidence.
func (f Fact) Normalize() Fact {
	if f.Source == Inferred {
		if f.Expires.IsZero() || f.Expires.Sub(f.At) > MaxInferredTTL {
			f.Expires = f.At.Add(MaxInferredTTL)
		}
		if f.Confidence <= 0 || f.Confidence > 0.8 {
			f.Confidence = 0.6
		}
	}
	return f
}

func (f Fact) live(now time.Time) bool { return f.Expires.IsZero() || now.Before(f.Expires) }

func rank(s Source) int {
	switch s {
	case Declared:
		return 3
	case Observed:
		return 2
	}
	return 1
}

// State is a worker's current facts.
type State struct {
	Facts []Fact `json:"facts"`
}

// Put adds a fact, replacing an older one with the same dimension and source.
func (s *State) Put(f Fact) {
	f = f.Normalize()
	for i, o := range s.Facts {
		if o.Dim == f.Dim && o.Source == f.Source {
			s.Facts[i] = f
			return
		}
	}
	s.Facts = append(s.Facts, f)
}

// Prune drops expired facts.
func (s *State) Prune(now time.Time) {
	out := s.Facts[:0]
	for _, f := range s.Facts {
		if f.live(now) {
			out = append(out, f)
		}
	}
	s.Facts = out
}

// Get returns the fact that wins for a dimension: live, then the most
// trusted source, then the newest.
func (s State) Get(d Dim, now time.Time) (Fact, bool) {
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

// Summary is the one-line state the brain sees when routing, e.g.
// "presence busy (queue 2) · health ok · receptiveness low? (declined t12)".
// Inferred values carry a "?" so the brain treats them as guesses.
func (s State) Summary(now time.Time) string {
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
		p := string(d) + " " + v
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

// Unreachable reports whether, by the hard facts, work can't be offered now.
func (s State) Unreachable(now time.Time) (bool, string) {
	f, ok := s.Get(Presence, now)
	if ok && f.Source != Inferred {
		switch f.Value {
		case "offline", "unavailable":
			return true, fmt.Sprintf("%s: %s", f.Value, f.Detail)
		}
	}
	return false, ""
}

// SortFacts orders facts for display.
func SortFacts(fs []Fact) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Dim != fs[j].Dim {
			return fs[i].Dim < fs[j].Dim
		}
		return rank(fs[i].Source) > rank(fs[j].Source)
	})
}
