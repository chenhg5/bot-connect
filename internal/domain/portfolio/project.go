// Package portfolio is the Portfolio context: projects, who holds which role
// in them and when, their cadence, their card (the project's memory) and the
// approval rules that apply inside them. The company is the root project.
package portfolio

import (
	"strings"
	"time"
	"unicode/utf8"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

type Status string

const (
	Proposed  Status = "proposed"
	Active    Status = "active"
	Paused    Status = "paused"
	Done      Status = "done"
	Cancelled Status = "cancelled"
)

// Project is an aggregate root. Memberships, cadence, card and policies are
// small and change with the project, so they live inside it.
type Project struct {
	ID        ProjectID      `json:"id"`
	Parent    ProjectID      `json:"parent,omitempty"`
	Title     string         `json:"title"`
	Objective Objective      `json:"objective"`
	Priority  Priority       `json:"priority"`
	Timebox   Period         `json:"timebox"`
	Status    Status         `json:"status"`
	Members   []Membership   `json:"members,omitempty"`
	Cadence   []Cadence      `json:"cadence,omitempty"`
	Card      Card           `json:"card"`
	Home      string         `json:"home,omitempty"` // conversation for project messages
	Policies  []ApprovalRule `json:"policies,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	Version   int            `json:"version"`
}

type Objective struct {
	Text    string   `json:"text"`
	Metrics []Metric `json:"metrics,omitempty"`
}

type Metric struct {
	Name    string    `json:"name"`
	Target  float64   `json:"target"`
	Current float64   `json:"current"`
	Unit    string    `json:"unit,omitempty"`
	Source  string    `json:"source,omitempty"`
	At      time.Time `json:"at,omitempty"`
}

// Membership: a worker holds a role in this project, with duties, for a period.
type Membership struct {
	Worker WorkerID `json:"worker"`
	Role   string   `json:"role"`
	Duties []string `json:"duties,omitempty"`
	Period Period   `json:"period"`
}

// Cadence: when the project wakes the bot by itself.
type Cadence struct {
	Kind  string `json:"kind"`            // standup | weekly_review | milestone_review | custom
	Spec  string `json:"spec"`            // schedule expression
	Layer string `json:"layer,omitempty"` // default brain layer: L2 | L3
	Note  string `json:"note,omitempty"`
}

// ApprovalRule: an action that needs a person's approval. Approvers are role
// names (resolved through memberships at the time) or worker ids.
type ApprovalRule struct {
	Action    string   `json:"action"`
	Scope     string   `json:"scope,omitempty"`
	Approvers []string `json:"approvers"`
	Quorum    int      `json:"quorum,omitempty"`
}

// Card is the project's memory: small, structured, edited incrementally.
type Card struct {
	Scope       string     `json:"scope,omitempty"`
	Decisions   []Decision `json:"decisions,omitempty"`
	Conventions []string   `json:"conventions,omitempty"`
	Links       []string   `json:"links,omitempty"`
}

type Decision struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"` // what and why
}

// CardLimit bounds the card so it always fits in a briefing.
const CardLimit = 4000

// CardDelta is an incremental change; the card is never rewritten wholesale,
// so details don't erode over many edits.
type CardDelta struct {
	SetScope         *string
	AddDecision      string
	AddConvention    string
	AddLink          string
	RemoveConvention string
	RemoveLink       string
}

func (c Card) size() int {
	n := utf8.RuneCountInString(c.Scope)
	for _, d := range c.Decisions {
		n += utf8.RuneCountInString(d.Text) + 12
	}
	for _, x := range append(append([]string{}, c.Conventions...), c.Links...) {
		n += utf8.RuneCountInString(x)
	}
	return n
}

func (p *Project) ev(typ string, at time.Time, by Actor, kv ...any) Event {
	return NewEvent(typ, "project:"+string(p.ID), at, by, kv...)
}

// New creates a project.
func New(id, parent ProjectID, title string, pr Priority, timebox Period, now time.Time, by Actor) (Project, []Event, error) {
	if id == "" || strings.TrimSpace(title) == "" {
		return Project{}, nil, Invalid("a project needs an id and a title")
	}
	if id != OrgProject && parent == "" {
		parent = OrgProject
	}
	if id == OrgProject && parent != "" {
		return Project{}, nil, Invalid("the root project has no parent")
	}
	if timebox.From != nil && timebox.Until != nil && !timebox.From.Before(*timebox.Until) {
		return Project{}, nil, Invalid("timebox ends before it starts")
	}
	p := Project{ID: id, Parent: parent, Title: title, Priority: pr, Timebox: timebox, Status: Active, CreatedAt: now}
	return p, []Event{p.ev("project.created", now, by, "title", title, "parent", string(parent))}, nil
}

// AddMember adds a role. The same worker can't hold the same role twice in
// overlapping periods.
func (p *Project) AddMember(m Membership, now time.Time, by Actor) ([]Event, error) {
	if m.Worker == "" || strings.TrimSpace(m.Role) == "" {
		return nil, Invalid("a membership needs a worker and a role")
	}
	for _, o := range p.Members {
		if o.Worker == m.Worker && strings.EqualFold(o.Role, m.Role) && o.Period.Overlaps(m.Period) {
			return nil, Conflict("%s is already %s in %s for an overlapping period", m.Worker, m.Role, p.ID)
		}
	}
	p.Members = append(p.Members, m)
	return []Event{p.ev("project.member_added", now, by, "worker", string(m.Worker), "role", m.Role)}, nil
}

// EndMembership ends a worker's role at a time.
func (p *Project) EndMembership(w WorkerID, role string, at time.Time, by Actor) ([]Event, error) {
	for i, m := range p.Members {
		if m.Worker == w && strings.EqualFold(m.Role, role) && m.Period.Contains(at) {
			end := at
			p.Members[i].Period.Until = &end
			return []Event{p.ev("project.member_ended", at, by, "worker", string(w), "role", role)}, nil
		}
	}
	return nil, NotFound("%s holds no active role %q in %s", w, role, p.ID)
}

func (p *Project) SetPriority(pr Priority, now time.Time, by Actor) []Event {
	p.Priority = pr
	return []Event{p.ev("project.priority_changed", now, by, "priority", pr.String())}
}

// Transition changes the status. The root project can't end.
func (p *Project) Transition(to Status, now time.Time, by Actor) ([]Event, error) {
	if p.ID == OrgProject && (to == Done || to == Cancelled) {
		return nil, Forbidden("the root project can't be closed")
	}
	if p.Status == Done || p.Status == Cancelled {
		return nil, Conflict("project %s is already %s", p.ID, p.Status)
	}
	p.Status = to
	return []Event{p.ev("project.status_changed", now, by, "status", string(to))}, nil
}

// UpdateCard applies an incremental change within the size limit.
func (p *Project) UpdateCard(d CardDelta, now time.Time, by Actor) ([]Event, error) {
	c := p.Card
	c.Decisions = append([]Decision(nil), c.Decisions...)
	c.Conventions = append([]string(nil), c.Conventions...)
	c.Links = append([]string(nil), c.Links...)
	if d.SetScope != nil {
		c.Scope = *d.SetScope
	}
	if d.AddDecision != "" {
		c.Decisions = append(c.Decisions, Decision{At: now, Text: d.AddDecision})
	}
	if d.AddConvention != "" {
		c.Conventions = append(c.Conventions, d.AddConvention)
	}
	if d.AddLink != "" {
		c.Links = append(c.Links, d.AddLink)
	}
	c.Conventions = without(c.Conventions, d.RemoveConvention)
	c.Links = without(c.Links, d.RemoveLink)
	if c.size() > CardLimit {
		return nil, Invalid("the project card would exceed %d characters; consolidate or remove entries first", CardLimit)
	}
	p.Card = c
	return []Event{p.ev("project.card_updated", now, by)}, nil
}

func without(v []string, x string) []string {
	if x == "" {
		return v
	}
	out := v[:0]
	for _, s := range v {
		if s != x {
			out = append(out, s)
		}
	}
	return out
}

// SetPolicy adds or replaces the rule for an action + scope.
func (p *Project) SetPolicy(r ApprovalRule, now time.Time, by Actor) ([]Event, error) {
	if r.Action == "" || len(r.Approvers) == 0 {
		return nil, Invalid("an approval rule needs an action and approvers")
	}
	for i, o := range p.Policies {
		if o.Action == r.Action && o.Scope == r.Scope {
			p.Policies[i] = r
			return []Event{p.ev("project.policy_set", now, by, "action", r.Action)}, nil
		}
	}
	p.Policies = append(p.Policies, r)
	return []Event{p.ev("project.policy_set", now, by, "action", r.Action)}, nil
}

// ---- rules (pure) ----

func (p Project) Open() bool { return p.Status == Active || p.Status == Proposed || p.Status == Paused }

// ActiveMembers: memberships in force at now.
func (p Project) ActiveMembers(now time.Time) []Membership {
	var out []Membership
	for _, m := range p.Members {
		if m.Period.Contains(now) {
			out = append(out, m)
		}
	}
	return out
}

// Holding returns who holds a role at now.
func (p Project) Holding(role string, now time.Time) []WorkerID {
	var out []WorkerID
	for _, m := range p.ActiveMembers(now) {
		if strings.EqualFold(m.Role, role) {
			out = append(out, m.Worker)
		}
	}
	return out
}

// RolesOf lists a worker's active memberships.
func (p Project) RolesOf(w WorkerID, now time.Time) []Membership {
	var out []Membership
	for _, m := range p.ActiveMembers(now) {
		if m.Worker == w {
			out = append(out, m)
		}
	}
	return out
}

// Elapsed is the planned share of the timebox that has passed (0..1), or -1
// without a full timebox.
func (p Project) Elapsed(now time.Time) float64 {
	if p.Timebox.From == nil || p.Timebox.Until == nil {
		return -1
	}
	total := p.Timebox.Until.Sub(*p.Timebox.From)
	done := now.Sub(*p.Timebox.From)
	switch {
	case done <= 0:
		return 0
	case done >= total:
		return 1
	}
	return float64(done) / float64(total)
}
