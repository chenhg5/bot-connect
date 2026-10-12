package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/inbox"
	"github.com/chenhg5/bot-connect/internal/domain/insight"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
	"github.com/chenhg5/bot-connect/internal/domain/world"
)

// Style is how the principal wants things run. Hard limits are enforced
// here; the rest is shown to the brain so its recommendations fit.
type Style struct {
	Risk        string   // cautious | balanced | bold
	AskBefore   []string // what the principal wants to be asked about first
	BudgetMonth float64  // money the bot may spend a month (0: ask every time)
	Parallel    int      // agent assignments plans may run at once (default 4)
}

func (s Style) parallel() int {
	if s.Parallel <= 0 {
		return 4
	}
	return s.Parallel
}

// Describe is the style in one line for the brain.
func (s Style) Describe() string {
	risk := map[string]string{
		"cautious": "谨慎：先验证再投入，优先低方差的路径，拿不准先问",
		"bold":     "激进：接受高方差换更大的收益，先做再说，但要有止损条件",
	}[s.Risk]
	if risk == "" {
		risk = "平衡：可逆的事直接做，不可逆或花钱的事先问"
	}
	parts := []string{"风险偏好 " + risk}
	if len(s.AskBefore) > 0 {
		parts = append(parts, "这些事先问："+strings.Join(s.AskBefore, "、"))
	}
	if s.BudgetMonth > 0 {
		parts = append(parts, fmt.Sprintf("每月可自主花费 %.0f", s.BudgetMonth))
	} else {
		parts = append(parts, "花钱都要先问")
	}
	parts = append(parts, fmt.Sprintf("计划最多同时跑 %d 个 agent 任务", s.parallel()))
	return strings.Join(parts, "；")
}

// PlanReviewer gives an outside look at a plan before it is proposed: a few
// narrow questions, answered with probabilities. Its notes go back to the
// brain; they don't block anything.
type PlanReviewer interface {
	ReviewPlan(ctx context.Context, v PlanView) []string
}

// PlanView is a plan as the reviewer sees it.
type PlanView struct {
	Project     string
	Objective   string
	Kind        portfolio.GoalKind
	Feasibility portfolio.Feasibility
	Assumptions []string
	StopIf      []string
	Questions   []string
	Style       string
	Nodes       []NodeView
}

type NodeView struct {
	ID, Title, Executor, ExecutorKind, Why, Output string
	DependsOn                                      []string
	Compounding                                    bool
}

// NodeSpec is one node of a plan as the brain writes it. Nodes refer to each
// other by Ref (new nodes) or ID (existing items).
type NodeSpec struct {
	ID          ItemID
	Ref         string
	Title       string
	Done        []string
	Executor    string // worker id or name; "" = to be decided
	Why         NeedKind
	Output      planning.Output
	DependsOn   []string
	Estimate    time.Duration
	Due         *time.Time
	Brief       string
	Compounding bool
	Routine     string
	Milestone   bool
}

// PlanDraft is a new revision of a project's plan.
type PlanDraft struct {
	Project ProjectID
	Spec    portfolio.PlanSpec
	Nodes   []NodeSpec
	Replace bool      // open nodes not listed are dropped
	Since   time.Time // when the message this turn handles came in (exploring: a round tried before it needs recording first)
}

// PlanResult says what drafting did and what the plan looks like now.
type PlanResult struct {
	Project  portfolio.Project
	Refs     map[string]ItemID
	Dropped  []ItemID
	Analysis planning.Analysis
	Notes    []string // the reviewer's, on propose
	Asked    []AssignmentID
	Picked   []string // executors bot-connect chose for exploratory nodes
}

// DraftPlan writes a revision of a project's plan: nodes are created or
// updated, structure is checked, and a plan that was agreed goes back to
// draft. Errors in the structure reject the whole revision.
func (a *App) DraftPlan(ctx context.Context, by Actor, d PlanDraft) (PlanResult, error) {
	if err := requirePrivileged(by, "plan work"); err != nil {
		return PlanResult{}, err
	}
	var picked []string
	for i, n := range d.Nodes {
		if n.Executor != "" || !n.Output.Exploratory() {
			continue
		}
		// research and drafts don't wait on anyone: give them the best-fitting agent
		for _, c := range a.Candidates(ctx, insight.Need{Project: d.Project, Text: n.Title + " " + n.Brief, By: by}) {
			if c.Agent && c.Excluded == "" {
				d.Nodes[i].Executor = string(c.Worker)
				picked = append(picked, fmt.Sprintf("%s→%s", firstNonEmpty(n.Ref, n.Title), c.Worker))
				break
			}
		}
	}
	execs := map[string]WorkerID{}
	for _, n := range d.Nodes {
		if n.Executor == "" {
			continue
		}
		id, err := a.ResolveWorker(n.Executor)
		if err != nil {
			return PlanResult{}, err
		}
		execs[n.Executor] = id
	}
	now := a.now()
	res := PlanResult{Refs: map[string]ItemID{}, Picked: picked}
	var cancelled []delegation.Assignment
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		p, ok := s.Projects[d.Project]
		if !ok {
			return nil, NotFound("no project %s", d.Project)
		}
		if !p.Open() {
			return nil, Conflict("project %s is %s", p.ID, p.Status)
		}
		var evs []Event
		if p.Plan.Kind == portfolio.KindExplore && p.Plan.Stage == portfolio.PlanActive && !d.Since.IsZero() && roundTried(s, p, d.Since) {
			return nil, Conflict("this round's probes went out and nothing was recorded since: first plan learn (what the principal said about what they saw, " +
				"and continue / pivot / stop / converge), then draft the next round")
		}
		ids := make([]ItemID, len(d.Nodes))
		for i, n := range d.Nodes {
			switch {
			case n.ID != "" && s.Items[n.ID].Project == d.Project:
				ids[i] = n.ID
			case n.ID != "" && n.Ref == "":
				d.Nodes[i].Ref, d.Nodes[i].ID = string(n.ID), "" // not an item here: the brain meant a ref
				n = d.Nodes[i]
				ids[i] = ItemID(s.NextID("I"))
			case n.Ref != "" && res.Refs[n.Ref] != "":
				return nil, Invalid("ref %q used twice", n.Ref)
			default:
				ids[i] = ItemID(s.NextID("I"))
			}
			if n.Ref != "" {
				res.Refs[n.Ref] = ids[i]
			}
		}
		listed := map[ItemID]bool{}
		for i, n := range d.Nodes {
			id := ids[i]
			listed[id] = true
			var deps []ItemID
			for _, x := range n.DependsOn {
				dep, ok := res.Refs[x]
				if !ok {
					dep = ItemID(x)
					if _, exists := s.Items[dep]; !exists {
						return nil, NotFound("node %s depends on %q: no such ref or item", firstNonEmpty(n.Ref, string(id)), x)
					}
				}
				deps = append(deps, dep)
			}
			node := planning.Node{Executor: execs[n.Executor], Why: n.Why, Output: n.Output, Brief: n.Brief, Compounding: n.Compounding, Routine: n.Routine,
				Round: max(p.Plan.Round, 1)}
			if cur, ok := s.Items[id]; ok {
				if !cur.Open() {
					continue // done and dropped nodes stay as they are
				}
				node.Confirm = cur.Node.Confirm
				if node.Executor != cur.Node.Executor {
					node.Confirm = ""
				}
				if n.Title != "" {
					cur.Title = n.Title
				}
				if n.Done != nil {
					cur.Acceptance = nil
					for _, c := range n.Done {
						if c = strings.TrimSpace(c); c != "" {
							cur.Acceptance = append(cur.Acceptance, planning.Criterion{Text: c})
						}
					}
				}
				if n.Estimate > 0 {
					cur.Estimate = n.Estimate
				}
				if n.Due != nil && cur.Assignment == "" {
					cur.Due = n.Due
				}
				cur.DependsOn, cur.Node, cur.UpdatedAt = deps, node, now
				cur.Version++
				s.Items[id] = cur
				continue
			}
			kind := planning.Task
			if n.Milestone {
				kind = planning.Milestone
			}
			it, e, err := planning.New(id, planning.Spec{Project: d.Project, Kind: kind, Title: n.Title, Acceptance: n.Done, Due: n.Due,
				Estimate: n.Estimate, DependsOn: deps, Node: node}, now, by)
			if err != nil {
				return nil, fmt.Errorf("node %s: %w", firstNonEmpty(n.Ref, string(id)), err)
			}
			s.Items[id] = it
			evs = append(evs, e...)
		}
		g := planning.Graph{Items: s.Items}
		for _, id := range ids {
			it := s.Items[id]
			if err := g.Validate(id, it.Project, it.Parent, it.DependsOn); err != nil {
				return nil, fmt.Errorf("%s: %w", id, err)
			}
		}
		if d.Replace {
			for id, it := range planning.Nodes(s.Items, d.Project) {
				if listed[id] {
					continue
				}
				if as, ok := s.Assignments[it.Assignment]; ok && as.Open() {
					e, err := as.Cancel("计划调整，这一项不做了", now, by)
					if err != nil {
						return nil, err
					}
					s.Assignments[as.ID] = as
					cancelled = append(cancelled, as)
					evs = append(evs, e...)
				}
				e, err := it.Drop("计划调整", now, by)
				if err != nil {
					return nil, err
				}
				s.Items[id] = it
				res.Dropped = append(res.Dropped, id)
				evs = append(evs, e...)
			}
		}
		evs = append(evs, p.Draft(d.Spec, now, by)...)
		s.Projects[p.ID] = p
		res.Analysis = analyze(s, p.ID, now)
		if len(planning.Nodes(s.Items, p.ID)) == 0 && p.Plan.Stage != portfolio.PlanDone {
			return nil, Invalid("the plan has no nodes, so nothing will happen: pass nodes — each thing to do now, with executor and output " +
				"(for exploring: this round's probes, e.g. {ref:a, title:方向 A 的原型, executor:<agent>, output:draft})")
		}
		if errs := res.Analysis.Errors(); len(errs) > 0 {
			var b strings.Builder
			b.WriteString("the plan has problems; nothing was saved:")
			for _, e := range errs {
				b.WriteString("\n" + e.String())
			}
			return nil, Invalid("%s", b.String())
		}
		res.Project = p
		return evs, nil
	})
	if err != nil {
		return PlanResult{}, err
	}
	for _, as := range cancelled {
		a.notify(ctx, as, Notice{Kind: "cancelled", Text: "计划调整，这一项不做了"})
	}
	a.advancePlans(ctx)
	return res, nil
}

// analyze checks a project's plan against a snapshot of state.
func analyze(s *State, pid ProjectID, now time.Time) planning.Analysis {
	w := world.New(now)
	w.Workers, w.Items, w.Assignments = s.Workers, s.Items, s.Assignments
	pace := insight.PaceFrom(w)
	who := func(id WorkerID) (bool, bool) {
		wk, ok := s.Workers[id]
		return wk.Kind.Agentic(), ok
	}
	return planning.Analyze(s.Items, pid, who, func(it planning.Item) time.Duration {
		if it.Assignee == "" && it.Node.Executor != "" {
			it.Assignee = it.Node.Executor // forecast with who is meant to do it
		}
		return insight.Remaining(w, it, pace)
	})
}

// PlanStatus is the plan and its analysis now.
func (a *App) PlanStatus(pid ProjectID) (portfolio.Project, planning.Analysis, bool) {
	var p portfolio.Project
	var an planning.Analysis
	var ok bool
	a.Store.Read(func(s *State) {
		p, ok = s.Projects[pid]
		if ok {
			an = analyze(s, pid, a.now())
		}
	})
	return p, an, ok
}

// ProposePlan puts a draft to the people in it: everyone (other than the
// principal) with a node is asked to confirm their part, and the reviewer
// takes an outside look. The principal's agreement is recorded with
// AgreePlan when they give it.
func (a *App) ProposePlan(ctx context.Context, by Actor, pid ProjectID) (PlanResult, error) {
	if err := requirePrivileged(by, "propose a plan"); err != nil {
		return PlanResult{}, err
	}
	p, an, ok := a.PlanStatus(pid)
	if !ok {
		return PlanResult{}, NotFound("no project %s", pid)
	}
	if errs := an.Errors(); len(errs) > 0 {
		return PlanResult{}, Invalid("fix the plan first: %v", errs)
	}
	res := PlanResult{Project: p, Analysis: an}
	if a.Reviewer != nil {
		res.Notes = a.Reviewer.ReviewPlan(ctx, a.planView(ctx, pid))
	}
	if _, err := a.ChangeProject(ctx, by, pid, func(p *portfolio.Project, now time.Time) ([]Event, error) {
		return p.Propose(res.Notes, now, by)
	}); err != nil {
		return PlanResult{}, err
	}
	w := a.World(ctx)
	nodes := planning.Nodes(w.Items, pid)
	ids := make([]ItemID, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		it := nodes[id]
		ex := it.Node.Executor
		wk := w.Workers[ex]
		if ex == "" || ex == Principal || wk.Kind != workforce.Human {
			continue
		}
		if cur, ok := w.Assignments[it.Node.Confirm]; ok && (cur.Open() || cur.Choice == "approve") {
			continue // asked already, or already confirmed this part
		}
		when := ""
		if it.Due != nil {
			when = "，截止 " + fmtDue(it.Due)
		}
		as, err := a.Delegate(ctx, by, DelegateSpec{Item: id, Worker: ex, Kind: delegation.Review, Why: it.Node.Why,
			Options: []string{"approve", "change"},
			Brief: delegation.Brief{Goal: fmt.Sprintf("确认计划里你的部分：「%s」%s", it.Title, when),
				Context: "这是「" + p.Title + "」计划的一部分。可以的话回复 approve；时间、范围或方式要调整就回复 change 并说明。" + it.Node.Brief}})
		if err != nil {
			res.Analysis.Issues = append(res.Analysis.Issues, planning.Issue{Item: id, Error: true, Text: "没能请 " + string(ex) + " 确认：" + err.Error()})
			continue
		}
		res.Asked = append(res.Asked, as.ID)
		_, _ = a.commit(ctx, func(s *State) ([]Event, error) {
			x := s.Items[id]
			x.Node.Confirm = as.ID
			s.Items[id] = x
			return nil, nil
		})
	}
	res.Project, _, _ = a.PlanStatus(pid)
	a.advancePlans(ctx)
	return res, nil
}

// AgreePlan records the principal's agreement; the plan becomes active once
// everyone in it has confirmed.
func (a *App) AgreePlan(ctx context.Context, by Actor, pid ProjectID) (portfolio.Project, error) {
	if by.Role != RolePrincipal {
		return portfolio.Project{}, Forbidden("only the principal agrees to a plan")
	}
	p, err := a.ChangeProject(ctx, by, pid, func(p *portfolio.Project, now time.Time) ([]Event, error) {
		return p.PrincipalAgrees(now, by)
	})
	if err != nil {
		return p, err
	}
	a.advancePlans(ctx)
	p, _, _ = a.PlanStatus(pid)
	return p, nil
}

// PlanWaitingOn lists who still has to confirm a proposed plan.
func (a *App) PlanWaitingOn(pid ProjectID) []string {
	var out []string
	a.Store.Read(func(s *State) {
		p := s.Projects[pid]
		if p.Plan.Stage != portfolio.PlanProposed {
			return
		}
		if !p.Plan.PrincipalOK {
			out = append(out, string(Principal))
		}
		for _, it := range planning.Nodes(s.Items, pid) {
			if c, ok := confirmOf(s, it); ok && c.Choice != "approve" {
				out = append(out, fmt.Sprintf("%s（%s，%s）", c.Worker, it.ID, c.Status))
			}
		}
	})
	sort.Strings(out)
	return out
}

// confirmOf is the confirmation a person node needs (ok=false: none needed).
func confirmOf(s *State, it planning.Item) (delegation.Assignment, bool) {
	ex := it.Node.Executor
	if ex == "" || ex == Principal || s.Workers[ex].Kind != workforce.Human {
		return delegation.Assignment{}, false
	}
	c, ok := s.Assignments[it.Node.Confirm]
	if !ok {
		return delegation.Assignment{Worker: ex, Status: "not asked"}, true
	}
	return c, true
}

// advancePlans is the plans' part of the control loop: agree plans everyone
// has confirmed, hand out ready nodes (exploratory work at any stage,
// everything else once agreed), and close finished plans.
func (a *App) advancePlans(ctx context.Context) {
	var agree, finish []ProjectID
	var dispatch []ItemID
	running := 0
	a.Store.Read(func(s *State) {
		for _, as := range s.Assignments {
			if as.Open() && s.Workers[as.Worker].Kind.Agentic() && as.Status != delegation.Delivered {
				running++
			}
		}
		pids := make([]ProjectID, 0, len(s.Projects))
		for id := range s.Projects {
			pids = append(pids, id)
		}
		sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
		for _, pid := range pids {
			p := s.Projects[pid]
			st := p.Plan.Stage
			if st == portfolio.NoPlan || st == portfolio.PlanDone || !p.Open() || p.Status == portfolio.Paused {
				continue
			}
			nodes := planning.Nodes(s.Items, pid)
			if st == portfolio.PlanProposed && p.Plan.PrincipalOK {
				all := true
				for _, it := range nodes {
					if c, ok := confirmOf(s, it); ok && c.Choice != "approve" {
						all = false
					}
				}
				if all {
					agree = append(agree, pid)
					st = portfolio.PlanActive
				}
			}
			if st == portfolio.PlanActive && len(nodes) == 0 {
				finish = append(finish, pid)
				continue
			}
			ids := make([]ItemID, 0, len(nodes))
			for id := range nodes {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			for _, id := range ids {
				it := nodes[id]
				if it.Assignment != "" || it.Node.Executor == "" || it.Kind == planning.Milestone || !(planning.Graph{Items: s.Items}).Ready(id) {
					continue
				}
				if !(it.Node.Output.Exploratory() && s.Workers[it.Node.Executor].Kind.Agentic()) && st != portfolio.PlanActive {
					continue
				}
				dispatch = append(dispatch, id)
			}
		}
	})
	for _, pid := range agree {
		if _, err := a.ChangeProject(ctx, System("plan"), pid, func(p *portfolio.Project, now time.Time) ([]Event, error) {
			return p.Agree(now, System("plan"))
		}); err == nil {
			a.tellPrincipal(ctx, fmt.Sprintf("📋 %s 的计划大家都确认了，开始执行。", a.projectTitle(pid)))
		}
	}
	for _, id := range dispatch {
		if err := a.dispatchNode(ctx, id, &running); err != nil && a.Log != nil {
			a.Log.Info("plan node not handed out", "item", id, "err", err)
		}
	}
	for _, pid := range finish {
		_, _ = a.ChangeProject(ctx, System("plan"), pid, func(p *portfolio.Project, now time.Time) ([]Event, error) {
			return p.Finish(now, System("plan")), nil
		})
		_, _, _ = a.Ingest(ctx, SignalSpec{Dedupe: "plan_done:" + string(pid), Source: inbox.Risk, Reason: "plan_done",
			Actor: System("plan"), Summary: a.projectTitle(pid) + " 的计划全部完成", Body: a.projectTitle(pid) + " 的计划全部完成：总结结果，告诉委托人，想想有没有值得沉淀的工具或流程。",
			Refs: inbox.Refs{Project: pid}})
	}
}

// dispatchNode hands a ready node to its planned executor, with what the
// nodes before it produced.
func (a *App) dispatchNode(ctx context.Context, id ItemID, running *int) error {
	var it planning.Item
	var p portfolio.Project
	var context []string
	a.Store.Read(func(s *State) {
		it = s.Items[id]
		p = s.Projects[it.Project]
		for _, d := range it.DependsOn {
			dep := s.Items[d]
			for _, as := range s.Assignments {
				if as.Item == d && as.Status == delegation.Verified && as.Result != "" {
					context = append(context, fmt.Sprintf("「%s」的结果：%s", dep.Title, clipRunes(as.Result, 4000)))
				}
			}
		}
	})
	agentic := a.isAgent(it.Node.Executor)
	if agentic {
		if *running >= a.Style.parallel() {
			return nil // next tick
		}
	}
	kind := map[planning.Output]delegation.AskKind{planning.OutDecision: delegation.Decision, planning.OutApproval: delegation.Approval,
		planning.OutAction: delegation.Action}[it.Node.Output]
	ctxText := strings.TrimSpace(it.Node.Brief + "\n" + strings.Join(context, "\n"))
	if p.Objective.Text != "" {
		ctxText = "项目目标：" + p.Objective.Text + "\n" + ctxText
	}
	by := p.Plan.By
	by.Via = "plan"
	_, err := a.Delegate(ctx, by, DelegateSpec{Item: id, Worker: it.Node.Executor, Kind: kind, Why: it.Node.Why,
		Brief: delegation.Brief{Context: ctxText}})
	if err != nil {
		_, _, _ = a.Ingest(ctx, SignalSpec{Dedupe: "plan_stuck:" + string(id) + ":" + string(it.Node.Executor), Source: inbox.Risk, Reason: "plan_stuck",
			Actor: System("plan"), Summary: fmt.Sprintf("计划节点 %s「%s」交不出去", id, oneLine(it.Title, 40)),
			Body: fmt.Sprintf("计划节点 %s「%s」该交给 %s 了，但交不出去：%v。换人、调整计划，或者解决这个问题。", id, it.Title, it.Node.Executor, err),
			Refs: inbox.Refs{Project: it.Project, Item: id}})
		return err
	}
	if agentic {
		*running++
	}
	return nil
}

func (a *App) projectTitle(pid ProjectID) string {
	var t string
	a.Store.Read(func(s *State) { t = s.Projects[pid].Title })
	return firstNonEmpty(t, string(pid))
}

func (a *App) planView(ctx context.Context, pid ProjectID) PlanView {
	w := a.World(ctx)
	p := w.Projects[pid]
	v := PlanView{Project: p.Title, Objective: p.Objective.Text, Kind: p.Plan.Kind, Feasibility: p.Plan.Feasibility,
		Assumptions: p.Plan.Assumptions, StopIf: p.Plan.StopIf, Questions: p.Plan.Questions, Style: a.Style.Describe()}
	nodes := planning.Nodes(w.Items, pid)
	ids := make([]ItemID, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		it := nodes[id]
		kind := "待定"
		if wk, ok := w.Workers[it.Node.Executor]; ok {
			kind = map[bool]string{true: "agent", false: "人"}[wk.Kind.Agentic()]
		}
		var deps []string
		for _, d := range it.DependsOn {
			deps = append(deps, string(d))
		}
		v.Nodes = append(v.Nodes, NodeView{ID: string(id), Title: it.Title, Executor: string(it.Node.Executor), ExecutorKind: kind,
			Why: string(it.Node.Why), Output: string(it.Node.Output), DependsOn: deps, Compounding: it.Node.Compounding})
	}
	return v
}

// DescribePlan renders a plan for people: stage, feasibility, layers that
// can run in parallel, the critical path, and what is wrong.
func (a *App) DescribePlan(pid ProjectID) string {
	p, an, ok := a.PlanStatus(pid)
	if !ok {
		return ""
	}
	var items map[ItemID]planning.Item
	a.Store.Read(func(s *State) {
		items = map[ItemID]planning.Item{}
		for k, v := range s.Items {
			items[k] = v
		}
	})
	var b strings.Builder
	stage := map[portfolio.Stage]string{portfolio.PlanDraft: "草案", portfolio.PlanProposed: "待确认", portfolio.PlanActive: "执行中", portfolio.PlanDone: "已完成"}[p.Plan.Stage]
	if p.Plan.Kind == portfolio.KindExplore && p.Plan.Stage == portfolio.PlanActive {
		stage = "探索中"
	}
	fmt.Fprintf(&b, "%s「%s」计划 rev%d：%s（%s）\n", p.ID, p.Title, p.Plan.Revision, firstNonEmpty(stage, "无"), p.Plan.Kind)
	if f := p.Plan.Feasibility; f.Verdict != "" {
		fmt.Fprintf(&b, "可行性：%s %s\n", f.Verdict, f.Note)
	}
	if len(p.Plan.Assumptions) > 0 {
		b.WriteString("假设：" + strings.Join(p.Plan.Assumptions, "；") + "\n")
	}
	if len(p.Plan.StopIf) > 0 {
		b.WriteString("止损：" + strings.Join(p.Plan.StopIf, "；") + "\n")
	}
	if p.Plan.Kind == portfolio.KindExplore {
		fmt.Fprintf(&b, "探索第 %d 轮", p.Plan.Round)
		if !p.Plan.LastReview.IsZero() {
			b.WriteString("，上次和委托人对齐 " + p.Plan.LastReview.Format("01-02 15:04"))
		}
		if p.Timebox.Until != nil {
			b.WriteString("，时间盒到 " + p.Timebox.Until.Format("01-02"))
		}
		b.WriteString("\n")
		if len(p.Plan.Questions) > 0 {
			b.WriteString("还没答案的问题：" + strings.Join(p.Plan.Questions, "；") + "\n")
		}
		for _, l := range p.Plan.Learned {
			fmt.Fprintf(&b, "学到（第 %d 轮，%s）：%s\n", l.Round, firstNonEmpty(l.Choice, "continue"), l.Text)
		}
	}
	for i, layer := range an.Layers {
		fmt.Fprintf(&b, "第 %d 批（可并行）：\n", i+1)
		for _, id := range layer {
			it := items[id]
			who := firstNonEmpty(string(it.Node.Executor), "待定")
			if it.Node.Why != "" {
				who += "·" + string(it.Node.Why)
			}
			line := fmt.Sprintf("  - %s「%s」→ %s", id, it.Title, who)
			if it.Node.Output != "" {
				line += " [" + string(it.Node.Output) + "]"
			}
			if it.Node.Compounding {
				line += " ♻复利"
			}
			if len(it.DependsOn) > 0 {
				line += fmt.Sprintf(" 依赖 %v", it.DependsOn)
			}
			if it.Assignment != "" {
				line += " · " + string(it.Assignment)
			}
			b.WriteString(line + "\n")
		}
	}
	if len(an.Critical) > 0 {
		fmt.Fprintf(&b, "关键路径：%v，约 %s；总工作量 %s\n", an.Critical, humanDur(an.Span), humanDur(an.Effort))
	}
	if wait := a.PlanWaitingOn(pid); len(wait) > 0 {
		b.WriteString("等待确认：" + strings.Join(wait, "、") + "\n")
	}
	for _, i := range an.Issues {
		b.WriteString(i.String() + "\n")
	}
	for _, n := range p.Plan.Notes {
		b.WriteString("审视：" + n + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func humanDur(d time.Duration) string {
	switch {
	case d <= 0:
		return "未估"
	case d >= 24*time.Hour:
		return fmt.Sprintf("%.1f 天", d.Hours()/24)
	}
	return fmt.Sprintf("%.0f 小时", d.Hours())
}

// RoutineHint: repeated work in a project with nothing built to make it
// cheaper (empty when there is none).
func (a *App) RoutineHint(pid ProjectID) string {
	done, invested := map[string]int{}, map[string]bool{}
	a.Store.Read(func(s *State) {
		for _, it := range s.Items {
			rt := it.Node.Routine
			if it.Project != pid || rt == "" || it.Status == planning.Dropped {
				continue
			}
			if it.Node.Compounding {
				invested[rt] = true
			} else if it.Status == planning.Done {
				done[rt]++
			}
		}
	})
	var out []string
	for rt, n := range done {
		if n >= 3 && !invested[rt] {
			out = append(out, fmt.Sprintf("「%s」这类工作已经做了 %d 次，还没有沉淀成工具 / skill / 专门的 worker", rt, n))
		}
	}
	sort.Strings(out)
	return strings.Join(out, "；")
}

// StartedNodes lists a plan's nodes already handed out: "I1→researcher（A1）".
func (a *App) StartedNodes(pid ProjectID) []string {
	var out []string
	a.Store.Read(func(s *State) {
		for id, it := range planning.Nodes(s.Items, pid) {
			if as, ok := s.Assignments[it.Assignment]; ok && as.Open() {
				out = append(out, fmt.Sprintf("%s「%s」→%s（%s）", id, oneLine(it.Title, 30), as.Worker, as.ID))
			}
		}
	})
	sort.Strings(out)
	return out
}

// LearningChecker looks at what a round of exploration concluded against
// what earlier rounds did: going back to a direction already dropped, or
// contradicting an earlier conclusion without saying why.
type LearningChecker interface {
	CheckLearning(ctx context.Context, goal string, prior []string, now string) []string
}

// LearnPlan records a round of exploration: what it settled, the
// principal's direction, and which open questions are answered. Notes from
// the checker come back so the brain can address them.
func (a *App) LearnPlan(ctx context.Context, by Actor, pid ProjectID, text, choice string, resolved []string) (portfolio.Project, []string, error) {
	if err := requirePrivileged(by, "record what exploring taught"); err != nil {
		return portfolio.Project{}, nil, err
	}
	var notes []string
	if a.Learnings != nil {
		if p, _, ok := a.PlanStatus(pid); ok {
			var prior []string
			for _, l := range p.Plan.Learned {
				prior = append(prior, fmt.Sprintf("第 %d 轮：%s（%s）", l.Round, l.Text, firstNonEmpty(l.Choice, "continue")))
			}
			if len(prior) > 0 {
				notes = a.Learnings.CheckLearning(ctx, firstNonEmpty(p.Objective.Text, p.Title), prior, text)
			}
		}
	}
	p, err := a.ChangeProject(ctx, by, pid, func(p *portfolio.Project, now time.Time) ([]Event, error) {
		return p.Learn(text, choice, resolved, now, by)
	})
	if err != nil {
		return p, nil, err
	}
	if choice == "pivot" || choice == "stop" {
		// the old direction is abandoned: its unfinished probes stop too
		var drop []ItemID
		a.Store.Read(func(s *State) {
			for id, it := range planning.Nodes(s.Items, pid) {
				if it.Node.Round < p.Plan.Round {
					drop = append(drop, id)
				}
			}
		})
		sort.Slice(drop, func(i, j int) bool { return drop[i] < drop[j] })
		for _, id := range drop {
			if err := a.DropItem(ctx, by, id, "探索转向，这个方向不做了"); err == nil {
				notes = append(notes, fmt.Sprintf("已停掉旧方向的 %s", id))
			}
		}
	}
	if len(notes) > 0 {
		_, _ = a.ChangeProject(ctx, by, pid, func(p *portfolio.Project, now time.Time) ([]Event, error) {
			p.Plan.Notes = append(p.Plan.Notes, notes...)
			return nil, nil
		})
	}
	a.advancePlans(ctx)
	p, _, _ = a.PlanStatus(pid)
	return p, notes, nil
}

// roundTried: the current round of an exploration has nodes handed out
// since the principal's last reaction was recorded.
func roundTried(s *State, p portfolio.Project, before time.Time) bool {
	for _, it := range s.Items {
		if it.Project == p.ID && !it.Ask && it.Node.Round == p.Plan.Round && it.Assignment != "" &&
			it.CreatedAt.After(p.Plan.LastReview) && it.CreatedAt.Before(before) {
			return true
		}
	}
	return false
}
