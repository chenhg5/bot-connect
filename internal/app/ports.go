// Package app is the application layer: commands (with authorization),
// queries (world snapshots, agenda, progress, staffing), the control loop
// (rules, follow-ups) and the ports adapters implement. It orchestrates the
// domain; it holds no business rules of its own beyond sequencing.
package app

import (
	"context"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/inbox"
	"github.com/chenhg5/bot-connect/internal/domain/observe"
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
	Signals     map[string]inbox.Signal                `json:"signals"`             // the attention inbox
	Contacts    map[string]Contact                     `json:"contacts"`            // messages the bot sent people via contact
	Escalated   map[string]bool                        `json:"escalated,omitempty"` // risk signals sent straight to the principal
	LastBatch   time.Time                              `json:"last_batch,omitempty"`
	// Following work the bot doesn't run: its handbook per project, the
	// sources it watches, and command patterns the principal allowed.
	Dossiers map[ProjectID]observe.Dossier `json:"dossiers,omitempty"`
	Watches  map[string]observe.Watch      `json:"watches,omitempty"`
	Allowed  []string                      `json:"allowed_commands,omitempty"`
	Granted  []string                      `json:"act_permissions,omitempty"` // standing permissions to act
	Done     []observe.Done                `json:"acts,omitempty"`            // recent actions (audit)
	Proposed map[string]ActProposal        `json:"act_proposals,omitempty"`   // actions waiting for the principal
	Tools    map[string]observe.Tool       `json:"tools,omitempty"`           // the toolbox
	Wakeups  map[string]Wakeup             `json:"wakeups,omitempty"`         // times the brain asked to be woken
}

// Contact is a message the brain sent someone through the contact tool.
type Contact struct {
	ID          string     `json:"id"`
	Worker      WorkerID   `json:"worker"`
	Message     string     `json:"message"`
	About       inbox.Refs `json:"about"`
	ExpectReply bool       `json:"expect_reply"`
	Open        bool       `json:"open"` // still waiting for their reply
	At          time.Time  `json:"at"`
	By          Actor      `json:"by"`
}

func NewState() *State {
	return &State{Seq: map[string]int{}, Projects: map[ProjectID]portfolio.Project{}, Items: map[ItemID]planning.Item{},
		Assignments: map[AssignmentID]delegation.Assignment{}, Workers: map[WorkerID]workforce.Worker{},
		States: map[WorkerID]workforce.WorkerState{}, Signals: map[string]inbox.Signal{}, Contacts: map[string]Contact{}, Escalated: map[string]bool{},
		Dossiers: map[ProjectID]observe.Dossier{}, Watches: map[string]observe.Watch{}, Proposed: map[string]ActProposal{}, Tools: map[string]observe.Tool{}, Wakeups: map[string]Wakeup{}}
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
	if s.Signals == nil {
		s.Signals = f.Signals
	}
	if s.Contacts == nil {
		s.Contacts = f.Contacts
	}
	if s.Escalated == nil {
		s.Escalated = f.Escalated
	}
	if s.Dossiers == nil {
		s.Dossiers = f.Dossiers
	}
	if s.Watches == nil {
		s.Watches = f.Watches
	}
	if s.Proposed == nil {
		s.Proposed = map[string]ActProposal{}
	}
	if s.Tools == nil {
		s.Tools = map[string]observe.Tool{}
	}
	if s.Wakeups == nil {
		s.Wakeups = map[string]Wakeup{}
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

// Wake asks the cognition layer to handle signals now: the focus of a
// wake-up (the rest of the inbox is listed alongside, not pushed).
type Wake struct {
	Signals   []inbox.Signal
	ReplyConv string // where a plain reply goes ("" = the principal's private chat)
}

// Waker hands wake-ups to the cognition layer.
type Waker interface {
	Wake(ctx context.Context, w Wake)
}

// WakerFunc adapts a function.
type WakerFunc func(ctx context.Context, w Wake)

func (f WakerFunc) Wake(ctx context.Context, w Wake) { f(ctx, w) }

// Triage is the L1 judgment on an incoming signal (a System One model, or
// rules): it may raise or lower its level and override when to wake. The
// default does nothing.
type Triage interface {
	Assess(ctx context.Context, s inbox.Signal) (TriageResult, error)
}

type TriageResult struct {
	Wake       string // "" keep the policy's decision | now | batch | ignore
	Level      *int
	Note       string
	Intent     string  // chat | status | request | reply | decision | who | other
	Action     string  // for a reply to an assignment: accept | decline | counter | question | deliver | progress
	Confidence float64 // of Action
	Hints      []string
	NeedsPlan  bool
	Scope      string // goal | program | explore | handoff | other (principal requests)
}

// Say posts a message into a conversation (set by the wiring; used for
// acknowledgements the scaffold gives without waking the brain).
type Say func(ctx context.Context, convKey, text string) error

// EventSink receives committed events (audit, metrics, board sync…).
type EventSink interface {
	Publish(ctx context.Context, evs []Event)
}
