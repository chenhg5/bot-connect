package portfolio

import (
	"strings"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// Chain is a project and its ancestors, nearest first, ending at the root.
// Rules that look "upwards" (approval policies, standing roles) use it.
type Chain []Project

// ChainOf builds the chain for id from a lookup.
func ChainOf(id ProjectID, get func(ProjectID) (Project, bool)) Chain {
	var c Chain
	seen := map[ProjectID]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		p, ok := get(id)
		if !ok {
			break
		}
		c = append(c, p)
		id = p.Parent
	}
	return c
}

// Rule returns the approval rule for an action in a scope: the nearest
// project that has one wins (a project can tighten or loosen the company's
// rule for itself).
func (c Chain) Rule(action, scope string) (ApprovalRule, ProjectID, bool) {
	for _, p := range c {
		var best *ApprovalRule
		for i, r := range p.Policies {
			if r.Action == action && ScopeMatches(r.Scope, scope) {
				if best == nil || len(r.Scope) > len(best.Scope) { // most specific scope
					best = &p.Policies[i]
				}
			}
		}
		if best != nil {
			return *best, p.ID, true
		}
	}
	return ApprovalRule{}, "", false
}

// Holding finds who holds a role at now, nearest project first.
func (c Chain) Holding(role string, now time.Time) ([]WorkerID, ProjectID) {
	for _, p := range c {
		if ws := p.Holding(role, now); len(ws) > 0 {
			return ws, p.ID
		}
	}
	return nil, ""
}

// Approvers resolves a rule's approvers to workers at now: role names
// through memberships (nearest project first), anything else as a worker id.
func (c Chain) Approvers(r ApprovalRule, now time.Time, isWorker func(WorkerID) bool) []WorkerID {
	seen := map[WorkerID]bool{}
	var out []WorkerID
	for _, a := range r.Approvers {
		if ws, _ := c.Holding(a, now); len(ws) > 0 {
			for _, w := range ws {
				if !seen[w] {
					seen[w] = true
					out = append(out, w)
				}
			}
			continue
		}
		if w := WorkerID(a); isWorker != nil && isWorker(w) && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// DutyMatch scores how well a membership's role and duties match a need
// text (0..1). This is the cheap literal matcher; semantic matching is a
// separate, pluggable judgment.
func DutyMatch(m Membership, need string) float64 {
	need = strings.ToLower(strings.TrimSpace(need))
	if need == "" {
		return 0
	}
	best := 0.0
	for _, d := range append([]string{m.Role}, m.Duties...) {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		switch {
		case d == need:
			return 1
		case strings.Contains(need, d) || strings.Contains(d, need):
			if best < 0.8 {
				best = 0.8
			}
		}
	}
	return best
}
