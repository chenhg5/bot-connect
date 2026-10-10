// Package shared is the shared kernel of the domain: typed ids, the actor of
// a command, time, priorities and domain events. Every domain package may
// depend on it; it depends on nothing but the standard library.
package shared

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type (
	WorkerID     string
	ProjectID    string
	ItemID       string
	AssignmentID string
)

// OrgProject is the root project: the company. Standing roles and
// company-wide approval rules live on it.
const OrgProject ProjectID = "org"

// Owner is the worker id of the bot's owner as a person.
const Owner WorkerID = "owner"

// Role is a caller's role towards the bot.
type Role string

const (
	RoleOwner   Role = "owner"
	RoleAdmin   Role = "admin"
	RoleMember  Role = "member"
	RoleVisitor Role = "visitor"
	RoleSystem  Role = "system" // rules, drivers, the scaffold itself
)

// Actor is whoever performs a command or causes an event.
type Actor struct {
	UserID string   `json:"user,omitempty"`   // platform identity, when a person acts
	Worker WorkerID `json:"worker,omitempty"` // the worker they are, if any
	Role   Role     `json:"role"`
	Via    string   `json:"via,omitempty"` // "chat" | "brain:L2" | "brain:L3" | "rule:overdue" | "driver:agent" | "cli"
}

// System is the scaffold acting on its own (a rule, a driver).
func System(via string) Actor { return Actor{Role: RoleSystem, Via: via} }

// Privileged: owner or admin.
func (a Actor) Privileged() bool { return a.Role == RoleOwner || a.Role == RoleAdmin }

func (a Actor) String() string {
	switch {
	case a.Worker != "":
		return string(a.Worker)
	case a.UserID != "":
		return a.UserID
	}
	return string(a.Role) + ":" + a.Via
}

// Clock is where time comes from: the real clock, or a fake one in tests and
// simulations.
type Clock interface{ Now() time.Time }

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// FakeClock is a settable clock for tests and the simulator.
type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{t: t} }

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *FakeClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	return c.t
}

// Period is a half-open time range [From, Until); nil ends are open.
type Period struct {
	From  *time.Time `json:"from,omitempty"`
	Until *time.Time `json:"until,omitempty"`
}

func (p Period) Contains(t time.Time) bool {
	return (p.From == nil || !t.Before(*p.From)) && (p.Until == nil || t.Before(*p.Until))
}

// Overlaps reports whether two periods share any instant.
func (p Period) Overlaps(o Period) bool {
	startsBeforeOEnds := p.From == nil || o.Until == nil || p.From.Before(*o.Until)
	oStartsBeforeEnd := o.From == nil || p.Until == nil || o.From.Before(*p.Until)
	return startsBeforeOEnds && oStartsBeforeEnd
}

// Priority: P0 (highest) … P3.
type Priority int

const (
	P0 Priority = iota
	P1
	P2
	P3
)

// Weight is how much attention a priority is worth.
func (p Priority) Weight() float64 {
	switch p {
	case P0:
		return 8
	case P1:
		return 4
	case P2:
		return 2
	}
	return 1
}

func (p Priority) String() string { return fmt.Sprintf("P%d", int(p)) }

func ParsePriority(s string) (Priority, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "P0":
		return P0, nil
	case "P1":
		return P1, nil
	case "P2", "":
		return P2, nil
	case "P3":
		return P3, nil
	}
	return P2, fmt.Errorf("priority must be P0..P3, not %q", s)
}

// NeedKind is why something needs a particular kind of worker — in
// particular, why a person rather than an agent.
type NeedKind string

const (
	NeedCapability   NeedKind = "capability"   // a system, tool, access or credential
	NeedApproval     NeedKind = "approval"     // a rule requires a person's approval
	NeedDecision     NeedKind = "decision"     // ambiguity, trade-offs, conflicting goals
	NeedPhysical     NeedKind = "physical"     // a body, an identity, presence
	NeedRelationship NeedKind = "relationship" // speaking for someone to other people
	NeedJudgment     NeedKind = "judgment"     // taste, expertise, the requester's own acceptance
	NeedKnowledge    NeedKind = "knowledge"    // only someone's memory has it
)

// HumanReasons are the need kinds that justify asking a person.
var HumanReasons = []NeedKind{NeedApproval, NeedDecision, NeedPhysical, NeedRelationship, NeedJudgment, NeedKnowledge, NeedCapability}

func ParseNeedKind(s string) (NeedKind, error) {
	for _, k := range HumanReasons {
		if string(k) == s {
			return k, nil
		}
	}
	return "", fmt.Errorf("unknown need %q (one of: approval, decision, physical, relationship, judgment, knowledge, capability)", s)
}

// Event is a domain event: something that happened to an aggregate.
// Aggregates return events from their command methods; the application layer
// persists and publishes them.
type Event struct {
	Type    string         `json:"type"`    // "assignment.delivered"
	Subject string         `json:"subject"` // "assignment:A7"
	At      time.Time      `json:"at"`
	By      Actor          `json:"by"`
	Data    map[string]any `json:"data,omitempty"`
}

func NewEvent(typ, subject string, at time.Time, by Actor, kv ...any) Event {
	e := Event{Type: typ, Subject: subject, At: at, By: by}
	if len(kv) > 0 {
		e.Data = map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			e.Data[fmt.Sprint(kv[i])] = kv[i+1]
		}
	}
	return e
}

// DomainError is a rule violation, with a kind callers can react to.
type DomainError struct {
	Kind string // invalid | forbidden | conflict | not_found
	Msg  string
}

func (e *DomainError) Error() string { return e.Msg }

func Invalid(format string, a ...any) error {
	return &DomainError{"invalid", fmt.Sprintf(format, a...)}
}
func Forbidden(format string, a ...any) error {
	return &DomainError{"forbidden", fmt.Sprintf(format, a...)}
}
func Conflict(format string, a ...any) error {
	return &DomainError{"conflict", fmt.Sprintf(format, a...)}
}
func NotFound(format string, a ...any) error {
	return &DomainError{"not_found", fmt.Sprintf(format, a...)}
}

// ScopeMatches reports whether a rule / authority scope covers an actual
// scope: empty covers everything; otherwise equal, or a prefix at a ":" or
// "/" boundary ("repo:tapnow" covers "repo:tapnow/web").
func ScopeMatches(rule, actual string) bool {
	if rule == "" || rule == "*" {
		return true
	}
	if rule == actual {
		return true
	}
	return strings.HasPrefix(actual, rule) && len(actual) > len(rule) && strings.ContainsRune(":/", rune(actual[len(rule)]))
}
