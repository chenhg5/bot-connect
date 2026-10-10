package planning

import (
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// Graph checks rules that span items: dependencies exist, form no cycle,
// and parents are in the same project.
type Graph struct {
	Items map[ItemID]Item
}

// Validate checks that item id may depend on deps and sit under parent.
func (g Graph) Validate(id ItemID, project ProjectID, parent ItemID, deps []ItemID) error {
	if parent != "" {
		p, ok := g.Items[parent]
		if !ok {
			return NotFound("no parent item %s", parent)
		}
		if p.Project != project {
			return Invalid("parent %s is in another project", parent)
		}
	}
	for _, d := range deps {
		if d == id {
			return Invalid("%s can't depend on itself", id)
		}
		if _, ok := g.Items[d]; !ok {
			return NotFound("no item %s to depend on", d)
		}
		if g.reaches(d, id, map[ItemID]bool{}) {
			return Invalid("depending on %s would create a cycle", d)
		}
	}
	return nil
}

// reaches reports whether from depends (transitively) on to.
func (g Graph) reaches(from, to ItemID, seen map[ItemID]bool) bool {
	if from == to {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	for _, d := range g.Items[from].DependsOn {
		if g.reaches(d, to, seen) {
			return true
		}
	}
	return false
}

// Dependents lists open items that depend directly on id.
func (g Graph) Dependents(id ItemID) []ItemID {
	var out []ItemID
	for _, it := range g.Items {
		if !it.Open() {
			continue
		}
		for _, d := range it.DependsOn {
			if d == id {
				out = append(out, it.ID)
				break
			}
		}
	}
	return out
}

// Downstream counts open items that depend on id, directly or transitively.
func (g Graph) Downstream(id ItemID) int {
	seen := map[ItemID]bool{}
	var walk func(ItemID)
	walk = func(x ItemID) {
		for _, d := range g.Dependents(x) {
			if !seen[d] {
				seen[d] = true
				walk(d)
			}
		}
	}
	walk(id)
	return len(seen)
}

// Ready reports whether all of an item's dependencies are done.
func (g Graph) Ready(id ItemID) bool {
	for _, d := range g.Items[id].DependsOn {
		if dep, ok := g.Items[d]; ok && dep.Status != Done && dep.Status != Dropped {
			return false
		}
	}
	return true
}

// Children lists direct children.
func (g Graph) Children(id ItemID) []Item {
	var out []Item
	for _, it := range g.Items {
		if it.Parent == id {
			out = append(out, it)
		}
	}
	return out
}
