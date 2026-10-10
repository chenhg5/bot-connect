// Package inbox holds attention signals: everything that may deserve the
// bot's attention — a message, a mention, a worker's reply or delivery, a
// risk a rule found, a cadence coming due — recorded once (deduplicated) with
// a lifecycle. A signal is not a trigger: whether to wake the brain, and what
// to do first once awake, are separate decisions (wake policy, triage, the
// brain itself).
package inbox

import (
	"strings"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// Source is where a signal comes from.
type Source string

const (
	DirectMessage Source = "direct_message" // someone wrote to the bot privately
	Mention       Source = "mention"        // someone @'d the bot in a group
	WorkerReply   Source = "worker_reply"   // a worker accepted / declined / countered / asked / delivered / answered
	Relay         Source = "relay"          // passed on from an isolated conversation (a colleague's request…)
	Risk          Source = "risk"           // a rule found a problem
	Cadence       Source = "cadence"        // a project's rhythm came due
	Internal      Source = "system"         // anything else from the scaffold
)

type Status string

const (
	Pending  Status = "pending"  // new, not shown to the brain yet
	Seen     Status = "seen"     // shown in a wake-up; not handled yet
	Handling Status = "handling" // the brain is on it (avoid double handling)
	Deferred Status = "deferred" // later: comes back at Until
	Handled  Status = "handled"
	Ignored  Status = "ignored" // decided no action is needed (with a reason)
	Expired  Status = "expired"
)

// Terminal statuses are never reopened.
func (s Status) Terminal() bool { return s == Handled || s == Ignored || s == Expired }

// Open: still needs attention (now or later).
func (s Status) Open() bool { return !s.Terminal() }

// Refs link a signal to the things it is about.
type Refs struct {
	Project    ProjectID    `json:"project,omitempty"`
	Item       ItemID       `json:"item,omitempty"`
	Assignment AssignmentID `json:"assignment,omitempty"`
	Worker     WorkerID     `json:"worker,omitempty"`
}

// ReplyTo is where an answer to the signal goes: the conversation (and
// message) it came from. The brain replies to a signal, not to a channel.
type ReplyTo struct {
	Conv      string `json:"conv,omitempty"`
	MessageID string `json:"message_id,omitempty"`
}

// Signal is an aggregate root.
type Signal struct {
	ID        string     `json:"id"`
	DedupeKey string     `json:"dedupe_key"`
	Source    Source     `json:"source"`
	Reason    string     `json:"reason"` // finer than Source: delivered, countered, will_miss, standup…
	Actor     Actor      `json:"actor"`
	ActorName string     `json:"actor_name,omitempty"`
	Summary   string     `json:"summary"`
	Body      string     `json:"body,omitempty"`
	Refs      Refs       `json:"refs"`
	ReplyTo   ReplyTo    `json:"reply_to"`
	Level     int        `json:"level"`          // 0 low, 1 medium, 2 high
	Wake      string     `json:"wake,omitempty"` // now | batch | ignore (decided at intake)
	Status    Status     `json:"status"`
	Until     *time.Time `json:"until,omitempty"` // deferred until
	Note      string     `json:"note,omitempty"`  // why handled / ignored / deferred
	CreatedAt time.Time  `json:"created_at"`
	SeenAt    time.Time  `json:"seen_at,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
	Version   int        `json:"version"`
}

// New creates a pending signal.
func New(id, dedupe string, src Source, reason string, now time.Time) (Signal, error) {
	if id == "" || strings.TrimSpace(dedupe) == "" {
		return Signal{}, Invalid("a signal needs an id and a dedupe key")
	}
	return Signal{ID: id, DedupeKey: dedupe, Source: src, Reason: reason, Status: Pending, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Signal) ev(typ string, at time.Time, by Actor) Event {
	return NewEvent(typ, "signal:"+s.ID, at, by, "source", string(s.Source), "reason", s.Reason, "status", string(s.Status))
}

// MarkSeen: shown to the brain in a wake-up.
func (s *Signal) MarkSeen(now time.Time) {
	if s.Status == Pending {
		s.Status, s.SeenAt, s.UpdatedAt = Seen, now, now
	}
}

// Transition applies a decision about the signal.
func (s *Signal) Transition(to Status, note string, until *time.Time, now time.Time, by Actor) ([]Event, error) {
	if s.Status.Terminal() {
		return nil, Conflict("signal %s is already %s", s.ID, s.Status)
	}
	switch to {
	case Handling, Handled:
	case Ignored:
		if strings.TrimSpace(note) == "" {
			return nil, Invalid("say why the signal needs no action")
		}
	case Deferred:
		if until == nil || !until.After(now) {
			return nil, Invalid("deferring needs a time in the future")
		}
		s.Until = until
	case Expired:
	default:
		return nil, Invalid("a signal can become handling, handled, ignored, deferred or expired")
	}
	s.Status, s.Note, s.UpdatedAt = to, note, now
	return []Event{s.ev("signal."+string(to), now, by)}, nil
}

// DueAgain: a deferred signal whose time has come goes back to pending.
func (s *Signal) DueAgain(now time.Time) bool {
	if s.Status == Deferred && s.Until != nil && !now.Before(*s.Until) {
		s.Status, s.Until, s.UpdatedAt = Pending, nil, now
		return true
	}
	return false
}

// Waiting reports whether a person is waiting for an answer.
func (s Signal) Waiting() bool {
	return s.Source == DirectMessage || s.Source == Mention || s.Source == Relay ||
		(s.Source == WorkerReply && (s.Reason == "asked" || s.Reason == "countered" || s.Reason == "delivered"))
}

// Score orders open signals: what matters most now. weight is the related
// project's priority weight (P0 8 … P3 1; 2 when unknown).
//
//	base:  direct message 6, mention 5, worker reply 5, relay 4, risk 3 + 2×level, cadence 2, system 2
//	× priority weight / 2
//	+ waiting: someone waits on us: +1 per hour, at most +8
func (s Signal) Score(weight float64, now time.Time) float64 {
	base := map[Source]float64{DirectMessage: 6, Mention: 5, WorkerReply: 5, Relay: 4, Risk: 3, Cadence: 2, Internal: 2}[s.Source]
	if s.Source == Risk {
		base += 2 * float64(s.Level)
	}
	if weight <= 0 {
		weight = 2
	}
	score := base * weight / 2
	if s.Waiting() {
		score += min(now.Sub(s.CreatedAt).Hours(), 8)
	}
	return score
}
