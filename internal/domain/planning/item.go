// Package planning is the Planning context: items (what must be done, by
// when, to what standard, needing what) and their dependency graph. Who does
// an item and how it is going belongs to delegation; an item only points at
// its current assignment.
package planning

import (
	"strings"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

type Status string

const (
	Todo    Status = "todo"
	Active  Status = "active"
	Blocked Status = "blocked"
	Done    Status = "done"
	Dropped Status = "dropped"
)

type Kind string

const (
	Task      Kind = "task"
	Milestone Kind = "milestone"
)

// Item is an aggregate root: there are many, each changing on its own.
type Item struct {
	ID         ItemID        `json:"id"`
	Project    ProjectID     `json:"project"`
	Parent     ItemID        `json:"parent,omitempty"`
	Kind       Kind          `json:"kind"`
	Title      string        `json:"title"`
	Acceptance []Criterion   `json:"acceptance,omitempty"`
	Due        *time.Time    `json:"due,omitempty"`
	Estimate   time.Duration `json:"estimate,omitempty"`
	Priority   *Priority     `json:"priority,omitempty"` // nil: the project's
	DependsOn  []ItemID      `json:"depends_on,omitempty"`
	Needs      []Need        `json:"needs,omitempty"`
	Status     Status        `json:"status"`
	Owner      WorkerID      `json:"owner,omitempty"`      // who has it now (from the current assignment)
	Assignment AssignmentID  `json:"assignment,omitempty"` // current open assignment
	Blocker    *Blocker      `json:"blocker,omitempty"`
	StartedAt  time.Time     `json:"started_at,omitempty"`
	LastSignal time.Time     `json:"last_signal,omitempty"` // last sign of progress
	Requester  Actor         `json:"requester"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
	Version    int           `json:"version"`
}

// Criterion is one acceptance condition, checked one by one on review.
type Criterion struct {
	Text string `json:"text"`
}

// Need is what doing the item requires: a capability ("repo:tapnow:write"),
// an approval ("deploy:prod"), a decision, a physical action…
type Need struct {
	Kind   NeedKind `json:"kind"`
	Detail string   `json:"detail,omitempty"`
}

type Blocker struct {
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
}

// Spec is what a new item is made from.
type Spec struct {
	Project    ProjectID
	Parent     ItemID
	Kind       Kind
	Title      string
	Acceptance []string
	Due        *time.Time
	Estimate   time.Duration
	Priority   *Priority
	DependsOn  []ItemID
	Needs      []Need
}

func (i *Item) ev(typ string, at time.Time, by Actor, kv ...any) Event {
	return NewEvent(typ, "item:"+string(i.ID), at, by, kv...)
}

// New creates an item. Dependency validity across items is checked by Graph.
func New(id ItemID, s Spec, now time.Time, by Actor) (Item, []Event, error) {
	if id == "" || s.Project == "" || strings.TrimSpace(s.Title) == "" {
		return Item{}, nil, Invalid("an item needs an id, a project and a title")
	}
	if s.Kind == "" {
		s.Kind = Task
	}
	if s.Estimate < 0 {
		return Item{}, nil, Invalid("estimate can't be negative")
	}
	it := Item{ID: id, Project: s.Project, Parent: s.Parent, Kind: s.Kind, Title: s.Title, Due: s.Due, Estimate: s.Estimate,
		Priority: s.Priority, DependsOn: s.DependsOn, Needs: s.Needs, Status: Todo, Requester: by, CreatedAt: now, UpdatedAt: now}
	for _, a := range s.Acceptance {
		if a = strings.TrimSpace(a); a != "" {
			it.Acceptance = append(it.Acceptance, Criterion{Text: a})
		}
	}
	for _, n := range s.Needs {
		if _, err := ParseNeedKind(string(n.Kind)); err != nil {
			return Item{}, nil, Invalid("%v", err)
		}
	}
	return it, []Event{it.ev("item.created", now, by, "project", string(s.Project), "title", s.Title)}, nil
}

func (i Item) Open() bool { return i.Status != Done && i.Status != Dropped }

func (i *Item) touch(now time.Time) { i.UpdatedAt = now }

// Reschedule changes the due date.
func (i *Item) Reschedule(due *time.Time, now time.Time, by Actor) ([]Event, error) {
	if !i.Open() {
		return nil, Conflict("item %s is %s", i.ID, i.Status)
	}
	i.Due = due
	i.touch(now)
	return []Event{i.ev("item.rescheduled", now, by, "due", due)}, nil
}

// Attach records the assignment now responsible for the item.
func (i *Item) Attach(a AssignmentID, w WorkerID, now time.Time, by Actor) ([]Event, error) {
	if !i.Open() {
		return nil, Conflict("item %s is %s", i.ID, i.Status)
	}
	if i.Assignment != "" {
		return nil, Conflict("item %s is already with %s (%s)", i.ID, i.Owner, i.Assignment)
	}
	i.Assignment, i.Owner = a, w
	i.touch(now)
	return []Event{i.ev("item.attached", now, by, "assignment", string(a), "worker", string(w))}, nil
}

// Detach: the assignment ended without completing the item; it goes back to todo.
func (i *Item) Detach(reason string, now time.Time, by Actor) []Event {
	i.Assignment, i.Owner, i.Blocker = "", "", nil
	if i.Open() {
		i.Status = Todo
	}
	i.touch(now)
	return []Event{i.ev("item.detached", now, by, "reason", reason)}
}

// Started / Progressed: signs of life from whoever has it.
func (i *Item) Progressed(at time.Time) {
	if i.Status == Todo || i.Status == Blocked {
		i.Status = Active
	}
	if i.StartedAt.IsZero() {
		i.StartedAt = at
	}
	i.Blocker, i.LastSignal = nil, at
	i.touch(at)
}

func (i *Item) Block(reason string, now time.Time, by Actor) []Event {
	i.Status, i.Blocker = Blocked, &Blocker{Reason: reason, Since: now}
	i.LastSignal = now
	i.touch(now)
	return []Event{i.ev("item.blocked", now, by, "reason", reason)}
}

// Complete closes the item. Only a verified assignment or a privileged
// actor (the owner deciding it's done) may complete it.
func (i *Item) Complete(verified bool, now time.Time, by Actor) ([]Event, error) {
	if !i.Open() {
		return nil, Conflict("item %s is already %s", i.ID, i.Status)
	}
	if !verified && !by.Privileged() {
		return nil, Forbidden("item %s can only be completed by a verified delivery or the owner", i.ID)
	}
	i.Status, i.Blocker = Done, nil
	i.touch(now)
	return []Event{i.ev("item.completed", now, by)}, nil
}

func (i *Item) Drop(reason string, now time.Time, by Actor) ([]Event, error) {
	if !i.Open() {
		return nil, Conflict("item %s is already %s", i.ID, i.Status)
	}
	i.Status = Dropped
	i.touch(now)
	return []Event{i.ev("item.dropped", now, by, "reason", reason)}, nil
}

// EffectivePriority: the item's own, else the project's.
func (i Item) EffectivePriority(project Priority) Priority {
	if i.Priority != nil {
		return *i.Priority
	}
	return project
}

// Requires reports whether the item has a need of a kind.
func (i Item) Requires(k NeedKind) (Need, bool) {
	for _, n := range i.Needs {
		if n.Kind == k {
			return n, true
		}
	}
	return Need{}, false
}
