// Package attention computes what needs the bot's attention now. Signals are
// derived from a world snapshot by pluggable Detectors, scored by a Scorer,
// sorted into tiers (Now / Today / Watch); only Resolutions — what was done
// about a signal — are stored. A signal has a stable key, so a handled
// signal doesn't nag again, and comes back when its resolution expires or the
// situation gets worse.
package attention

import (
	"sort"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/insight"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/world"
)

type Level int

const (
	Low Level = iota
	Medium
	High
)

func (l Level) String() string { return [...]string{"low", "medium", "high"}[l] }

// Signal is one thing that needs attention.
type Signal struct {
	Key     string         `json:"key"`  // stable: "will_miss:I7"
	Kind    string         `json:"kind"` // detector name
	Level   Level          `json:"level"`
	Subject string         `json:"subject"` // "item:I7" | "assignment:A12" | "worker:zhangsan" | "project:P1"
	Project ProjectID      `json:"project,omitempty"`
	Summary string         `json:"summary"`
	Facts   map[string]any `json:"facts,omitempty"` // the numbers behind it
	Suggest []string       `json:"suggest,omitempty"`
	People  []WorkerID     `json:"people,omitempty"`
	Since   time.Time      `json:"since,omitempty"` // how long it has been true (for waiting bonuses)
}

// Detector is one rule. Adding a rule = registering a Detector.
type Detector interface {
	Name() string
	Detect(c *Context) []Signal
}

// Context is what detectors read: the snapshot plus computed forecasts.
type Context struct {
	*world.World
	Pace     insight.Pace
	Forecast map[ItemID]time.Time
}

func NewContext(w *world.World) *Context {
	pace := insight.PaceFrom(w)
	return &Context{World: w, Pace: pace, Forecast: insight.Forecast(w, pace)}
}

// Scorer turns a signal into an attention score.
type Scorer interface {
	Score(s Signal, c *Context) float64
}

type Scored struct {
	Signal
	Score float64 `json:"score"`
}

// Agenda is the result: what to handle now, today, and what to just know.
type Agenda struct {
	Now        []Scored `json:"now"`
	Today      []Scored `json:"today"`
	Watch      []Scored `json:"watch"`
	Suppressed int      `json:"suppressed"` // handled earlier, not due to come back yet
}

// Tiering sorts scored signals into tiers.
type Tiering interface {
	Tier(scored []Scored) Agenda
}

// Outcome of handling a signal.
type Outcome string

const (
	Done          Outcome = "done"            // resolved; comes back only if it gets worse
	Acted         Outcome = "acted"           // did something, waiting on others; comes back at Until
	Deferred      Outcome = "deferred"        // later; comes back at Until
	HandedToOwner Outcome = "handed_to_owner" // the owner decides; comes back at Until
	Dismissed     Outcome = "dismissed"       // not an issue; comes back only if it gets worse
)

// DefaultRecheck is when an acted / deferred / handed signal comes back
// without an explicit time.
const DefaultRecheck = 24 * time.Hour

// Resolution is an aggregate root: what was done about a signal.
type Resolution struct {
	Key     string     `json:"key"`
	Outcome Outcome    `json:"outcome"`
	Level   Level      `json:"level"` // the level when handled
	Until   *time.Time `json:"until,omitempty"`
	Note    string     `json:"note,omitempty"`
	By      Actor      `json:"by"`
	At      time.Time  `json:"at"`
}

// Resolve records how a signal was handled.
func Resolve(s Signal, o Outcome, until *time.Time, note string, now time.Time, by Actor) (Resolution, []Event, error) {
	switch o {
	case Done, Dismissed:
	case Acted, Deferred, HandedToOwner:
		if until == nil {
			t := now.Add(DefaultRecheck)
			until = &t
		} else if !until.After(now) {
			return Resolution{}, nil, Invalid("the recheck time must be in the future")
		}
	default:
		return Resolution{}, nil, Invalid("unknown outcome %q", o)
	}
	r := Resolution{Key: s.Key, Outcome: o, Level: s.Level, Until: until, Note: note, By: by, At: now}
	return r, []Event{NewEvent("attention.resolved", "signal:"+s.Key, now, by, "outcome", string(o), "note", note)}, nil
}

// Suppresses reports whether this resolution keeps the signal off the agenda.
func (r Resolution) Suppresses(s Signal, now time.Time) bool {
	if s.Level > r.Level {
		return false // it got worse
	}
	if r.Until != nil {
		return now.Before(*r.Until)
	}
	return r.Outcome == Done || r.Outcome == Dismissed
}

// Service combines detectors, scoring and tiering.
type Service struct {
	Detectors []Detector
	Scorer    Scorer
	Tiering   Tiering
}

// Default is the built-in rule set.
func Default() Service {
	return Service{Detectors: DefaultDetectors(), Scorer: DefaultScorer{}, Tiering: DefaultTiering{}}
}

// Signals runs all detectors (no resolutions applied).
func (s Service) Signals(c *Context) []Signal {
	var out []Signal
	for _, d := range s.Detectors {
		out = append(out, d.Detect(c)...)
	}
	return out
}

// Build computes the agenda, skipping handled signals.
func (s Service) Build(c *Context, resolutions map[string]Resolution) Agenda {
	var scored []Scored
	suppressed := 0
	for _, sig := range s.Signals(c) {
		if r, ok := resolutions[sig.Key]; ok && r.Suppresses(sig, c.Now) {
			suppressed++
			continue
		}
		scored = append(scored, Scored{Signal: sig, Score: s.Scorer.Score(sig, c)})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].Key < scored[j].Key
	})
	ag := s.Tiering.Tier(scored)
	ag.Suppressed = suppressed
	return ag
}

// RiskCount per project, for health.
func RiskCount(sigs []Signal, p ProjectID) insight.RiskCount {
	var rc insight.RiskCount
	for _, s := range sigs {
		if s.Project != p {
			continue
		}
		switch s.Level {
		case High:
			rc.High++
		case Medium:
			rc.Medium++
		}
	}
	return rc
}

// DefaultScorer:
//
//	score = priority weight (P0 8 … P3 1)
//	      × urgency (high 2.5, medium 1.5, low 1; overdue 3)
//	      × (1 + 0.5 × open items downstream of it, transitively)
//	      × 0.8 for knock-on signals (late_dep), so root causes come first
//	      + waiting bonus (someone waits on us: +0.5 per hour, at most +6)
type DefaultScorer struct{}

func (DefaultScorer) Score(s Signal, c *Context) float64 {
	weight := P2.Weight()
	blocked := 0
	if id, ok := itemOf(s.Subject); ok {
		if it, ok := c.Items[id]; ok {
			weight = c.Priority(it).Weight()
			blocked = c.Graph().Downstream(id)
		}
	} else if p, ok := c.Projects[s.Project]; ok {
		weight = p.Priority.Weight()
	}
	urgency := map[Level]float64{Low: 1, Medium: 1.5, High: 2.5}[s.Level]
	if s.Kind == "overdue" {
		urgency = 3
	}
	score := weight * urgency * (1 + 0.5*float64(blocked))
	if s.Kind == "late_dep" {
		score *= 0.8
	}
	if s.Kind == "waiting_on_us" && !s.Since.IsZero() {
		score += min(c.Now.Sub(s.Since).Hours()*0.5, 6)
	}
	return score
}

func itemOf(subject string) (ItemID, bool) {
	if len(subject) > 5 && subject[:5] == "item:" {
		return ItemID(subject[5:]), true
	}
	return "", false
}

// DefaultTiering: Now = high-level signals and anything scoring ≥ 10, at
// most MaxNow per wake; Today = score ≥ 3; the rest is Watch.
type DefaultTiering struct{ MaxNow int }

func (t DefaultTiering) Tier(scored []Scored) Agenda {
	maxNow := t.MaxNow
	if maxNow <= 0 {
		maxNow = 3
	}
	var ag Agenda
	for _, s := range scored {
		switch {
		case (s.Level == High || s.Score >= 10) && len(ag.Now) < maxNow:
			ag.Now = append(ag.Now, s)
		case s.Score >= 3 || s.Level == High:
			ag.Today = append(ag.Today, s)
		default:
			ag.Watch = append(ag.Watch, s)
		}
	}
	return ag
}
