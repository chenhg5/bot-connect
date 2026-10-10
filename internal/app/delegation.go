package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
	"github.com/chenhg5/bot-connect/internal/domain/world"
)

// DelegateSpec hands (part of) an item to a worker.
type DelegateSpec struct {
	Item    ItemID
	Worker  WorkerID
	Kind    delegation.AskKind // default work
	Why     NeedKind           // required for people
	Options []string           // decisions / approvals
	Brief   delegation.Brief   // empty fields are filled from the item
	Conv    string             // where results are reported (default: the project's home)
}

// Delegate hands work to a worker. For answered kinds (approval, decision,
// clarification, review) a child item is created for the ask, so the main
// item keeps its own life. Rules enforced here:
//   - one open assignment per item;
//   - asking a person needs a reason; "capability" is refused while an agent
//     has the capabilities the item needs (agents first);
//   - people must have agreed to take work from the requester (approvers
//     named by a rule excepted);
//   - workers that can't be reached now are refused.
func (a *App) Delegate(ctx context.Context, by Actor, sp DelegateSpec) (delegation.Assignment, error) {
	if err := requirePrivileged(by, "hand out work"); err != nil {
		return delegation.Assignment{}, err
	}
	if sp.Kind == "" {
		sp.Kind = delegation.Work
	}
	w := a.World(ctx)
	wk, ok := w.Workers[sp.Worker]
	if !ok {
		return delegation.Assignment{}, NotFound("no worker %q", sp.Worker)
	}
	item, ok := w.Items[sp.Item]
	if !ok {
		return delegation.Assignment{}, NotFound("no item %s", sp.Item)
	}
	if err := checkWorker(w, wk, item, sp, by); err != nil {
		return delegation.Assignment{}, err
	}

	var out delegation.Assignment
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		it, ok := s.Items[sp.Item]
		if !ok || !it.Open() {
			return nil, Conflict("item %s is not open", sp.Item)
		}
		var evs []Event
		if sp.Kind.Answered() { // the ask gets its own child item
			title := fmt.Sprintf("%s：%s", askTitle(sp.Kind), firstNonEmpty(sp.Brief.Goal, it.Title))
			child, e, err := planning.New(ItemID(s.NextID("I")), planning.Spec{Project: it.Project, Parent: it.ID, Title: title,
				Due: firstDue(sp.Brief.Due, it.Due), Needs: []planning.Need{{Kind: sp.Why}}}, a.now(), by)
			if err != nil {
				return nil, err
			}
			s.Items[child.ID] = child
			evs = append(evs, e...)
			it = child
		} else if cur, ok := s.Assignments[it.Assignment]; ok && cur.Open() {
			return nil, Conflict("item %s is already with %s (%s, %s); cancel that first", it.ID, cur.Worker, cur.ID, cur.Status)
		} else if it.Assignment != "" {
			it.Assignment, it.Owner = "", "" // the previous assignment ended
		}
		brief := sp.Brief
		if brief.Goal == "" {
			brief.Goal = it.Title
		}
		if len(brief.Done) == 0 {
			for _, c := range it.Acceptance {
				brief.Done = append(brief.Done, c.Text)
			}
		}
		if brief.Due == nil {
			brief.Due = it.Due
		}
		if brief.Estimate == 0 {
			brief.Estimate = it.Estimate
		}
		conv := sp.Conv
		if conv == "" {
			conv = s.Projects[it.Project].Home
		}
		as, e, err := delegation.New(delegation.Offer{ID: AssignmentID(s.NextID("A")), Item: it.ID, Project: it.Project, Worker: wk.ID,
			ToPerson: wk.Kind == workforce.Human, Kind: sp.Kind, Why: sp.Why, Options: sp.Options, Brief: brief, Conv: conv}, a.now(), by)
		if err != nil {
			return nil, err
		}
		e2, err := it.Attach(as.ID, wk.ID, a.now(), by)
		if err != nil {
			return nil, err
		}
		s.Items[it.ID], s.Assignments[as.ID] = it, as
		out = as
		return append(append(evs, e...), e2...), nil
	})
	if err != nil {
		return delegation.Assignment{}, err
	}
	d := a.driverFor(wk)
	if d == nil {
		_ = a.Report(ctx, System("driver:none"), out.ID, Report{Action: "fail", Note: "no driver for " + string(wk.Kind)})
		return out, Invalid("no driver can reach %s (%s)", wk.ID, wk.Kind)
	}
	if err := d.Offer(ctx, wk, out); err != nil {
		_ = a.Report(ctx, System("driver:"+string(wk.Kind)), out.ID, Report{Action: "fail", Note: "could not deliver: " + err.Error()})
		return out, fmt.Errorf("could not reach %s: %w", wk.ID, err)
	}
	cur, _ := a.Assignment(out.ID)
	return cur, nil
}

func checkWorker(w *world.World, wk workforce.Worker, item planning.Item, sp DelegateSpec, by Actor) error {
	if !item.Open() {
		return Conflict("item %s is %s", item.ID, item.Status)
	}
	if wk.Kind == workforce.Human {
		if sp.Why == "" {
			return Invalid("asking a person needs a reason (why): approval, decision, physical, relationship, judgment, knowledge or capability")
		}
		if sp.Why == NeedCapability {
			if agent := agentThatCan(w, item); agent != "" {
				return Invalid("agent %s has what %s needs; hand it to the agent (only ask a person when no agent can do it)", agent, item.ID)
			}
		}
	}
	approver := sp.Kind == delegation.Approval && isApprover(w, wk, item)
	if !approver && !wk.AcceptsFrom(by, w.Now) {
		return Forbidden("%s has not agreed to take work from the bot (consent)", wk.ID)
	}
	if ok, why := w.State(wk.ID).Available(w.Now); !ok {
		return Conflict("%s can't take work now: %s", wk.ID, why)
	}
	return nil
}

// agentThatCan returns an agent with every capability the item needs.
func agentThatCan(w *world.World, it planning.Item) WorkerID {
	var needs []workforce.Capability
	for _, n := range it.Needs {
		if n.Kind == NeedCapability && n.Detail != "" {
			needs = append(needs, workforce.ParseCapability(n.Detail))
		}
	}
	if len(needs) == 0 {
		return ""
	}
	for id, wk := range w.Workers {
		if !wk.Kind.Agentic() {
			continue
		}
		all := true
		for _, n := range needs {
			all = all && wk.Can(n)
		}
		if all {
			if ok, _ := w.State(id).Available(w.Now); ok {
				return id
			}
		}
	}
	return ""
}

func isApprover(w *world.World, wk workforce.Worker, it planning.Item) bool {
	for _, n := range it.Needs {
		if n.Kind != NeedApproval {
			continue
		}
		action, scope, _ := strings.Cut(n.Detail, "@")
		if wk.MayApprove(action, scope) {
			return true
		}
		chain := w.Chain(it.Project)
		if r, _, ok := chain.Rule(action, scope); ok {
			for _, id := range chain.Approvers(r, w.Now, w.IsWorker) {
				if id == wk.ID {
					return true
				}
			}
		}
	}
	return false
}

func askTitle(k delegation.AskKind) string {
	return map[delegation.AskKind]string{delegation.Approval: "审批", delegation.Decision: "决定", delegation.Clarification: "澄清", delegation.Review: "确认"}[k]
}

func firstDue(v ...*time.Time) *time.Time {
	for _, t := range v {
		if t != nil {
			return t
		}
	}
	return nil
}

func firstNonEmpty(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}

// Assignment returns one assignment.
func (a *App) Assignment(id AssignmentID) (delegation.Assignment, bool) {
	var out delegation.Assignment
	var ok bool
	a.Store.Read(func(s *State) { out, ok = s.Assignments[id] })
	return out, ok
}

// Report is the worker side: what the worker (a person in chat, or a driver
// for an agent) says about an assignment.
type Report struct {
	Action   string // accept | decline | counter | start | progress | ask | pause | deliver | answer | fail | release
	Note     string
	ETA      *time.Time
	Due      *time.Time // counter
	Result   string
	Evidence []string
	Choice   string // answer
	Ref      string // worker-side id (agent task id)
}

func (a *App) Report(ctx context.Context, by Actor, id AssignmentID, r Report) error {
	now := a.now()
	var as delegation.Assignment
	evs, err := a.commit(ctx, func(s *State) ([]Event, error) {
		var ok bool
		as, ok = s.Assignments[id]
		if !ok {
			return nil, NotFound("no assignment %s", id)
		}
		var evs []Event
		var err error
		switch r.Action {
		case "accept":
			evs, err = as.Accept(r.ETA, r.Note, now, by)
		case "decline":
			evs, err = as.Decline(r.Note, now, by)
		case "counter":
			if r.Due == nil {
				return nil, Invalid("a counter-proposal needs a new due time")
			}
			evs, err = as.Counter(*r.Due, r.Note, now, by)
		case "start":
			evs, err = as.Start(now, by)
		case "progress":
			evs, err = as.Progress(r.Note, r.ETA, now, by)
		case "ask":
			evs, err = as.Ask(r.Note, now, by)
		case "pause":
			evs, err = as.Pause(r.Note, now, by)
		case "deliver":
			if as.Status == delegation.Offered { // delivering straight away implies accepting
				if _, err := as.Accept(nil, "", now, by); err != nil {
					return nil, err
				}
			}
			evs, err = as.Deliver(r.Result, r.Evidence, now, by)
		case "answer":
			evs, err = as.Answer(r.Choice, r.Note, now, by)
		case "fail":
			evs, err = as.Fail(r.Note, now, by)
		case "release":
			evs, err = as.Release(r.Note, now, by)
		default:
			return nil, Invalid("unknown report %q", r.Action)
		}
		if err != nil {
			return nil, err
		}
		if r.Ref != "" {
			as.Ref = r.Ref
		}
		s.Assignments[id] = as
		evs = append(evs, syncItem(s, as, r.Action, now, by)...)
		return evs, nil
	})
	if err != nil {
		return err
	}
	a.afterWorkerEvent(ctx, as, r, evs)
	return nil
}

// syncItem keeps the item in step with its assignment (same transaction).
func syncItem(s *State, as delegation.Assignment, action string, now time.Time, by Actor) []Event {
	it, ok := s.Items[as.Item]
	if !ok || it.Assignment != as.ID {
		return nil
	}
	var evs []Event
	switch {
	case as.Status == delegation.Verified:
		e, _ := it.Complete(true, now, by)
		evs = e
	case as.Status.Terminal():
		evs = it.Detach(string(as.Status), now, by)
	case action == "ask" || action == "pause":
		evs = it.Block(firstNonEmpty(as.Question, "暂停"), now, by)
	case action == "accept" || action == "start" || action == "progress" || action == "deliver" || action == "accept_counter" || action == "answer_question" || action == "revise":
		it.Progressed(now)
	}
	if action == "accept_counter" && as.Brief.Due != nil {
		e, _ := it.Reschedule(as.Brief.Due, now, by)
		evs = append(evs, e...)
	}
	s.Items[it.ID] = it
	return evs
}

// afterWorkerEvent wakes the brain where the requester waits.
func (a *App) afterWorkerEvent(ctx context.Context, as delegation.Assignment, r Report, evs []Event) {
	var text string
	head := fmt.Sprintf("%s（%s，%s）", as.ID, as.Worker, oneLine(as.Brief.Goal, 60))
	switch r.Action {
	case "accept":
		if a.isAgent(as.Worker) {
			return // agents accept by queueing; no news
		}
		text = head + " 已接下。" + r.Note
	case "decline":
		text = head + " 被拒绝：" + firstNonEmpty(r.Note, "没说原因") + "。事项已回到待分派。"
	case "counter":
		text = fmt.Sprintf("%s 还价：%s（提议截止 %s）。需要决定是否接受。", head, firstNonEmpty(r.Note, "-"), fmtDue(r.Due))
	case "ask":
		text = head + " 有问题：" + r.Note
	case "deliver":
		text = head + " 已交付，等待验收：" + oneLine(r.Result, 400)
		if len(r.Evidence) > 0 {
			text += "\n证据：" + strings.Join(r.Evidence, "；")
		}
		if len(as.Brief.Done) > 0 {
			text += "\n完成标准：" + strings.Join(as.Brief.Done, "；")
		}
	case "answer":
		text = fmt.Sprintf("%s 答复：%s。%s", head, r.Choice, r.Note)
	case "fail":
		text = head + " 失败：" + oneLine(r.Note, 400) + "。事项已回到待分派。"
	case "release":
		text = head + " 被退回：" + r.Note + "。事项已回到待分派。"
	case "progress":
		if a.isAgent(as.Worker) || r.Note == "" {
			return
		}
		text = head + " 进展：" + r.Note
	default:
		return
	}
	var ev *Event
	if len(evs) > 0 {
		ev = &evs[0]
	}
	a.wake(ctx, Trigger{Kind: "event", Conv: as.Conv, Project: as.Project, Text: text, Event: ev, About: as.Requester})
}

func (a *App) isAgent(id WorkerID) bool {
	var agentic bool
	a.Store.Read(func(s *State) { agentic = s.Workers[id].Kind.Agentic() })
	return agentic
}

// Review is the requester side: verify | revise | cancel | answer_question | accept_counter.
func (a *App) Review(ctx context.Context, by Actor, id AssignmentID, action, note string) error {
	now := a.now()
	var as delegation.Assignment
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		var ok bool
		as, ok = s.Assignments[id]
		if !ok {
			return nil, NotFound("no assignment %s", id)
		}
		var evs []Event
		var err error
		switch action {
		case "verify":
			evs, err = as.Verify(note, now, by)
		case "revise":
			evs, err = as.Revise(note, now, by)
		case "cancel":
			evs, err = as.Cancel(note, now, by)
		case "answer_question":
			evs, err = as.AnswerQuestion(note, now, by)
		case "accept_counter":
			evs, err = as.AcceptCounter(now, by)
		default:
			return nil, Invalid("unknown review action %q (verify, revise, cancel, answer_question, accept_counter)", action)
		}
		if err != nil {
			return nil, err
		}
		s.Assignments[id] = as
		return append(evs, syncItem(s, as, action, now, by)...), nil
	})
	if err != nil {
		return err
	}
	kind := map[string]string{"verify": "verified", "revise": "revise", "cancel": "cancelled", "answer_question": "answer", "accept_counter": "counter_accepted"}[action]
	if action == "accept_counter" {
		note = "按你提的时间来，截止 " + fmtDue(as.Brief.Due)
	}
	a.notify(ctx, as, Notice{Kind: kind, Text: note})
	return nil
}

func (a *App) notify(ctx context.Context, as delegation.Assignment, n Notice) {
	var wk workforce.Worker
	a.Store.Read(func(s *State) { wk = s.Workers[as.Worker] })
	if d := a.driverFor(wk); d != nil {
		if err := d.Notify(ctx, wk, as, n); err != nil && a.Log != nil {
			a.Log.Info("worker not notified", "assignment", as.ID, "notice", n.Kind, "err", err)
		}
	}
}

// ---- gate: actions that need approval ----

// Gate is the result of asking whether an action may proceed.
type Gate struct {
	Allowed    bool
	Rule       *portfolio.ApprovalRule
	Approvers  []WorkerID
	Assignment *delegation.Assignment // the approval request created
}

// Authorize checks an action against the approval rules of the item's
// project chain. Without a rule it is allowed; with one, an approval is
// requested from the first available approver (unless one was already
// granted for this item and action).
func (a *App) Authorize(ctx context.Context, by Actor, item ItemID, action, scope, why string) (Gate, error) {
	w := a.World(ctx)
	it, ok := w.Items[item]
	if !ok {
		return Gate{}, NotFound("no item %s", item)
	}
	chain := w.Chain(it.Project)
	rule, _, ok := chain.Rule(action, scope)
	if !ok {
		return Gate{Allowed: true}, nil
	}
	g := Gate{Rule: &rule, Approvers: chain.Approvers(rule, w.Now, w.IsWorker)}
	approvals, pending := 0, (*delegation.Assignment)(nil)
	for _, as := range w.Assignments {
		child := w.Items[as.Item]
		if child.Parent != item || as.Kind != delegation.Approval || !strings.Contains(child.Title, action) {
			continue
		}
		switch {
		case as.Status == delegation.Verified && as.Choice == "approve":
			approvals++
		case as.Open():
			x := as
			pending = &x
		}
	}
	quorum := max(rule.Quorum, 1)
	if approvals >= quorum {
		g.Allowed = true
		return g, nil
	}
	if pending != nil {
		g.Assignment = pending
		return g, nil
	}
	var approver WorkerID
	for _, id := range g.Approvers {
		if ok, _ := w.State(id).Available(w.Now); ok {
			approver = id
			break
		}
	}
	if approver == "" {
		return g, Conflict("no available approver for %s (%s)", action, strings.Join(rule.Approvers, "/"))
	}
	// The ask needs the action on the item so the approver is recognised.
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		x := s.Items[item]
		x.Needs = append(x.Needs, planning.Need{Kind: NeedApproval, Detail: action + "@" + scope})
		s.Items[item] = x
		return nil, nil
	})
	if err != nil {
		return g, err
	}
	as, err := a.Delegate(ctx, System("gate:"+action), DelegateSpec{Item: item, Worker: approver, Kind: delegation.Approval, Why: NeedApproval,
		Brief: delegation.Brief{Goal: action + "（" + scope + "）", Context: why}})
	if err != nil {
		return g, err
	}
	g.Assignment = &as
	return g, nil
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
