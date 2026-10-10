package insight

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
	"github.com/chenhg5/bot-connect/internal/domain/world"
)

// Need describes what someone is needed for.
type Need struct {
	Project    ProjectID
	Text       string                // “定埋点方案”, “review the PR touching media”
	Kind       NeedKind              // "" = ordinary work
	Capability *workforce.Capability // NeedCapability: e.g. gcloud-logging:read
	Action     string                // NeedApproval: e.g. merge:main
	Scope      string                // NeedApproval: e.g. repo:tapnow
	Item       ItemID                // continuing work: prefer whoever has context
	By         Actor                 // who will hand the work over (consent)
}

// Candidate is one worker with a score and the reasons for it.
type Candidate struct {
	Worker   WorkerID `json:"worker"`
	Agent    bool     `json:"agent"`
	Score    float64  `json:"score"`
	Reasons  []string `json:"reasons"`
	Excluded string   `json:"excluded,omitempty"` // why it can't be used now
}

// Matcher proposes candidates with partial scores. Matchers are pluggable
// (literal roles, skills, capabilities, authority, continuity, a semantic
// judge…); their scores add up.
type Matcher interface {
	Name() string
	Match(n Need, w *world.World) []Candidate
}

// Filter excludes candidates that can't be used now, saying why.
type Filter interface {
	Name() string
	Exclude(c Candidate, n Need, w *world.World) string
}

// Staffing turns a need into ranked candidates. Rules it enforces:
//   - agents first: for work an agent can do, people rank below agents and
//     are marked as such (asking a person must have a reason);
//   - approvals go to whoever the rule resolves to (by role, at this time);
//   - physical actions only to people who can act in the real world;
//   - unavailable / non-consenting workers are listed as excluded, with why.
type Staffing struct {
	Matchers []Matcher
	Filters  []Filter
}

// DefaultStaffing is the built-in set.
func DefaultStaffing() Staffing {
	return Staffing{
		Matchers: []Matcher{RoleMatcher{}, SkillMatcher{}, CapabilityMatcher{}, AuthorityMatcher{}, PhysicalMatcher{}, ContinuityMatcher{}},
		Filters:  []Filter{AvailabilityFilter{}, ConsentFilter{}},
	}
}

func (s Staffing) Candidates(n Need, w *world.World) []Candidate {
	byID := map[WorkerID]*Candidate{}
	for _, m := range s.Matchers {
		for _, c := range m.Match(n, w) {
			cur := byID[c.Worker]
			if cur == nil {
				wk := w.Workers[c.Worker]
				cur = &Candidate{Worker: c.Worker, Agent: wk.Kind.Agentic()}
				byID[c.Worker] = cur
			}
			cur.Score += c.Score
			cur.Reasons = append(cur.Reasons, c.Reasons...)
		}
	}
	// Agents first: when an agent can do it, a person is only a fallback.
	agentCan := false
	if n.Kind == "" || n.Kind == NeedCapability {
		for _, c := range byID {
			if c.Agent && c.Score > 0 {
				agentCan = true
			}
		}
	}
	var out []Candidate
	for _, c := range byID {
		if c.Score <= 0 {
			continue
		}
		if agentCan && !c.Agent {
			c.Score *= 0.3
			c.Reasons = append(c.Reasons, "agent 也能做：只有 agent 做不了时才找人")
		}
		for _, f := range s.Filters {
			if why := f.Exclude(*c, n, w); why != "" {
				c.Excluded = why
				break
			}
		}
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Excluded == "") != (out[j].Excluded == "") {
			return out[i].Excluded == ""
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Worker < out[j].Worker
	})
	return out
}

// ---- matchers ----

// RoleMatcher: roles and duties in the project, then its ancestors (the
// company last), weighted by distance.
type RoleMatcher struct{}

func (RoleMatcher) Name() string { return "role" }
func (RoleMatcher) Match(n Need, w *world.World) []Candidate {
	if n.Text == "" {
		return nil
	}
	var out []Candidate
	for depth, p := range w.Chain(n.Project) {
		weight := 1.0 / float64(depth+1)
		for _, m := range p.ActiveMembers(w.Now) {
			if s := portfolio.DutyMatch(m, n.Text); s > 0 {
				where := "项目「" + p.Title + "」"
				if p.ID == OrgProject {
					where = "公司"
				}
				out = append(out, Candidate{Worker: m.Worker, Score: 3 * s * weight,
					Reasons: []string{fmt.Sprintf("%s的%s（%s）", where, m.Role, strings.Join(m.Duties, "、"))}})
			}
		}
	}
	return out
}

// SkillMatcher: declared skills.
type SkillMatcher struct{}

func (SkillMatcher) Name() string { return "skill" }
func (SkillMatcher) Match(n Need, w *world.World) []Candidate {
	if n.Text == "" {
		return nil
	}
	var out []Candidate
	for id, wk := range w.Workers {
		for _, s := range wk.Skills {
			if s != "" && strings.Contains(strings.ToLower(n.Text), strings.ToLower(s)) {
				out = append(out, Candidate{Worker: id, Score: 1, Reasons: []string{"技能：" + s}})
				break
			}
		}
	}
	return out
}

// CapabilityMatcher: who has the system access the work needs.
type CapabilityMatcher struct{}

func (CapabilityMatcher) Name() string { return "capability" }
func (CapabilityMatcher) Match(n Need, w *world.World) []Candidate {
	if n.Capability == nil {
		return nil
	}
	var out []Candidate
	for id, wk := range w.Workers {
		if wk.Can(*n.Capability) {
			out = append(out, Candidate{Worker: id, Score: 4, Reasons: []string{"有权限 " + n.Capability.String()}})
		}
	}
	return out
}

// AuthorityMatcher: who may approve the action — by the project's rule
// (roles resolved at this time), and by personal authority.
type AuthorityMatcher struct{}

func (AuthorityMatcher) Name() string { return "authority" }
func (AuthorityMatcher) Match(n Need, w *world.World) []Candidate {
	if n.Kind != NeedApproval || n.Action == "" {
		return nil
	}
	var out []Candidate
	chain := w.Chain(n.Project)
	if r, pid, ok := chain.Rule(n.Action, n.Scope); ok {
		for _, id := range chain.Approvers(r, w.Now, w.IsWorker) {
			out = append(out, Candidate{Worker: id, Score: 6, Reasons: []string{fmt.Sprintf("按 %s 的审批规则 %s 由 %s 批准", pid, n.Action, strings.Join(r.Approvers, "/"))}})
		}
	}
	for id, wk := range w.Workers {
		if wk.MayApprove(n.Action, n.Scope) {
			out = append(out, Candidate{Worker: id, Score: 2, Reasons: []string{"有 " + n.Action + " 的批准权限"}})
		}
	}
	return out
}

// PhysicalMatcher: real-world actions need people who can do them.
type PhysicalMatcher struct{}

func (PhysicalMatcher) Name() string { return "physical" }
func (PhysicalMatcher) Match(n Need, w *world.World) []Candidate {
	if n.Kind != NeedPhysical {
		return nil
	}
	var out []Candidate
	for id, wk := range w.Workers {
		if wk.Physical {
			out = append(out, Candidate{Worker: id, Score: 1, Reasons: []string{"能做现实世界的动作"}})
		}
	}
	return out
}

// ContinuityMatcher: whoever already worked on this item (or its parent)
// has the context.
type ContinuityMatcher struct{}

func (ContinuityMatcher) Name() string { return "continuity" }
func (ContinuityMatcher) Match(n Need, w *world.World) []Candidate {
	if n.Item == "" {
		return nil
	}
	related := map[ItemID]bool{n.Item: true}
	if it, ok := w.Items[n.Item]; ok && it.Parent != "" {
		related[it.Parent] = true
	}
	seen := map[WorkerID]bool{}
	var out []Candidate
	for _, a := range w.Assignments {
		if related[a.Item] && !seen[a.Worker] && a.Status != delegation.Declined && a.Status != delegation.Expired {
			seen[a.Worker] = true
			out = append(out, Candidate{Worker: a.Worker, Score: 1.5, Reasons: []string{"做过这件事，有上下文"}})
		}
	}
	return out
}

// ---- filters ----

// AvailabilityFilter: hard facts say the worker can't take work now.
type AvailabilityFilter struct{}

func (AvailabilityFilter) Name() string { return "availability" }
func (AvailabilityFilter) Exclude(c Candidate, n Need, w *world.World) string {
	if ok, why := w.State(c.Worker).Available(w.Now); !ok {
		return "不可用：" + why
	}
	return ""
}

// ConsentFilter: people who haven't agreed to take work from this requester.
// Approvals and decisions are still possible: a rule names them.
type ConsentFilter struct{}

func (ConsentFilter) Name() string { return "consent" }
func (ConsentFilter) Exclude(c Candidate, n Need, w *world.World) string {
	if n.Kind == NeedApproval {
		return ""
	}
	wk := w.Workers[c.Worker]
	if !wk.AcceptsFrom(n.By, w.Now) {
		return "没有同意接 bot 派的活"
	}
	return ""
}
