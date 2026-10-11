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
	KindTask GoalKind = "task"
	KindGoal GoalKind = "goal"
)

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
	UpdatedAt   time.Time   `json:"updated_at,omitempty"`
}

// PlanSpec is what drafting changes about the whole.
type PlanSpec struct {
	Kind        GoalKind
	Feasibility *Feasibility
	Assumptions []string
	StopIf      []string
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
	return []Event{p.ev("plan.drafted", now, by, "revision", pl.Revision)}
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
