// Package app is the application layer: commands (with authorization),
// queries (world snapshots, agenda, progress, staffing), the control loop
// (rules, follow-ups) and the ports adapters implement. It orchestrates the
// domain; it holds no business rules of its own beyond sequencing.
package app

import (
	"context"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/attention"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
)

// State is everything the store keeps. Aggregates are values; a transaction
// works on a copy and the store swaps it in on commit.
type State struct {
	Seq         map[string]int                         `json:"seq"`
	Projects    map[ProjectID]portfolio.Project        `json:"projects"`
	Items       map[ItemID]planning.Item               `json:"items"`
	Assignments map[AssignmentID]delegation.Assignment `json:"assignments"`
	Workers     map[WorkerID]workforce.Worker          `json:"workers"`
	States      map[WorkerID]workforce.WorkerState     `json:"states"`
	Resolutions map[string]attention.Resolution        `json:"resolutions"`
	Raised      map[string]Raised                      `json:"raised"` // signals the brain was already woken for
}

// Raised: a signal the brain has been woken for.
type Raised struct {
	Level     attention.Level `json:"level"`
	At        time.Time       `json:"at"`
	Escalated bool            `json:"escalated,omitempty"` // also sent straight to the owner
}

func NewState() *State {
	return &State{Seq: map[string]int{}, Projects: map[ProjectID]portfolio.Project{}, Items: map[ItemID]planning.Item{},
		Assignments: map[AssignmentID]delegation.Assignment{}, Workers: map[WorkerID]workforce.Worker{},
		States: map[WorkerID]workforce.WorkerState{}, Resolutions: map[string]attention.Resolution{}, Raised: map[string]Raised{}}
}

// Fill makes sure every map exists (after loading an older file).
func (s *State) Fill() {
	f := NewState()
	if s.Seq == nil {
		s.Seq = f.Seq
	}
	if s.Projects == nil {
		s.Projects = f.Projects
	}
	if s.Items == nil {
		s.Items = f.Items
	}
	if s.Assignments == nil {
		s.Assignments = f.Assignments
	}
	if s.Workers == nil {
		s.Workers = f.Workers
	}
	if s.States == nil {
		s.States = f.States
	}
	if s.Resolutions == nil {
		s.Resolutions = f.Resolutions
	}
	if s.Raised == nil {
		s.Raised = f.Raised
	}
}

// NextID allocates "<prefix><n>".
func (s *State) NextID(prefix string) string {
	s.Seq[prefix]++
	return prefix + itoa(s.Seq[prefix])
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Store is the unit of work. Transactions are serialized: fn works on a
// private copy, which replaces the state (and is persisted) only if fn
// returns nil. Events are appended to the log in the same commit.
type Store interface {
	Tx(ctx context.Context, fn func(s *State) ([]Event, error)) ([]Event, error)
	Read(fn func(s *State))
}

// WorkerDriver gets work to a kind of worker and back. One per kind
// (agent sessions, people, agentflow, other bots…). Drivers report what
// happens through App.Report.
type WorkerDriver interface {
	// Observe returns facts measurable now (presence, load, health…).
	Observe(ctx context.Context, w workforce.Worker, open []delegation.Assignment) []workforce.Fact
	// Offer delivers a new assignment.
	Offer(ctx context.Context, w workforce.Worker, a delegation.Assignment) error
	// Notify relays something from the requester side (answer, verdict,
	// cancellation, new deadline, reminder).
	Notify(ctx context.Context, w workforce.Worker, a delegation.Assignment, n Notice) error
}

// Notice is a requester-side message to a worker.
type Notice struct {
	Kind    string // answer | verified | revise | cancelled | redated | counter_accepted | nudge
	Text    string
	Urgency workforce.Urgency
}

// Trigger is a reason to wake the brain.
type Trigger struct {
	Kind    string // event | rule | cadence | message
	Conv    string // conversation to handle it in ("" = the owner's)
	Project ProjectID
	Text    string
	Signals []attention.Signal
	Event   *Event
	About   Actor
}

// Waker hands triggers to the cognition layer.
type Waker interface {
	Wake(ctx context.Context, t Trigger)
}

// WakerFunc adapts a function.
type WakerFunc func(ctx context.Context, t Trigger)

func (f WakerFunc) Wake(ctx context.Context, t Trigger) { f(ctx, t) }

// EventSink receives committed events (audit, metrics, board sync…).
type EventSink interface {
	Publish(ctx context.Context, evs []Event)
}
