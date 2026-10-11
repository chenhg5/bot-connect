package portfolio

import (
	"strings"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// Stage of a project's plan. A plan is the bot's proposal until the people
// in it have confirmed their part and the principal has agreed:
//
//	draft ─propose→ proposed ─(principal agrees + every person confirms)→ active ─→ done
//	  ↑                │ someone declines or proposes something else
//	  └────revise──────┘ (any stage: a revision needs agreeing again)
//
// Exploratory work (research, drafts) may run in any stage; committing work
// (changes, actions, people's work) waits for agreement.
type Stage string

const (
	NoPlan       Stage = ""
	PlanDraft    Stage = "draft"
	PlanProposed Stage = "proposed"
	PlanActive   Stage = "active"
	PlanDone     Stage = "done"
)

// Committed: the plan's dates and owners are promises, so risks are real.
func (s Stage) Committed() bool { return s == NoPlan || s == PlanActive || s == PlanDone }

// GoalKind: a goal is an outcome with an open path and must be judged
// feasible before anyone commits to it; a task has a known path.
type GoalKind string

const (
	KindTask    GoalKind = "task"
	KindGoal    GoalKind = "goal"
	KindExplore GoalKind = "explore" // the goal itself becomes clear by doing: products, creative work, research & development
)

func ParseGoalKind(s string) (GoalKind, error) {
	switch k := GoalKind(s); k {
	case "", KindTask, KindGoal, KindExplore:
		return k, nil
	}
	return "", Invalid("unknown kind %q (task, goal, explore)", s)
}

// Verdict on feasibility.
type Verdict string

const (
	Unknown     Verdict = "unknown"
	Feasible    Verdict = "feasible"
	Conditional Verdict = "conditional" // feasible if the assumptions hold
	Infeasible  Verdict = "infeasible"
)

func ParseVerdict(s string) (Verdict, error) {
	switch v := Verdict(s); v {
	case "", Unknown, Feasible, Conditional, Infeasible:
		if v == "" {
			v = Unknown
		}
		return v, nil
	}
	return "", Invalid("unknown verdict %q (unknown, feasible, conditional, infeasible)", s)
}

type Feasibility struct {
	Verdict  Verdict  `json:"verdict"`
	Note     string   `json:"note,omitempty"`
	Evidence []ItemID `json:"evidence,omitempty"` // findings it rests on
}

// Plan is the project's way to its objective: the nodes are the project's
// items; this holds what is about the whole.
type Plan struct {
	Stage       Stage       `json:"stage,omitempty"`
	Revision    int         `json:"revision,omitempty"`
	Kind        GoalKind    `json:"kind,omitempty"`
	Feasibility Feasibility `json:"feasibility"`
	Assumptions []string    `json:"assumptions,omitempty"`
	StopIf      []string    `json:"stop_if,omitempty"` // when to stop or rethink
	PrincipalOK bool        `json:"principal_ok,omitempty"`
	Notes       []string    `json:"notes,omitempty"` // review notes on this revision
	By          Actor       `json:"by"`              // who drafted it; work it hands out is asked in their name
	// Exploration: the plan is a loop (try → show → learn → decide), not a
	// path. What is open, what was learned, and when the principal last saw it.
	Questions  []string   `json:"questions,omitempty"`
	Learned    []Learning `json:"learned,omitempty"`
	Round      int        `json:"round,omitempty"`
	LastReview time.Time  `json:"last_review,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at,omitempty"`
}

// Learning is one thing a round of exploration settled, kept so the next
// round — or a restart — builds on it instead of going in circles.
type Learning struct {
	At     time.Time `json:"at"`
	Round  int       `json:"round"`
	Text   string    `json:"text"`             // what we now know, and why
	Choice string    `json:"choice,omitempty"` // continue | pivot | stop | converge
}

// PlanSpec is what drafting changes about the whole.
type PlanSpec struct {
	Kind        GoalKind
	Feasibility *Feasibility
	Assumptions []string
	StopIf      []string
	Questions   []string
}

// Draft records a new revision of the plan. A plan that was agreed must be
// agreed again.
func (p *Project) Draft(sp PlanSpec, now time.Time, by Actor) []Event {
	pl := &p.Plan
	if pl.Stage == NoPlan || pl.Stage != PlanDraft {
		pl.Revision++
	}
	pl.Stage, pl.PrincipalOK, pl.Notes, pl.UpdatedAt, pl.By = PlanDraft, false, nil, now, by
	if sp.Kind != "" {
		pl.Kind = sp.Kind
	}
	if pl.Kind == "" {
		pl.Kind = KindTask
	}
	if sp.Feasibility != nil {
		pl.Feasibility = *sp.Feasibility
	}
	if pl.Feasibility.Verdict == "" {
		pl.Feasibility.Verdict = Unknown
	}
	if sp.Assumptions != nil {
		pl.Assumptions = clean(sp.Assumptions)
	}
	if sp.StopIf != nil {
		pl.StopIf = clean(sp.StopIf)
	}
	if sp.Questions != nil {
		pl.Questions = clean(sp.Questions)
	}
	if pl.Kind == KindExplore {
		// Exploring is revising: nobody signs off a path that doesn't exist
		// yet. The principal is in the loop through reviews instead.
		pl.Stage, pl.PrincipalOK = PlanActive, true
		if pl.Round == 0 {
			pl.Round = 1
		}
	}
	return []Event{p.ev("plan.drafted", now, by, "revision", pl.Revision)}
}

// Learn records what a round settled, with the principal's direction:
// continue (next round), pivot (new direction, same goal), stop, or
// converge (the open questions are answered: turn it into a task plan).
func (p *Project) Learn(text, choice string, resolved []string, now time.Time, by Actor) ([]Event, error) {
	pl := &p.Plan
	if pl.Kind != KindExplore {
		return nil, Conflict("only exploration plans learn in rounds; %s is a %s plan", p.ID, pl.Kind)
	}
	if strings.TrimSpace(text) == "" {
		return nil, Invalid("what was learned?")
	}
	switch choice {
	case "", "continue", "pivot", "stop", "converge":
	default:
		return nil, Invalid("choice: continue | pivot | stop | converge")
	}
	pl.Learned = append(pl.Learned, Learning{At: now, Round: pl.Round, Text: strings.TrimSpace(text), Choice: choice})
	if len(resolved) > 0 {
		var open []string
		for _, q := range pl.Questions {
			done := false
			for _, r := range resolved {
				done = done || strings.Contains(q, strings.TrimSpace(r)) || strings.Contains(strings.TrimSpace(r), q)
			}
			if !done {
				open = append(open, q)
			}
		}
		pl.Questions = open
	}
	pl.LastReview, pl.UpdatedAt = now, now
	switch choice {
	case "continue", "pivot", "":
		pl.Round++
	case "stop":
		pl.Stage = PlanDone
	case "converge":
		pl.Kind, pl.Stage, pl.PrincipalOK = KindTask, PlanDraft, false
	}
	return []Event{p.ev("plan.learned", now, by, "round", pl.Round, "choice", choice)}, nil
}

// Propose puts the draft to the people in it.
func (p *Project) Propose(notes []string, now time.Time, by Actor) ([]Event, error) {
	if p.Plan.Stage != PlanDraft {
		return nil, Conflict("plan of %s is %q; only a draft can be proposed", p.ID, p.Plan.Stage)
	}
	if p.Plan.Kind == KindGoal && p.Plan.Feasibility.Verdict == Infeasible {
		return nil, Conflict("the plan says the goal is infeasible: tell the principal and propose another goal instead")
	}
	p.Plan.Stage, p.Plan.Notes, p.Plan.UpdatedAt = PlanProposed, notes, now
	return []Event{p.ev("plan.proposed", now, by, "revision", p.Plan.Revision)}, nil
}

// PrincipalAgrees records the principal's agreement to this revision.
func (p *Project) PrincipalAgrees(now time.Time, by Actor) ([]Event, error) {
	if p.Plan.Stage != PlanProposed {
		return nil, Conflict("plan of %s is %q; propose it first", p.ID, p.Plan.Stage)
	}
	p.Plan.PrincipalOK, p.Plan.UpdatedAt = true, now
	return []Event{p.ev("plan.principal_agreed", now, by, "revision", p.Plan.Revision)}, nil
}

// Agree: everyone in it has confirmed. The plan becomes active at once.
func (p *Project) Agree(now time.Time, by Actor) ([]Event, error) {
	if p.Plan.Stage != PlanProposed || !p.Plan.PrincipalOK {
		return nil, Conflict("plan of %s is not ready to be agreed", p.ID)
	}
	p.Plan.Stage, p.Plan.UpdatedAt = PlanActive, now
	return []Event{p.ev("plan.agreed", now, by, "revision", p.Plan.Revision)}, nil
}

// Finish: every node is done.
func (p *Project) Finish(now time.Time, by Actor) []Event {
	p.Plan.Stage, p.Plan.UpdatedAt = PlanDone, now
	return []Event{p.ev("plan.done", now, by, "revision", p.Plan.Revision)}
}

func clean(v []string) []string {
	var out []string
	for _, x := range v {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}
