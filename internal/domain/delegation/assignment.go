// Package delegation is the Delegation context: an Assignment is one
// agreement handing (part of) an item to one worker — work, an approval, a
// decision, a clarification, a real-world action, a review — and its life:
// offered → accepted → working ⇄ blocked → delivered → verified, or
// declined / countered / failed / cancelled / released / expired.
//
// Agents we control accept at once; people and other bots receive a
// request they may decline or counter. Approvals and decisions are answered
// with a choice and need no separate verification.
package delegation

import (
	"slices"
	"strings"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// AskKind: what is being asked of the worker.
type AskKind string

const (
	Work          AskKind = "work"
	Approval      AskKind = "approval"
	Decision      AskKind = "decision"
	Clarification AskKind = "clarification"
	Action        AskKind = "action" // a real-world action
	Review        AskKind = "review" // check / confirm something
)

// Answered kinds are settled by a single answer (a choice), not by delivering work.
func (k AskKind) Answered() bool {
	return k == Approval || k == Decision || k == Clarification || k == Review
}

type Status string

const (
	Offered   Status = "offered"
	Countered Status = "countered"
	Accepted  Status = "accepted"
	Working   Status = "working"
	Blocked   Status = "blocked"
	Paused    Status = "paused"
	Delivered Status = "delivered"
	Revising  Status = "revising"
	Verified  Status = "verified"
	Declined  Status = "declined"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
	Released  Status = "released"
	Expired   Status = "expired"
)

func (s Status) Terminal() bool {
	switch s {
	case Verified, Declined, Failed, Cancelled, Released, Expired:
		return true
	}
	return false
}

// A2A maps onto Agent2Agent task states.
func (s Status) A2A() string {
	switch s {
	case Offered, Countered:
		return "submitted"
	case Accepted, Working, Revising, Paused:
		return "working"
	case Blocked:
		return "input-required"
	case Delivered, Verified:
		return "completed"
	case Declined:
		return "rejected"
	case Cancelled, Expired:
		return "canceled"
	}
	return "failed"
}

// Brief is everything the worker gets: self-contained and minimal (an
// external worker sees nothing else).
type Brief struct {
	Goal     string        `json:"goal"`
	Context  string        `json:"context,omitempty"`
	Done     []string      `json:"done,omitempty"` // acceptance criteria
	Evidence string        `json:"evidence,omitempty"`
	Due      *time.Time    `json:"due,omitempty"`
	Estimate time.Duration `json:"estimate,omitempty"`
	Priority Priority      `json:"priority"`
}

// Step is one entry in the history.
type Step struct {
	Type     string     `json:"type"`
	At       time.Time  `json:"at"`
	By       Actor      `json:"by"`
	Note     string     `json:"note,omitempty"`
	ETA      *time.Time `json:"eta,omitempty"`
	Due      *time.Time `json:"due,omitempty"`
	Result   string     `json:"result,omitempty"`
	Evidence []string   `json:"evidence,omitempty"`
	Choice   string     `json:"choice,omitempty"`
}

// Assignment is an aggregate root.
type Assignment struct {
	ID          AssignmentID `json:"id"`
	Item        ItemID       `json:"item"`
	Project     ProjectID    `json:"project"`
	Worker      WorkerID     `json:"worker"`
	Kind        AskKind      `json:"kind"`
	Why         NeedKind     `json:"why,omitempty"` // required when the worker is a person
	Options     []string     `json:"options,omitempty"`
	Requester   Actor        `json:"requester"`
	Conv        string       `json:"conv,omitempty"` // where to report back
	Brief       Brief        `json:"brief"`
	Status      Status       `json:"status"`
	Ref         string       `json:"ref,omitempty"` // worker-side id
	Question    string       `json:"question,omitempty"`
	ProposedDue *time.Time   `json:"proposed_due,omitempty"` // from a counter
	ETA         *time.Time   `json:"eta,omitempty"`
	Result      string       `json:"result,omitempty"`
	Evidence    []string     `json:"evidence,omitempty"`
	Choice      string       `json:"choice,omitempty"`
	History     []Step       `json:"history"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	LastLife    time.Time    `json:"last_life,omitempty"` // last sign of life from the worker
	Version     int          `json:"version"`
}

// Offer is what a new assignment is made from.
type Offer struct {
	ID       AssignmentID
	Item     ItemID
	Project  ProjectID
	Worker   WorkerID
	ToPerson bool // the worker is a person: Why is required
	Kind     AskKind
	Why      NeedKind
	Options  []string
	Brief    Brief
	Conv     string
}

// New creates an offered assignment.
func New(o Offer, now time.Time, by Actor) (Assignment, []Event, error) {
	if o.ID == "" || o.Worker == "" || o.Item == "" {
		return Assignment{}, nil, Invalid("an assignment needs an id, an item and a worker")
	}
	if o.Kind == "" {
		o.Kind = Work
	}
	if strings.TrimSpace(o.Brief.Goal) == "" {
		return Assignment{}, nil, Invalid("the brief needs a goal")
	}
	if o.ToPerson {
		if o.Why == "" {
			return Assignment{}, nil, Invalid("asking a person needs a reason (why): approval, decision, physical, relationship, judgment, knowledge or capability")
		}
		if _, err := ParseNeedKind(string(o.Why)); err != nil {
			return Assignment{}, nil, Invalid("%v", err)
		}
	}
	if (o.Kind == Decision || o.Kind == Approval) && len(o.Options) == 0 {
		if o.Kind == Approval {
			o.Options = []string{"approve", "reject"}
		} else {
			return Assignment{}, nil, Invalid("a decision needs options")
		}
	}
	a := Assignment{ID: o.ID, Item: o.Item, Project: o.Project, Worker: o.Worker, Kind: o.Kind, Why: o.Why, Options: o.Options,
		Requester: by, Conv: o.Conv, Brief: o.Brief, Status: Offered, CreatedAt: now, UpdatedAt: now}
	a.History = []Step{{Type: "offer", At: now, By: by, Note: o.Brief.Goal}}
	return a, []Event{a.ev("assignment.offered", now, by, "worker", string(o.Worker), "kind", string(o.Kind), "why", string(o.Why))}, nil
}

func (a *Assignment) ev(typ string, at time.Time, by Actor, kv ...any) Event {
	kv = append(kv, "item", string(a.Item), "worker", string(a.Worker), "status", string(a.Status))
	return NewEvent(typ, "assignment:"+string(a.ID), at, by, kv...)
}

// party checks
func (a Assignment) isWorker(by Actor) bool {
	return by.Worker == a.Worker || (by.Role == RoleSystem && strings.HasPrefix(by.Via, "driver"))
}

func (a Assignment) isRequester(by Actor) bool {
	return by.Privileged() || (by.UserID != "" && by.UserID == a.Requester.UserID) || by.Role == RoleSystem
}

type rule struct {
	from   []Status
	to     Status
	worker bool // who may: true = the worker's side, false = the requester's side
}

var rules = map[string]rule{
	"accept":  {[]Status{Offered}, Accepted, true},
	"decline": {[]Status{Offered, Countered}, Declined, true},
	"counter": {[]Status{Offered}, Countered, true},
	"start":   {[]Status{Accepted, Paused, Blocked, Revising}, Working, true},
	"ask":     {[]Status{Accepted, Working, Revising}, Blocked, true},
	"pause":   {[]Status{Accepted, Working, Blocked, Revising}, Paused, true},
	"deliver": {[]Status{Accepted, Working, Revising, Blocked}, Delivered, true},
	"answerq": {[]Status{Offered, Accepted, Working}, Verified, true}, // an answered kind settled by a choice
	"fail":    {[]Status{Accepted, Working, Blocked, Paused, Revising}, Failed, true},
	"release": {[]Status{Accepted, Working, Blocked, Paused, Revising}, Released, true},

	"accept_counter": {[]Status{Countered}, Working, false},
	"answer":         {[]Status{Blocked}, Working, false},
	"verify":         {[]Status{Delivered}, Verified, false},
	"revise":         {[]Status{Delivered}, Revising, false},
	"cancel":         {[]Status{Offered, Countered, Accepted, Working, Blocked, Paused, Delivered, Revising}, Cancelled, false},
	"expire":         {[]Status{Offered, Countered}, Expired, false},
}

func (a *Assignment) apply(name string, s Step, by Actor) ([]Event, error) {
	r := rules[name]
	if r.worker && !a.isWorker(by) {
		return nil, Forbidden("only %s can %s assignment %s", a.Worker, name, a.ID)
	}
	if !r.worker && !a.isRequester(by) {
		return nil, Forbidden("only the requester or the owner can %s assignment %s", name, a.ID)
	}
	if !slices.Contains(r.from, a.Status) {
		return nil, Conflict("can't %s assignment %s while it is %s", name, a.ID, a.Status)
	}
	a.Status = r.to
	s.Type, s.By = name, by
	a.History = append(a.History, s)
	a.UpdatedAt = s.At
	if r.worker {
		a.LastLife = s.At
	}
	return []Event{a.ev("assignment."+eventName(name), s.At, by, "note", s.Note)}, nil
}

var eventNames = map[string]string{
	"accept": "accepted", "decline": "declined", "counter": "countered", "start": "started", "ask": "asked",
	"pause": "paused", "deliver": "delivered", "answerq": "answered", "fail": "failed", "release": "released",
	"accept_counter": "counter_accepted", "answer": "question_answered", "verify": "verified", "revise": "revised",
	"cancel": "cancelled", "expire": "expired",
}

func eventName(n string) string { return eventNames[n] }

// ---- worker side ----

func (a *Assignment) Accept(eta *time.Time, note string, at time.Time, by Actor) ([]Event, error) {
	evs, err := a.apply("accept", Step{At: at, ETA: eta, Note: note}, by)
	if err == nil && eta != nil {
		a.ETA = eta
	}
	return evs, err
}

func (a *Assignment) Decline(reason string, at time.Time, by Actor) ([]Event, error) {
	return a.apply("decline", Step{At: at, Note: reason}, by)
}

// Counter proposes another deadline; the requester decides.
func (a *Assignment) Counter(due time.Time, note string, at time.Time, by Actor) ([]Event, error) {
	evs, err := a.apply("counter", Step{At: at, Due: &due, Note: note}, by)
	if err == nil {
		a.ProposedDue = &due
	}
	return evs, err
}

func (a *Assignment) Start(at time.Time, by Actor) ([]Event, error) {
	return a.apply("start", Step{At: at}, by)
}

// Progress is a sign of life (no status change except accepted → working).
func (a *Assignment) Progress(note string, eta *time.Time, at time.Time, by Actor) ([]Event, error) {
	if !a.isWorker(by) {
		return nil, Forbidden("only %s can report progress on %s", a.Worker, a.ID)
	}
	if a.Status.Terminal() || a.Status == Offered || a.Status == Countered {
		return nil, Conflict("assignment %s is %s", a.ID, a.Status)
	}
	if a.Status == Accepted {
		a.Status = Working
	}
	if eta != nil {
		a.ETA = eta
	}
	a.History = append(a.History, Step{Type: "progress", At: at, By: by, Note: note, ETA: eta})
	a.UpdatedAt, a.LastLife = at, at
	return []Event{a.ev("assignment.progressed", at, by, "note", note)}, nil
}

func (a *Assignment) Ask(question string, at time.Time, by Actor) ([]Event, error) {
	evs, err := a.apply("ask", Step{At: at, Note: question}, by)
	if err == nil {
		a.Question = question
	}
	return evs, err
}

func (a *Assignment) Pause(note string, at time.Time, by Actor) ([]Event, error) {
	return a.apply("pause", Step{At: at, Note: note}, by)
}

// Deliver hands in work. Not for answered kinds (use Answer).
func (a *Assignment) Deliver(result string, evidence []string, at time.Time, by Actor) ([]Event, error) {
	if a.Kind.Answered() {
		return nil, Invalid("a %s is answered with a choice, not delivered", a.Kind)
	}
	evs, err := a.apply("deliver", Step{At: at, Result: result, Evidence: evidence}, by)
	if err == nil {
		a.Result, a.Evidence, a.Question = result, evidence, ""
	}
	return evs, err
}

// Answer settles an approval / decision / clarification / review with a
// choice (one of Options when there are options).
func (a *Assignment) Answer(choice, note string, at time.Time, by Actor) ([]Event, error) {
	if !a.Kind.Answered() {
		return nil, Invalid("a %s assignment is delivered, not answered", a.Kind)
	}
	if len(a.Options) > 0 && !slices.Contains(a.Options, choice) {
		return nil, Invalid("choose one of %v", a.Options)
	}
	evs, err := a.apply("answerq", Step{At: at, Choice: choice, Note: note}, by)
	if err == nil {
		a.Choice, a.Result = choice, note
	}
	return evs, err
}

func (a *Assignment) Fail(reason string, at time.Time, by Actor) ([]Event, error) {
	return a.apply("fail", Step{At: at, Note: reason}, by)
}

func (a *Assignment) Release(reason string, at time.Time, by Actor) ([]Event, error) {
	return a.apply("release", Step{At: at, Note: reason}, by)
}

// ---- requester side ----

// AcceptCounter takes the worker's proposed deadline; work goes ahead.
func (a *Assignment) AcceptCounter(at time.Time, by Actor) ([]Event, error) {
	evs, err := a.apply("accept_counter", Step{At: at, Due: a.ProposedDue}, by)
	if err == nil {
		a.Brief.Due, a.ProposedDue = a.ProposedDue, nil
	}
	return evs, err
}

// AnswerQuestion replies to the worker's question.
func (a *Assignment) AnswerQuestion(text string, at time.Time, by Actor) ([]Event, error) {
	evs, err := a.apply("answer", Step{At: at, Note: text}, by)
	if err == nil {
		a.Question = ""
	}
	return evs, err
}

func (a *Assignment) Verify(note string, at time.Time, by Actor) ([]Event, error) {
	return a.apply("verify", Step{At: at, Note: note}, by)
}

func (a *Assignment) Revise(what string, at time.Time, by Actor) ([]Event, error) {
	return a.apply("revise", Step{At: at, Note: what}, by)
}

func (a *Assignment) Cancel(reason string, at time.Time, by Actor) ([]Event, error) {
	return a.apply("cancel", Step{At: at, Note: reason}, by)
}

func (a *Assignment) Expire(at time.Time) ([]Event, error) {
	return a.apply("expire", Step{At: at, Note: "no answer"}, System("rule:expire"))
}

// Redate changes the deadline (the item was rescheduled).
func (a *Assignment) Redate(due *time.Time, at time.Time, by Actor) ([]Event, error) {
	if a.Status.Terminal() {
		return nil, Conflict("assignment %s is %s", a.ID, a.Status)
	}
	a.Brief.Due = due
	a.History = append(a.History, Step{Type: "redate", At: at, By: by, Due: due})
	a.UpdatedAt = at
	return []Event{a.ev("assignment.redated", at, by)}, nil
}

// Nudged records a reminder sent to the worker.
func (a *Assignment) Nudged(note string, at time.Time) []Event {
	a.History = append(a.History, Step{Type: "nudge", At: at, By: System("rule:nudge"), Note: note})
	a.UpdatedAt = at
	return []Event{a.ev("assignment.nudged", at, System("rule:nudge"))}
}

// ---- rules (pure) ----

func (a Assignment) Open() bool { return !a.Status.Terminal() }

// Nudges lists when reminders were sent.
func (a Assignment) Nudges() []time.Time {
	var out []time.Time
	for _, s := range a.History {
		if s.Type == "nudge" {
			out = append(out, s.At)
		}
	}
	return out
}

// WaitingOnRequester: the worker is waiting for us (an answer, a verdict,
// a decision on a counter).
func (a Assignment) WaitingOnRequester() (string, bool) {
	switch a.Status {
	case Blocked:
		if a.Question != "" {
			return "question", true
		}
	case Delivered:
		return "verify", true
	case Countered:
		return "counter", true
	}
	return "", false
}

// Started is when work began (accepted or first started).
func (a Assignment) Started() time.Time {
	for _, s := range a.History {
		if s.Type == "accept" || s.Type == "start" || s.Type == "accept_counter" {
			return s.At
		}
	}
	return time.Time{}
}

// DeliveredAt is when the last delivery (or answer) happened.
func (a Assignment) DeliveredAt() time.Time {
	var t time.Time
	for _, s := range a.History {
		if s.Type == "deliver" || s.Type == "answerq" {
			t = s.At
		}
	}
	return t
}
