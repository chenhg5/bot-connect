package workforce

import (
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/identity"
)

// Status of an assignment: one piece of work handed to one worker.
type Status string

const (
	Offered   Status = "offered"   // sent, no answer yet
	Countered Status = "countered" // worker proposed another deadline / scope
	Accepted  Status = "accepted"
	Working   Status = "working"
	Blocked   Status = "blocked"   // waiting for an answer (input_required) or something external
	Paused    Status = "paused"    // worker paused / unreachable for now
	Delivered Status = "delivered" // result handed in, not yet verified
	Revising  Status = "revising"  // requester asked for changes
	Verified  Status = "verified"  // done: result accepted
	Declined  Status = "declined"
	Failed    Status = "failed"
	Cancelled Status = "cancelled" // by the requester
	Released  Status = "released"  // handed back by the worker ("can't do it after all")
	Expired   Status = "expired"   // no answer to the offer in time
)

// Terminal reports whether nothing more will happen to the assignment.
func (s Status) Terminal() bool {
	switch s {
	case Verified, Declined, Failed, Cancelled, Released, Expired:
		return true
	}
	return false
}

// A2A maps the status onto the Agent2Agent task states, for interop.
func (s Status) A2A() string {
	switch s {
	case Offered, Countered:
		return "submitted"
	case Accepted, Working, Revising:
		return "working"
	case Blocked:
		return "input-required"
	case Delivered, Verified:
		return "completed"
	case Declined:
		return "rejected"
	case Cancelled, Expired:
		return "canceled"
	case Failed, Released:
		return "failed"
	}
	return "unknown"
}

// EventType is something that happens to an assignment.
type EventType string

const (
	EvOffer    EventType = "offer"
	EvAccept   EventType = "accept"
	EvDecline  EventType = "decline"
	EvCounter  EventType = "counter" // Due / Note carry the proposal
	EvStart    EventType = "start"
	EvProgress EventType = "progress" // Note, ETA
	EvAsk      EventType = "ask"      // worker needs input: Note is the question
	EvAnswer   EventType = "answer"
	EvBlock    EventType = "block" // waiting on something external
	EvPause    EventType = "pause"
	EvResume   EventType = "resume"
	EvDeliver  EventType = "deliver" // Result, Evidence
	EvVerify   EventType = "verify"  // requester accepts the delivery
	EvRevise   EventType = "revise"  // requester wants changes: Note
	EvFail     EventType = "fail"
	EvCancel   EventType = "cancel"
	EvRelease  EventType = "release"
	EvExpire   EventType = "expire"
	EvNudge    EventType = "nudge"  // reminder sent (no status change)
	EvRisk     EventType = "risk"   // risk raised on this assignment (no status change)
	EvRedate   EventType = "redate" // requester changed the deadline: Due
)

// Event is one entry in an assignment's history.
type Event struct {
	Type     EventType     `json:"type"`
	At       time.Time     `json:"at"`
	By       identity.User `json:"by"`
	Note     string        `json:"note,omitempty"`
	ETA      *time.Time    `json:"eta,omitempty"`
	Due      *time.Time    `json:"due,omitempty"`
	Result   string        `json:"result,omitempty"`
	Evidence []string      `json:"evidence,omitempty"`
	Ref      string        `json:"ref,omitempty"` // worker-side id (e.g. the agent task id)
}

// Brief is what the worker is asked to do. It must stand on its own: the
// worker doesn't see the conversation it came from, and an external worker
// gets nothing beyond it.
type Brief struct {
	Goal     string        `json:"goal"`               // what and why
	Context  string        `json:"context,omitempty"`  // only what's needed to do it
	Done     string        `json:"done"`               // acceptance criteria
	Evidence string        `json:"evidence,omitempty"` // what proof to hand in (link, commit, numbers…)
	Due      *time.Time    `json:"due,omitempty"`
	Estimate time.Duration `json:"estimate,omitempty"` // requester's estimate of the effort
	Priority int           `json:"priority,omitempty"` // 0 normal, 1 high, 2 urgent
}

// Assignment is one piece of work handed to one worker. A plan task may go
// through several assignments (reassigned after a decline, say).
type Assignment struct {
	ID        string        `json:"id"`
	TaskID    string        `json:"task_id,omitempty"` // plan task it belongs to
	Worker    string        `json:"worker"`
	Bot       string        `json:"bot,omitempty"`
	ConvKey   string        `json:"conv,omitempty"` // conversation to report back to
	Requester identity.User `json:"requester"`
	Brief     Brief         `json:"brief"`
	Status    Status        `json:"status"`
	Question  string        `json:"question,omitempty"` // open question while Blocked
	ETA       *time.Time    `json:"eta,omitempty"`
	Result    string        `json:"result,omitempty"`
	Evidence  []string      `json:"evidence,omitempty"`
	History   []Event       `json:"history"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`            // last event of any kind
	Progress  time.Time     `json:"progress_at,omitempty"` // last sign of life from the worker
	Ref       string        `json:"ref,omitempty"`         // worker-side id, e.g. the agent task running it
}

// transitions: event → (allowed from-statuses → resulting status). Events
// absent here (nudge, risk, progress, redate) don't change the status.
var transitions = map[EventType]struct {
	from []Status
	to   Status
}{
	EvAccept:  {[]Status{Offered, Countered}, Accepted},
	EvDecline: {[]Status{Offered, Countered}, Declined},
	EvCounter: {[]Status{Offered}, Countered},
	EvStart:   {[]Status{Accepted, Paused, Blocked, Revising}, Working},
	EvAsk:     {[]Status{Accepted, Working, Revising}, Blocked},
	EvAnswer:  {[]Status{Blocked}, Working},
	EvBlock:   {[]Status{Accepted, Working, Revising}, Blocked},
	EvPause:   {[]Status{Accepted, Working, Blocked, Revising}, Paused},
	EvResume:  {[]Status{Paused}, Working},
	EvDeliver: {[]Status{Accepted, Working, Revising, Blocked}, Delivered},
	EvVerify:  {[]Status{Delivered}, Verified},
	EvRevise:  {[]Status{Delivered}, Revising},
	EvFail:    {[]Status{Accepted, Working, Blocked, Paused, Revising}, Failed},
	EvCancel:  {[]Status{Offered, Countered, Accepted, Working, Blocked, Paused, Delivered, Revising}, Cancelled},
	EvRelease: {[]Status{Accepted, Working, Blocked, Paused, Revising}, Released},
	EvExpire:  {[]Status{Offered, Countered}, Expired},
}

// Apply validates and records an event.
func (a *Assignment) Apply(ev Event) error {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	switch ev.Type {
	case EvOffer:
		if a.Status != "" {
			return fmt.Errorf("assignment %s already offered", a.ID)
		}
		a.Status, a.CreatedAt = Offered, ev.At
	case EvNudge, EvRisk:
	case EvProgress:
		if a.Status.Terminal() {
			return fmt.Errorf("assignment %s is %s", a.ID, a.Status)
		}
		if ev.ETA != nil {
			a.ETA = ev.ETA
		}
		if a.Status == Accepted {
			a.Status = Working
		}
	case EvRedate:
		if a.Status.Terminal() || ev.Due == nil {
			return fmt.Errorf("can't change the deadline of %s assignment %s", a.Status, a.ID)
		}
		a.Brief.Due = ev.Due
	default:
		tr, ok := transitions[ev.Type]
		if !ok {
			return fmt.Errorf("unknown event %q", ev.Type)
		}
		allowed := false
		for _, s := range tr.from {
			allowed = allowed || s == a.Status
		}
		if !allowed {
			return fmt.Errorf("can't %s assignment %s while it is %s", ev.Type, a.ID, a.Status)
		}
		a.Status = tr.to
		switch ev.Type {
		case EvCounter:
			if ev.Due != nil {
				a.Brief.Due = ev.Due // proposed; takes effect if the requester accepts
			}
		case EvAccept:
			if ev.ETA != nil {
				a.ETA = ev.ETA
			}
		case EvAsk:
			a.Question = ev.Note
		case EvAnswer:
			a.Question = ""
		case EvDeliver:
			a.Result, a.Evidence = ev.Result, ev.Evidence
		}
	}
	switch ev.Type {
	case EvAccept, EvCounter, EvStart, EvProgress, EvAsk, EvDeliver, EvResume:
		a.Progress = ev.At // the worker showed signs of life
	}
	if ev.Ref != "" {
		a.Ref = ev.Ref
	}
	a.UpdatedAt = ev.At
	a.History = append(a.History, ev)
	return nil
}

// Nudges counts reminders sent since t.
func (a Assignment) Nudges(since time.Time) (n int, last time.Time) {
	for _, e := range a.History {
		if e.Type == EvNudge && !e.At.Before(since) {
			n++
			last = e.At
		}
	}
	return n, last
}

// Instruction renders the brief as a self-contained instruction for a worker
// that takes plain text (agents, chat messages).
func (b Brief) Instruction() string {
	var s strings.Builder
	s.WriteString(b.Goal)
	if b.Context != "" {
		s.WriteString("\n\n背景：" + b.Context)
	}
	if b.Done != "" {
		s.WriteString("\n\n完成标准：" + b.Done)
	}
	if b.Evidence != "" {
		s.WriteString("\n\n交付时请附上：" + b.Evidence)
	}
	if b.Due != nil {
		s.WriteString("\n\n截止：" + b.Due.Format("2006-01-02 15:04"))
	}
	return s.String()
}
