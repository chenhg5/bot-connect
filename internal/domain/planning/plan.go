package planning

import (
	"fmt"
	"sort"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// Output is what a plan node produces. It decides who may do it and when:
// exploratory work (findings, drafts) is cheap and reversible, so it may
// start before the plan is agreed; decisions and approvals belong to people.
type Output string

const (
	OutFindings Output = "findings" // research: conclusions with evidence
	OutDraft    Output = "draft"    // a proposal, a document, a design to be decided on
	OutDecision Output = "decision" // someone chooses
	OutApproval Output = "approval" // someone allows
	OutChange   Output = "change"   // code, config, content changed for real
	OutAction   Output = "action"   // something done in the world
)

func ParseOutput(s string) (Output, error) {
	switch o := Output(s); o {
	case "", OutFindings, OutDraft, OutDecision, OutApproval, OutChange, OutAction:
		return o, nil
	}
	return "", Invalid("unknown output %q (findings, draft, decision, approval, change, action)", s)
}

// Exploratory work needs nobody's agreement to start.
func (o Output) Exploratory() bool { return o == OutFindings || o == OutDraft }

// Answered outputs are given by a person, not produced by an agent.
func (o Output) Answered() bool { return o == OutDecision || o == OutApproval }

// Node is an item's place in its project's plan: who is meant to do it (before
// anything is handed out), why a person, what it produces, and whether it is
// an investment that pays off on repeated work.
type Node struct {
	Executor    WorkerID     `json:"executor,omitempty"` // planned; "" = to be decided
	Why         NeedKind     `json:"why,omitempty"`      // why a person
	Output      Output       `json:"output,omitempty"`
	Brief       string       `json:"brief,omitempty"`       // instructions beyond title and acceptance
	Compounding bool         `json:"compounding,omitempty"` // a tool / skill / worker for repeated work
	Routine     string       `json:"routine,omitempty"`     // the kind of repeated work it belongs to ("release")
	Confirm     AssignmentID `json:"confirm,omitempty"`     // the executor's confirmation while the plan is proposed
	Round       int          `json:"round,omitempty"`       // exploring: the round it was tried in
}

// Who tells whether a worker exists and is an agent.
type Who func(WorkerID) (agentic, known bool)

// Issue found in a plan. Errors block proposing it; warnings are advice.
type Issue struct {
	Item  ItemID
	Error bool
	Text  string
}

func (i Issue) String() string {
	lv := "⚠"
	if i.Error {
		lv = "✗"
	}
	if i.Item == "" {
		return lv + " " + i.Text
	}
	return fmt.Sprintf("%s %s：%s", lv, i.Item, i.Text)
}

// Analysis of a project's plan: what can run in parallel, the critical
// path, and what is wrong with it.
type Analysis struct {
	Issues   []Issue
	Layers   [][]ItemID // open nodes by depth; a layer can run in parallel
	Critical []ItemID   // the longest chain of open work
	Span     time.Duration
	Effort   time.Duration
}

func (a Analysis) Errors() []Issue {
	var out []Issue
	for _, i := range a.Issues {
		if i.Error {
			out = append(out, i)
		}
	}
	return out
}

// Nodes returns the open plan nodes of a project: its items, without the
// asks hanging under them.
func Nodes(items map[ItemID]Item, project ProjectID) map[ItemID]Item {
	out := map[ItemID]Item{}
	for id, it := range items {
		if it.Project == project && it.Open() && !it.Ask {
			out[id] = it
		}
	}
	return out
}

// Analyze checks a project's plan. span is the calendar time an item still
// needs (pace and working hours applied).
func Analyze(items map[ItemID]Item, project ProjectID, who Who, span func(Item) time.Duration) Analysis {
	nodes := Nodes(items, project)
	var a Analysis
	add := func(id ItemID, err bool, f string, v ...any) {
		a.Issues = append(a.Issues, Issue{Item: id, Error: err, Text: fmt.Sprintf(f, v...)})
	}
	ids := make([]ItemID, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	routines, invested := map[string]bool{}, map[string]bool{}
	for _, it := range items {
		if it.Project == project && it.Status != Dropped && it.Node.Compounding {
			invested[it.Node.Routine] = true
		}
	}
	for _, id := range ids {
		it := nodes[id]
		n := it.Node
		if it.Kind == Milestone {
			continue
		}
		if n.Routine != "" {
			routines[n.Routine] = true
		}
		if n.Executor == "" {
			if !waitsOnDecision(items, it, map[ItemID]bool{}) {
				add(id, false, "执行者待定：要么现在定，要么让它依赖于决定它的那个节点")
			}
			continue
		}
		agentic, known := who(n.Executor)
		switch {
		case !known:
			add(id, true, "没有 worker %q", n.Executor)
		case !agentic && n.Why == "":
			add(id, true, "交给人（%s）要写理由 why：approval / decision / physical / relationship / judgment / knowledge / capability", n.Executor)
		case !agentic && n.Output.Exploratory() && n.Why != NeedKnowledge && n.Why != NeedJudgment && n.Why != NeedRelationship:
			add(id, false, "调研、起草是 agent 能做的事；只在需要 %s 的知识或判断时才找他", n.Executor)
		case agentic && n.Output.Answered():
			add(id, true, "决定、审批属于人，不能交给 agent %s", n.Executor)
		}
	}
	for r := range routines {
		if !invested[r] {
			add("", false, "「%s」是周期性工作：考虑加一个复利节点（compounding），把它做成工具 / skill / 专门的 worker", r)
		}
	}

	// layers: depth over open dependencies
	depth := map[ItemID]int{}
	var depthOf func(id ItemID, guard int) int
	depthOf = func(id ItemID, guard int) int {
		if d, ok := depth[id]; ok {
			return d
		}
		d := 0
		if guard < 64 {
			for _, dep := range nodes[id].DependsOn {
				if _, open := nodes[dep]; open {
					d = max(d, depthOf(dep, guard+1)+1)
				}
			}
		}
		depth[id] = d
		return d
	}
	for _, id := range ids {
		l := depthOf(id, 0)
		for len(a.Layers) <= l {
			a.Layers = append(a.Layers, nil)
		}
		a.Layers[l] = append(a.Layers[l], id)
	}

	// critical path
	finish, prev := map[ItemID]time.Duration{}, map[ItemID]ItemID{}
	var finishOf func(id ItemID, guard int) time.Duration
	finishOf = func(id ItemID, guard int) time.Duration {
		if f, ok := finish[id]; ok {
			return f
		}
		var start time.Duration
		if guard < 64 {
			for _, dep := range nodes[id].DependsOn {
				if _, open := nodes[dep]; open {
					if f := finishOf(dep, guard+1); f > start {
						start, prev[id] = f, dep
					}
				}
			}
		}
		f := start + span(nodes[id])
		finish[id] = f
		return f
	}
	var last ItemID
	for _, id := range ids {
		a.Effort += nodes[id].Estimate
		if f := finishOf(id, 0); f > a.Span || last == "" {
			a.Span, last = f, id
		}
	}
	for x := last; x != ""; x = prev[x] {
		a.Critical = append([]ItemID{x}, a.Critical...)
	}
	if len(ids) >= 3 && len(a.Layers) == len(ids) {
		add("", false, "所有节点都是串行的：检查哪些其实互不依赖，可以并行")
	}
	return a
}

// waitsOnDecision: the item depends (transitively) on an open decision, so
// who does it can wait for that decision.
func waitsOnDecision(items map[ItemID]Item, it Item, seen map[ItemID]bool) bool {
	for _, d := range it.DependsOn {
		dep, ok := items[d]
		if !ok || seen[d] || !dep.Open() {
			continue
		}
		seen[d] = true
		if dep.Node.Output.Answered() || waitsOnDecision(items, dep, seen) {
			return true
		}
	}
	return false
}
