// Package workforce is the Workforce context: who can do work (agents,
// people, other bots), what they can do, what they may approve, how and when
// to reach them, and how they are doing right now.
package workforce

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

type Kind string

const (
	AgentSession Kind = "agent_session"
	Agentflow    Kind = "agentflow"
	Human        Kind = "human"
	Bot          Kind = "bot"
	A2A          Kind = "a2a"
	Command      Kind = "command"
)

// Agentic reports whether the worker is software we can hand work to
// without asking a person.
func (k Kind) Agentic() bool { return k == AgentSession || k == Agentflow || k == Command }

type Trust string

const (
	Controlled Trust = "controlled" // we run it and can sandbox it
	External   Trust = "external"   // we only control what we send and how we treat what comes back
)

// Worker is the aggregate root of a worker's profile: stable facts.
type Worker struct {
	ID          WorkerID     `json:"id"`
	Name        string       `json:"name"`
	Kind        Kind         `json:"kind"`
	Trust       Trust        `json:"trust"`
	Description string       `json:"description,omitempty"`
	Skills      []string     `json:"skills,omitempty"`
	Principal   string       `json:"principal,omitempty"` // owner | self | the other bot's owner
	Interaction Interaction  `json:"interaction"`
	Contact     []Route      `json:"contact,omitempty"`
	Norms       Norms        `json:"norms"`
	Consent     Consent      `json:"consent"`
	Driver      string       `json:"driver,omitempty"` // which WorkerDriver runs it (config key)
	Capability  []Capability `json:"capabilities,omitempty"`
	Authority   []Authority  `json:"authority,omitempty"`
	Physical    bool         `json:"physical,omitempty"` // can act in the real world (calls, presence, signatures…)
	// Identities recognise a person's messages on chat platforms: user id,
	// union id or email, optionally prefixed ("feishu:ou_…", "email:a@b.com").
	Identities []string `json:"identities,omitempty"`
	Version    int      `json:"version"`
}

type Interaction struct {
	Sessions        bool `json:"sessions"`
	CanAsk          bool `json:"can_ask"`
	CanRefuse       bool `json:"can_refuse"`
	CanCounter      bool `json:"can_counter"`
	ReportsProgress bool `json:"reports_progress"`
}

// Consent: what a person agreed to take from the bot.
type Consent struct {
	Given bool       `json:"given"`
	From  []string   `json:"from,omitempty"` // roles or user ids; empty = owner only
	Until *time.Time `json:"until,omitempty"`
}

// Capability is access to a system: "repo:tapnow" write, "gcloud-logging" read.
type Capability struct {
	System string `json:"system"`
	Access string `json:"access"` // read | write | admin
	Note   string `json:"note,omitempty"`
}

// Authority is what a person may approve: action "merge:main" in scope "repo:tapnow".
type Authority struct {
	Action string `json:"action"`
	Scope  string `json:"scope,omitempty"`
}

var accessRank = map[string]int{"read": 1, "write": 2, "admin": 3}

// ParseCapability reads "system:access" where access is the last segment
// ("repo:tapnow:write"); without an access level, read is assumed.
func ParseCapability(s string) Capability {
	if i := strings.LastIndex(s, ":"); i > 0 {
		if _, ok := accessRank[s[i+1:]]; ok {
			return Capability{System: s[:i], Access: s[i+1:]}
		}
	}
	return Capability{System: s, Access: "read"}
}

func (c Capability) String() string { return c.System + ":" + c.Access }

// Covers reports whether this capability is enough for need.
func (c Capability) Covers(need Capability) bool {
	return ScopeMatches(c.System, need.System) && accessRank[c.Access] >= accessRank[need.Access]
}

// Validate checks the profile's invariants.
func (w Worker) Validate() error {
	if w.ID == "" || strings.ContainsAny(string(w.ID), " @#") {
		return Invalid("worker id %q must be non-empty without spaces, @ or #", w.ID)
	}
	switch w.Kind {
	case AgentSession, Agentflow, Human, Bot, A2A, Command:
	default:
		return Invalid("worker %s: unknown kind %q", w.ID, w.Kind)
	}
	if w.Trust != Controlled && w.Trust != External {
		return Invalid("worker %s: trust must be controlled or external", w.ID)
	}
	if w.Kind == Human && w.Trust != External {
		return Invalid("worker %s: a person is always external", w.ID)
	}
	if w.Physical && w.Kind != Human {
		return Invalid("worker %s: only people act in the real world", w.ID)
	}
	if err := w.Norms.Validate(); err != nil {
		return fmt.Errorf("worker %s: %w", w.ID, err)
	}
	return nil
}

// Is reports whether one of the given platform identities is this worker.
func (w Worker) Is(ids ...string) bool {
	for _, mine := range w.Identities {
		if _, rest, ok := strings.Cut(mine, ":"); ok {
			mine = rest
		}
		for _, id := range ids {
			if id != "" && strings.EqualFold(id, mine) {
				return true
			}
		}
	}
	return false
}

// Can reports whether the worker has a capability that covers need.
func (w Worker) Can(need Capability) bool {
	for _, c := range w.Capability {
		if c.Covers(need) {
			return true
		}
	}
	return false
}

// MayApprove reports whether the worker holds authority for action in scope.
func (w Worker) MayApprove(action, scope string) bool {
	for _, a := range w.Authority {
		if a.Action == action && ScopeMatches(a.Scope, scope) {
			return true
		}
	}
	return false
}

// AcceptsFrom reports whether the worker takes work from this actor.
// Controlled agents take work from the owner and admins; people need consent.
func (w Worker) AcceptsFrom(by Actor, now time.Time) bool {
	if w.ID == Owner {
		return true
	}
	if w.Kind.Agentic() && w.Trust == Controlled {
		return by.Privileged() || by.Role == RoleSystem
	}
	c := w.Consent
	if !c.Given || (c.Until != nil && !now.Before(*c.Until)) {
		return false
	}
	from := c.From
	if len(from) == 0 {
		from = []string{string(RoleOwner)}
	}
	for _, f := range from {
		if f == string(by.Role) || f == by.UserID || (f == string(RoleAdmin) && by.Role == RoleOwner) {
			return true
		}
	}
	return false
}

// ---- reaching people ----

// Urgency of a message to a worker.
type Urgency int

const (
	Normal Urgency = iota
	Reminder
	Urgent
	Critical
)

func (u Urgency) String() string { return [...]string{"normal", "reminder", "urgent", "critical"}[u] }

// RouteKind is a way to reach someone (feishu_dm, feishu_urgent_phone, email, a2a…).
// The set is open: adapters register reachers for kinds they support.
type RouteKind string

// Route is one way to reach one worker.
type Route struct {
	Kind       RouteKind `json:"kind"`
	Address    string    `json:"address"`
	MinUrgency Urgency   `json:"min_urgency,omitempty"`
	Approval   bool      `json:"approval,omitempty"` // the owner approves each use
}

// Window is a daily range "HH:MM"–"HH:MM", optionally on some weekdays; End
// before Start wraps midnight.
type Window struct {
	Start string         `json:"start"`
	End   string         `json:"end"`
	Days  []time.Weekday `json:"days,omitempty"`
}

func hhmm(s string) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok {
		return 0, Invalid("time %q is not HH:MM", s)
	}
	hi, e1 := strconv.Atoi(h)
	mi, e2 := strconv.Atoi(m)
	if e1 != nil || e2 != nil || hi < 0 || hi > 24 || mi < 0 || mi > 59 {
		return 0, Invalid("time %q is not HH:MM", s)
	}
	return hi*60 + mi, nil
}

func (w Window) Validate() error {
	if _, err := hhmm(w.Start); err != nil {
		return err
	}
	_, err := hhmm(w.End)
	return err
}

// Contains reports whether t (in the right location) is inside the window.
func (w Window) Contains(t time.Time) bool {
	s, e1 := hhmm(w.Start)
	e, e2 := hhmm(w.End)
	if e1 != nil || e2 != nil {
		return false
	}
	m, day := t.Hour()*60+t.Minute(), t.Weekday()
	in := false
	if s <= e {
		in = m >= s && m < e
	} else if m >= s {
		in = true
	} else if m < e {
		in, day = true, (day+6)%7 // after midnight: belongs to yesterday's window
	}
	if !in || len(w.Days) == 0 {
		return in
	}
	for _, d := range w.Days {
		if d == day {
			return true
		}
	}
	return false
}

// Norms: when and how often a worker may be contacted. Enforced by code.
type Norms struct {
	Timezone         string        `json:"timezone,omitempty"`
	WorkHours        []Window      `json:"work_hours,omitempty"` // empty = any time
	QuietHours       []Window      `json:"quiet_hours,omitempty"`
	MaxNudgesPerDay  int           `json:"max_nudges_per_day,omitempty"`
	MinNudgeInterval time.Duration `json:"min_nudge_interval,omitempty"`
}

func (n Norms) Validate() error {
	if n.Timezone != "" {
		if _, err := time.LoadLocation(n.Timezone); err != nil {
			return Invalid("timezone %q: %v", n.Timezone, err)
		}
	}
	for _, w := range append(append([]Window{}, n.WorkHours...), n.QuietHours...) {
		if err := w.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (n Norms) loc() *time.Location {
	if n.Timezone != "" {
		if l, err := time.LoadLocation(n.Timezone); err == nil {
			return l
		}
	}
	return time.Local
}

func in(ws []Window, t time.Time) bool {
	for _, w := range ws {
		if w.Contains(t) {
			return true
		}
	}
	return false
}

// Available reports whether, by the clock, the worker may be contacted now
// with a message of this urgency (critical messages ignore hours).
func (n Norms) Available(u Urgency, now time.Time) error {
	if u >= Critical {
		return nil
	}
	t := now.In(n.loc())
	if in(n.QuietHours, t) {
		return Forbidden("quiet hours")
	}
	if len(n.WorkHours) > 0 && !in(n.WorkHours, t) {
		return Forbidden("outside work hours")
	}
	return nil
}

func (n Norms) maxNudges() int {
	if n.MaxNudgesPerDay > 0 {
		return n.MaxNudgesPerDay
	}
	return 2
}

func (n Norms) minInterval() time.Duration {
	if n.MinNudgeInterval > 0 {
		return n.MinNudgeInterval
	}
	return 3 * time.Hour
}

// NudgeAllowed reports whether one more reminder may be sent now, given the
// times of earlier reminders about the same thing. ErrNudgeBudget means the
// daily budget is spent: stop reminding and tell the requester instead.
func (n Norms) NudgeAllowed(earlier []time.Time, now time.Time) error {
	var today int
	var last time.Time
	for _, t := range earlier {
		if now.Sub(t) < 24*time.Hour {
			today++
		}
		if t.After(last) {
			last = t
		}
	}
	if today >= n.maxNudges() {
		return ErrNudgeBudget
	}
	if !last.IsZero() && now.Sub(last) < n.minInterval() {
		return Forbidden("reminded %s ago", now.Sub(last).Round(time.Minute))
	}
	return n.Available(Reminder, now)
}

// ErrNudgeBudget: the reminder budget for the last 24 hours is used up.
var ErrNudgeBudget = Forbidden("reminder budget for today is used up")

// Plan picks the routes usable for a message now, in escalation order.
func (w Worker) Plan(u Urgency, now time.Time) ([]Route, error) {
	if err := w.Norms.Available(u, now); err != nil {
		return nil, err
	}
	var out []Route
	for _, r := range w.Contact {
		if r.MinUrgency <= u {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, Invalid("%s has no contact route for a %s message", w.ID, u)
	}
	return out, nil
}
