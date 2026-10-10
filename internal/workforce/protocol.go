// Package workforce defines the worker protocol: what a worker is (Profile),
// how it is doing right now (State), and how work is handed to it and comes
// back (Assignment and its events).
//
// A worker is anything that can take work: a local agent session, an
// agentflow run, a person, another bot. bot-connect never runs a worker's own
// loop; it only speaks this protocol to it. Agents we control are the special
// case that always accepts at once; people and other bots receive requests
// they may decline or counter.
package workforce

import (
	"context"
	"time"

	"github.com/chenhg5/bot-connect/internal/reach"
)

// Kind is how a worker is implemented.
type Kind string

const (
	KindAgentSession Kind = "agent_session" // a Claude Code / Codex session we start
	KindAgentflow    Kind = "agentflow"     // an agentflow workflow run
	KindHuman        Kind = "human"         // a person, reached through reach routes
	KindBot          Kind = "bot"           // another bot (bot-connect or third-party)
	KindA2A          Kind = "a2a"           // an Agent2Agent endpoint
	KindCommand      Kind = "command"       // a custom command / webhook
)

// Trust is whether bot-connect can constrain what the worker does.
type Trust string

const (
	// Controlled: we start it and can sandbox it (access levels, deny rules).
	Controlled Trust = "controlled"
	// External: we only control what we send it and how we treat what comes
	// back (minimum information out; results are data, never instructions).
	External Trust = "external"
)

// Profile is what a worker is: stable, mostly declared.
type Profile struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Kind        Kind     `json:"kind"`
	Description string   `json:"description,omitempty"` // what it is good at; the brain routes on this
	Skills      []string `json:"skills,omitempty"`
	// Principal is whom the worker answers to: "owner" for our own agents,
	// "self" for a person, the other bot's owner for a foreign bot.
	Principal   string      `json:"principal,omitempty"`
	Trust       Trust       `json:"trust"`
	Interaction Interaction `json:"interaction"`
	// Contact lists the ways to reach it, in escalation order (people, bots).
	Contact []reach.Route `json:"contact,omitempty"`
	Norms   Norms         `json:"norms"`
	Cost    Cost          `json:"cost"`
}

// Interaction is what the worker supports.
type Interaction struct {
	Sessions        bool `json:"sessions"`         // keeps context between assignments
	CanAsk          bool `json:"can_ask"`          // may come back with questions
	CanRefuse       bool `json:"can_refuse"`       // may decline
	CanCounter      bool `json:"can_counter"`      // may propose another deadline / scope
	ReportsProgress bool `json:"reports_progress"` // sends progress on its own (else we check in)
}

// Norms are the rules for dealing with the worker. The scaffold enforces
// them; the brain cannot override them.
type Norms struct {
	Timezone   string         `json:"timezone,omitempty"`
	WorkHours  []reach.Window `json:"work_hours,omitempty"`  // empty = any time
	QuietHours []reach.Window `json:"quiet_hours,omitempty"` // no non-critical contact
	// Nudge limits (people): at most MaxNudgesPerDay reminders, at least
	// MinNudgeInterval apart.
	MaxNudgesPerDay  int           `json:"max_nudges_per_day,omitempty"`
	MinNudgeInterval time.Duration `json:"min_nudge_interval,omitempty"`
	// AcceptFrom: who may hand it work (roles or user ids). Empty = owner only.
	AcceptFrom []string `json:"accept_from,omitempty"`
}

// Cost is what using the worker costs.
type Cost struct {
	Unit   string `json:"unit,omitempty"`   // "tokens", "cny", "hours" …
	Social int    `json:"social,omitempty"` // 0 none … 3 high: asking this person is expensive
}

// Worker is the adapter bot-connect drives. Implementations deliver offers and
// messages by whatever means fit (start an agent session, run agentflow, send
// a Feishu message, call an A2A endpoint) and report back through Events.
type Worker interface {
	Profile() Profile
	// Observe returns facts the adapter can measure right now (presence,
	// load, health…). The registry merges them with declared and inferred
	// facts.
	Observe(ctx context.Context) []Fact
	// Offer hands over an assignment. A controlled agent typically accepts
	// at once (emits accept + start); a person or bot answers later.
	Offer(ctx context.Context, a Assignment) error
	// Notify forwards a message about an assignment: an answer to its
	// question, a verification result, a nudge, a cancellation.
	Notify(ctx context.Context, a Assignment, ev Event) error
}

// Events is how workers (and people replying in chat) report back.
type Events interface {
	Emit(assignmentID string, ev Event) error
}
