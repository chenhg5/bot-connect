package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/attention"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/inbox"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
)

// WakePolicy is the bot's work rhythm: which signals may interrupt and wake
// the brain now, which wait for the next batch, which are only recorded.
type WakePolicy struct {
	Modes      map[string]string // direct_message | mention | worker_reply | relay | risk_high | risk_medium | risk_low | cadence | system → now | batch | ignore
	BatchEvery time.Duration
	// Working rhythm for non-urgent wake-ups (batches, routines): hours
	// "09:00-21:00" (overnight ranges allowed), days "weekdays" / "Mon,Tue…",
	// and how many wake-ups a day before non-urgent ones wait for tomorrow.
	Hours       string
	Days        string
	DailyBudget int
}

// DefaultWakePolicy: people and high risks wake the brain; the rest batches.
func DefaultWakePolicy() WakePolicy {
	return WakePolicy{BatchEvery: 30 * time.Minute, Modes: map[string]string{
		"direct_message": "now", "mention": "now", "worker_reply": "now", "relay": "now",
		"risk_high": "now", "risk_medium": "batch", "risk_low": "ignore", "cadence": "batch", "system": "batch",
		"observation": "batch", "wakeup": "now"}, DailyBudget: DefaultDailyBudget}
}

// Mode decides for one signal.
func (p WakePolicy) Mode(s inbox.Signal) string {
	key := string(s.Source)
	if s.Source == inbox.Risk {
		key = "risk_" + map[int]string{0: "low", 1: "medium", 2: "high"}[s.Level]
	}
	if m := p.Modes[key]; m != "" {
		return m
	}
	return "batch"
}

// DefaultDailyBudget bounds non-urgent wake-ups a day when nothing is
// configured: working on its own must not be able to burn the whole quota.
const DefaultDailyBudget = 60

// SignalSpec is what a new signal is made from.
type SignalSpec struct {
	Dedupe    string
	Source    inbox.Source
	Reason    string
	Actor     Actor
	ActorName string
	Summary   string
	Body      string
	Refs      inbox.Refs
	ReplyTo   inbox.ReplyTo
	Level     int
}

// Ingest records a signal (once per dedupe key) and wakes the brain if the
// policy (and triage) say now. It returns the signal and whether it is new.
func (a *App) Ingest(ctx context.Context, sp SignalSpec) (inbox.Signal, bool, error) {
	draft, err := inbox.New("draft", sp.Dedupe, sp.Source, sp.Reason, a.now())
	if err != nil {
		return inbox.Signal{}, false, err
	}
	draft.Actor, draft.ActorName, draft.Summary, draft.Body, draft.Refs, draft.ReplyTo, draft.Level =
		sp.Actor, sp.ActorName, sp.Summary, sp.Body, sp.Refs, sp.ReplyTo, sp.Level
	draft.Wake = a.Policy.Mode(draft)
	var tri TriageResult
	if a.Triage != nil && (sp.Source == inbox.DirectMessage || sp.Source == inbox.Mention) {
		// triage is on the hot path: a slow judge must not delay the reply
		tctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		r, err := a.Triage.Assess(tctx, draft)
		cancel()
		if err == nil {
			tri = r
			if r.Level != nil {
				draft.Level = *r.Level
				draft.Wake = a.Policy.Mode(draft)
			}
			if r.Wake != "" {
				draft.Wake = r.Wake
			}
			if r.Intent != "" {
				draft.Reason = sp.Reason + ":" + r.Intent
			}
			draft.Hints, draft.NeedsPlan = r.Hints, r.NeedsPlan
			if sp.Actor.Privileged() {
				if p, ok := a.exploreAwaitingReaction(); ok {
					draft.Hints = append(draft.Hints, fmt.Sprintf("「%s」第 %d 轮的尝试已经出去了：如果这条是委托人对它的反应，先用 plan learn 记下学到了什么和方向（continue / pivot / stop / converge），再安排下一轮。", p.Title, p.Plan.Round))
				}
			}
			if !r.NeedsPlan && sp.Actor.Privileged() {
				if g, ok := a.unplannedGoal(); ok {
					draft.NeedsPlan = true
					draft.Hints = append(draft.Hints, fmt.Sprintf("%s 的目标（%s）还没有计划。约束够用就 plan draft；哪个方向最好是调研要回答的问题，不要让委托人替你回答。", g.ID, oneLine(g.Body, 40)))
				}
			}
		} else if a.Log != nil {
			a.Log.Info("triage failed; using the policy", "err", err)
		}
	}
	// What needs no decision lands without waking the brain: a person
	// accepting the one request waiting for their answer.
	if tri.Action == "accept" && tri.Confidence >= 0.9 && sp.Actor.Worker != "" && sp.Actor.Worker != Principal {
		if as, ok := a.onlyOffer(sp.Actor.Worker); ok {
			if err := a.Report(ctx, sp.Actor, as.ID, Report{Action: "accept", Note: sp.Body}); err == nil {
				draft.Status, draft.Note, draft.Wake = inbox.Handled, "landed by triage: accepted "+string(as.ID), "ignore"
				if a.Say != nil && sp.ReplyTo.Conv != "" {
					ack := fmt.Sprintf("收到，已登记你接下了 %s「%s」", as.ID, oneLine(as.Brief.Goal, 40))
					if as.Brief.Due != nil {
						ack += "，截止 " + fmtDue(as.Brief.Due)
					}
					_ = a.Say(ctx, sp.ReplyTo.Conv, ack+"。")
				}
			}
		}
	}
	var out inbox.Signal
	created := false
	_, err = a.commit(ctx, func(s *State) ([]Event, error) {
		for _, x := range s.Signals {
			if x.DedupeKey == sp.Dedupe {
				out = x
				return nil, nil
			}
		}
		draft.ID = s.NextID("S")
		if draft.Wake == "ignore" && draft.Status == inbox.Pending {
			draft.Status, draft.Note = inbox.Ignored, "wake policy: ignore"
		}
		s.Signals[draft.ID] = draft
		out, created = draft, true
		return []Event{NewEvent("signal.received", "signal:"+draft.ID, a.now(), sp.Actor, "source", string(sp.Source), "reason", sp.Reason, "wake", draft.Wake)}, nil
	})
	if err != nil {
		return inbox.Signal{}, false, err
	}
	if created && out.Wake == "now" {
		a.WakeOn(ctx, out.ReplyTo.Conv, out.ID)
	}
	return out, created, nil
}

// WakeOn marks signals seen and hands them to the brain as the focus of a
// wake-up.
func (a *App) WakeOn(ctx context.Context, replyConv string, ids ...string) {
	var focus []inbox.Signal
	_, _ = a.commit(ctx, func(s *State) ([]Event, error) {
		for _, id := range ids {
			x, ok := s.Signals[id]
			if !ok || x.Status.Terminal() {
				continue
			}
			x.MarkSeen(a.now())
			s.Signals[id] = x
			focus = append(focus, x)
		}
		if len(focus) > 0 {
			countWake(s, a.now())
		}
		return nil, nil
	})
	if len(focus) > 0 && a.Waker != nil {
		a.Waker.Wake(ctx, Wake{Signals: focus, ReplyConv: replyConv})
	}
}

// UpdateSignal records a decision about a signal.
func (a *App) UpdateSignal(ctx context.Context, by Actor, id string, to inbox.Status, note string, until *time.Time) error {
	if err := requirePrivileged(by, "handle signals"); err != nil {
		return err
	}
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		x, ok := s.Signals[id]
		if !ok {
			return nil, NotFound("no signal %s", id)
		}
		evs, err := x.Transition(to, note, until, a.now(), by)
		if err != nil {
			return nil, err
		}
		s.Signals[id] = x
		return evs, nil
	})
	return err
}

// Signal returns one signal.
func (a *App) Signal(id string) (inbox.Signal, bool) {
	var out inbox.Signal
	var ok bool
	a.Store.Read(func(s *State) { out, ok = s.Signals[id] })
	return out, ok
}

// Scored is a signal with its attention score.
type ScoredSignal struct {
	inbox.Signal
	Score float64
}

// Inbox lists open signals (pending, seen, handling; deferred too if all),
// highest score first.
func (a *App) Inbox(all bool) []ScoredSignal {
	now := a.now()
	var out []ScoredSignal
	a.Store.Read(func(s *State) {
		for _, x := range s.Signals {
			if x.Status.Terminal() || (x.Status == inbox.Deferred && !all) {
				continue
			}
			weight := 2.0
			if p, ok := s.Projects[x.Refs.Project]; ok {
				weight = p.Priority.Weight()
			}
			out = append(out, ScoredSignal{x, x.Score(weight, now)})
		}
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// SignalLine is one signal in a list the brain reads.
func SignalLine(x inbox.Signal, now time.Time) string {
	who := firstNonEmpty(x.ActorName, x.Actor.String())
	line := fmt.Sprintf("%s [%s·%s] %s", x.ID, x.Source, x.Reason, x.Summary)
	if who != "" && x.Source != inbox.Risk && x.Source != inbox.Cadence {
		line += "（来自 " + who + "）"
	}
	if x.Waiting() {
		line += fmt.Sprintf("，已等 %s", now.Sub(x.CreatedAt).Round(time.Minute))
	}
	if x.Status == inbox.Deferred && x.Until != nil {
		line += "，推迟到 " + x.Until.Format("01-02 15:04")
	}
	return line
}

// FocusText renders the signals a wake-up is about, in full.
func FocusText(sigs []inbox.Signal) string {
	var b strings.Builder
	for _, x := range sigs {
		fmt.Fprintf(&b, "信号 %s [%s·%s]", x.ID, x.Source, x.Reason)
		if who := firstNonEmpty(x.ActorName, x.Actor.String()); who != "" {
			b.WriteString(" 来自 " + who)
		}
		if x.Refs.Project != "" || x.Refs.Item != "" || x.Refs.Assignment != "" {
			fmt.Fprintf(&b, "（%s）", strings.Trim(strings.Join([]string{string(x.Refs.Project), string(x.Refs.Item), string(x.Refs.Assignment)}, " "), " "))
		}
		b.WriteString("\n")
		body := firstNonEmpty(x.Body, x.Summary)
		b.WriteString(body + "\n")
		for _, h := range x.Hints {
			b.WriteString("〔判断〕" + h + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// InboxDigest is the rest of the inbox, one line each, for a wake-up.
func (a *App) InboxDigest(exclude map[string]bool, max int) string {
	now := a.now()
	var lines []string
	n := 0
	for _, x := range a.Inbox(false) {
		if exclude[x.ID] {
			continue
		}
		if n++; n > max {
			lines = append(lines, fmt.Sprintf("…还有 %d 条（inbox 查看）", len(a.Inbox(false))-max-len(exclude)))
			break
		}
		lines = append(lines, fmt.Sprintf("- %s 分 %.1f", SignalLine(x.Signal, now), x.Score))
	}
	return strings.Join(lines, "\n")
}

// TurnDone is called after a wake-up turn: focus signals the brain left
// untouched are handled if it answered (a message was sent or tools used),
// otherwise they stay seen and come back with the next batch.
func (a *App) TurnDone(ctx context.Context, ids []string, answered bool) {
	if !answered || len(ids) == 0 {
		return
	}
	_, _ = a.commit(ctx, func(s *State) ([]Event, error) {
		var evs []Event
		for _, id := range ids {
			x, ok := s.Signals[id]
			if !ok || x.Status != inbox.Seen {
				continue
			}
			e, err := x.Transition(inbox.Handled, "answered in the wake-up", nil, a.now(), System("turn"))
			if err == nil {
				s.Signals[id] = x
				evs = append(evs, e...)
			}
		}
		return evs, nil
	})
}

// ---- contacting people ----

// Contact sends a message to a person along their contact routes (the
// channel is the scaffold's business: urgency, hours, budgets, approvals).
func (a *App) Contact(ctx context.Context, by Actor, who WorkerID, message string, u workforce.Urgency, expectReply bool, about inbox.Refs) (string, error) {
	if err := requirePrivileged(by, "contact people"); err != nil {
		return "", err
	}
	var wk workforce.Worker
	var ok bool
	a.Store.Read(func(s *State) { wk, ok = s.Workers[who] })
	if !ok {
		return "", NotFound("no worker %q", who)
	}
	if wk.Kind.Agentic() {
		return "", Invalid("%s is an agent: use delegate (tracked) or agent_task (quick)", who)
	}
	d := a.driverFor(wk)
	if d == nil {
		return "", Invalid("no way to reach %s", who)
	}
	if err := d.Notify(ctx, wk, delegationStub(about), Notice{Kind: "message", Text: message, Urgency: u}); err != nil {
		return "", err
	}
	var id string
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		id = s.NextID("C")
		s.Contacts[id] = Contact{ID: id, Worker: who, Message: message, About: about, ExpectReply: expectReply, Open: expectReply, At: a.now(), By: by}
		return []Event{NewEvent("contact.sent", "worker:"+string(who), a.now(), by, "contact", id, "urgency", u.String())}, nil
	})
	return id, err
}

// OpenContacts lists messages to a worker still waiting for their reply.
func (a *App) OpenContacts(who WorkerID) []Contact {
	var out []Contact
	a.Store.Read(func(s *State) {
		for _, c := range s.Contacts {
			if c.Worker == who && c.Open {
				out = append(out, c)
			}
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// Relay passes something from an isolated conversation (a colleague's
// request, a reply to a contact) to the principal side as a signal; replies to
// open contacts close them.
func (a *App) Relay(ctx context.Context, from Actor, fromName, conv, msgID, text string) (inbox.Signal, error) {
	var about inbox.Refs
	if from.Worker != "" {
		_, _ = a.commit(ctx, func(s *State) ([]Event, error) {
			for id, c := range s.Contacts {
				if c.Worker == from.Worker && c.Open {
					c.Open = false
					about = c.About
					s.Contacts[id] = c
				}
			}
			return nil, nil
		})
		about.Worker = from.Worker
	}
	dedupe := "relay:" + conv + ":" + firstNonEmpty(msgID, fmt.Sprint(a.now().UnixNano()))
	sig, _, err := a.Ingest(ctx, SignalSpec{Dedupe: dedupe, Source: inbox.Relay, Reason: "relay", Actor: from, ActorName: fromName,
		Summary: oneLine(text, 120), Body: text, Refs: about})
	return sig, err
}

// ---- the inbox side of the control loop ----

// tickInbox: deferred signals coming due, batched wake-ups, escalation of
// high risks nobody handled, expiry of stale signals.
func (a *App) tickInbox(ctx context.Context) {
	now := a.now()
	var due []string
	var escalate []inbox.Signal
	batch := false
	_, _ = a.commit(ctx, func(s *State) ([]Event, error) {
		var evs []Event
		for id, x := range s.Signals {
			switch {
			case x.DueAgain(now):
				s.Signals[id] = x
				due = append(due, id)
			case (x.Status == inbox.Pending || x.Status == inbox.Seen) && now.Sub(x.CreatedAt) > 7*24*time.Hour:
				if e, err := x.Transition(inbox.Expired, "unhandled for a week", nil, now, System("rule:expire")); err == nil {
					s.Signals[id] = x
					evs = append(evs, e...)
				}
			case x.Source == inbox.Risk && x.Level >= 2 && x.Status.Open() && x.Status != inbox.Deferred && x.Status != inbox.Handling &&
				!s.Escalated[id] && now.Sub(x.CreatedAt) >= EscalateAfter:
				s.Escalated[id] = true
				escalate = append(escalate, x)
			}
		}
		quiet := !a.Policy.InWindow(now) || (a.Policy.DailyBudget > 0 && s.WakeCounts[dayKey(now)] >= a.Policy.DailyBudget)
		if !quiet && now.Sub(s.LastBatch) >= a.Policy.BatchEvery {
			for _, x := range s.Signals {
				if x.Status == inbox.Pending {
					batch = true
					break
				}
			}
			if batch {
				s.LastBatch = now
			}
		}
		return evs, nil
	})
	for _, x := range escalate {
		a.escalateToPrincipal(ctx, x)
	}
	if batch || len(due) > 0 {
		var ids []string
		for _, x := range a.Inbox(false) {
			if x.Status == inbox.Pending && len(ids) < 5 {
				ids = append(ids, x.ID)
			}
		}
		if len(ids) > 0 {
			a.WakeOn(ctx, "", ids...)
		}
	}
}

func (a *App) escalateToPrincipal(ctx context.Context, x inbox.Signal) {
	var principal workforce.Worker
	var ok bool
	a.Store.Read(func(s *State) { principal, ok = s.Workers[Principal] })
	if !ok {
		return
	}
	if d := a.driverFor(principal); d != nil {
		text := fmt.Sprintf("⚠️ 已 %s 没有处理：%s（信号 %s）", EscalateAfter, x.Summary, x.ID)
		_ = d.Notify(ctx, principal, delegationStub(x.Refs), Notice{Kind: "message", Text: text, Urgency: workforce.Urgent})
	}
}

// raiseFindings turns what the rules find into risk signals: once per
// finding and level; a worse level supersedes the old signal; a finding
// that went away closes its open signal.
func (a *App) raiseFindings(ctx context.Context) {
	c := attention.NewContext(a.World(ctx))
	current := map[string]attention.Signal{}
	for _, f := range a.Attention.Signals(c) {
		current[f.Key] = f
	}
	_, _ = a.commit(ctx, func(s *State) ([]Event, error) {
		now := a.now()
		var evs []Event
		for id, x := range s.Signals {
			if x.Source != inbox.Risk || !x.Status.Open() {
				continue
			}
			key := strings.TrimPrefix(x.DedupeKey, "risk:")
			if i := strings.LastIndex(key, ":L"); i > 0 {
				key = key[:i]
			}
			f, still := current[key]
			if !still || int(f.Level) > x.Level {
				note := "condition cleared"
				if still {
					note = "superseded by a worse level"
				}
				if e, err := x.Transition(inbox.Handled, note, nil, now, System("rule")); err == nil {
					s.Signals[id] = x
					evs = append(evs, e...)
				}
			}
		}
		return evs, nil
	})
	keys := make([]string, 0, len(current))
	for k := range current {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := current[k]
		if f.Level < attention.Medium {
			continue
		}
		_, _, _ = a.Ingest(ctx, SignalSpec{Dedupe: fmt.Sprintf("risk:%s:L%d", f.Key, f.Level), Source: inbox.Risk, Reason: f.Kind,
			Actor: System("rule:" + f.Kind), Summary: f.Summary, Body: f.Summary + "\n建议：" + strings.Join(f.Suggest, " / "),
			Refs: refsOf(f), Level: int(f.Level), ReplyTo: inbox.ReplyTo{Conv: homeOf(c, f.Project)}})
	}
}

func refsOf(f attention.Signal) inbox.Refs {
	r := inbox.Refs{Project: f.Project}
	switch {
	case strings.HasPrefix(f.Subject, "item:"):
		r.Item = ItemID(strings.TrimPrefix(f.Subject, "item:"))
	case strings.HasPrefix(f.Subject, "assignment:"):
		r.Assignment = AssignmentID(strings.TrimPrefix(f.Subject, "assignment:"))
	case strings.HasPrefix(f.Subject, "worker:"):
		r.Worker = WorkerID(strings.TrimPrefix(f.Subject, "worker:"))
	}
	return r
}

func homeOf(c *attention.Context, p ProjectID) string {
	if x, ok := c.Projects[p]; ok {
		return x.Home
	}
	return ""
}

// onlyOffer returns the worker's single assignment waiting for their answer.
func (a *App) onlyOffer(w WorkerID) (delegation.Assignment, bool) {
	var found []delegation.Assignment
	a.Store.Read(func(s *State) {
		for _, x := range s.Assignments {
			if x.Worker == w && x.Status == delegation.Offered {
				found = append(found, x)
			}
		}
	})
	if len(found) != 1 {
		return delegation.Assignment{}, false
	}
	return found[0], true
}

// unplannedGoal: a goal or multi-step request from the principal side in the
// last day, with no plan drafted since.
func (a *App) unplannedGoal() (inbox.Signal, bool) {
	var goal inbox.Signal
	found := false
	a.Store.Read(func(s *State) {
		now := a.now()
		for _, x := range s.Signals {
			if x.NeedsPlan && now.Sub(x.CreatedAt) < 24*time.Hour && (!found || x.CreatedAt.After(goal.CreatedAt)) {
				goal, found = x, true
			}
		}
		if !found {
			return
		}
		for _, p := range s.Projects {
			if p.Plan.Stage != portfolio.NoPlan && !p.Plan.UpdatedAt.Before(goal.CreatedAt) {
				found = false
			}
		}
	})
	return goal, found
}

// exploreAwaitingReaction: an exploration whose current round went out and
// nothing was recorded since.
func (a *App) exploreAwaitingReaction() (portfolio.Project, bool) {
	var out portfolio.Project
	found := false
	a.Store.Read(func(s *State) {
		for _, p := range s.Projects {
			if p.Plan.Kind == portfolio.KindExplore && p.Plan.Stage == portfolio.PlanActive && roundTried(s, p, a.now()) {
				out, found = p, true
			}
		}
	})
	return out, found
}
