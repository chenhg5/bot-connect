package pm

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/plan"
	"github.com/chenhg5/bot-connect/internal/reach"
	"github.com/chenhg5/bot-connect/internal/workforce"
)

// Follow-up timing for people. Agents are followed by their own timeouts.
const (
	offerNudgeAfter  = 4 * time.Hour  // no answer to an offer → reminder
	offerExpireAfter = 48 * time.Hour // still no answer → expired, back to the planner
	quietNudgeAfter  = 24 * time.Hour // working, due within a day, silent this long → reminder
	escalateAfter    = 2 * time.Hour  // a high risk nobody handled → straight to the owner
)

// ioReachers builds the channels a bot can use.
func ioReachers(io *BotIO) []reach.Reacher {
	var rs []reach.Reacher
	if io.SendUser != nil {
		rs = append(rs, funcReacher{reach.FeishuDM, func(ctx context.Context, r reach.Route, m reach.Message) (string, error) {
			return io.SendUser(ctx, "feishu", r.Address, m.Text)
		}})
	}
	if io.NotifyOwner != nil {
		rs = append(rs, funcReacher{reach.OwnerChat, func(ctx context.Context, r reach.Route, m reach.Message) (string, error) {
			return "", io.NotifyOwner(ctx, m.Text)
		}})
	}
	return rs
}

type funcReacher struct {
	ch reach.Channel
	f  func(context.Context, reach.Route, reach.Message) (string, error)
}

func (f funcReacher) Channel() reach.Channel { return f.ch }
func (f funcReacher) Send(ctx context.Context, r reach.Route, m reach.Message) (reach.Receipt, error) {
	id, err := f.f(ctx, r, m)
	return reach.Receipt{Channel: f.ch, Address: r.Address, MessageID: id, At: time.Now()}, err
}

// Run ticks every interval until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick(ctx)
		}
	}
}

// Tick runs one round of the control loop: risk detection, follow-ups with
// people, escalation. It never decides what to do about a problem — it
// makes sure the brain (or the owner) hears about it in time.
func (s *Service) Tick(ctx context.Context) {
	s.followUps(ctx)
	s.assessRisks()
	s.escalate(ctx)
}

// pace: how long a worker takes relative to estimates (from verified work).
func (s *Service) paceLocked() plan.Pace {
	ratios := map[string][]float64{}
	for _, a := range s.d.Assignments {
		if a.Status != workforce.Verified && a.Status != workforce.Delivered || a.Brief.Estimate <= 0 {
			continue
		}
		var start, end time.Time
		for _, e := range a.History {
			switch e.Type {
			case workforce.EvAccept, workforce.EvStart:
				if start.IsZero() {
					start = e.At
				}
			case workforce.EvDeliver:
				end = e.At
			}
		}
		if !start.IsZero() && end.After(start) {
			ratios[a.Worker] = append(ratios[a.Worker], float64(end.Sub(start))/float64(a.Brief.Estimate))
		}
	}
	return func(w string) float64 {
		rs := ratios[w]
		if len(rs) < 2 { // not enough history to trust
			return 1
		}
		sum := 0.0
		for _, r := range rs {
			sum += r
		}
		avg := sum / float64(len(rs))
		if avg < 0.5 {
			avg = 0.5
		}
		if avg > 3 {
			avg = 3
		}
		return avg
	}
}

func (s *Service) assessRisks() {
	now := s.now()
	type wake struct {
		bot, conv string
		text      string
		owner     identity.User
	}
	var wakes []wake
	s.mu.Lock()
	var open []plan.Task
	for _, t := range s.d.Tasks {
		if t.Open() {
			open = append(open, *t)
		}
	}
	found := map[string]bool{}
	for _, d := range plan.Assess(open, now, s.paceLocked()) {
		key := d.Subject + "/" + d.Rule
		found[key] = true
		var existing *plan.Risk
		for _, r := range s.d.Risks {
			if r.Subject == d.Subject && r.Rule == d.Rule && r.Status != plan.RiskClosed {
				existing = r
				break
			}
		}
		t := s.d.Tasks[d.Subject]
		switch {
		case existing == nil:
			r := &plan.Risk{ID: s.nextID("R"), Bot: t.Bot, Subject: d.Subject, Kind: d.Kind, Level: d.Level, Rule: d.Rule,
				Summary: d.Summary, Options: d.Options, RaisedBy: "rule:" + d.Rule, Status: plan.RiskOpen, At: now}
			s.d.Risks[r.ID] = r
			wakes = append(wakes, wake{t.Bot, t.ConvKey, riskText(*r, *t, false), t.Owner})
		case existing.Status == plan.RiskOpen && d.Level > existing.Level:
			existing.Level, existing.Summary, existing.Options, existing.At = d.Level, d.Summary, d.Options, now
			delete(s.d.Escalated, existing.ID)
			wakes = append(wakes, wake{t.Bot, t.ConvKey, riskText(*existing, *t, true), t.Owner})
		}
	}
	// Rule-raised risks whose condition is gone close themselves.
	for _, r := range s.d.Risks {
		if r.Rule != "" && r.Status == plan.RiskOpen && !found[r.Subject+"/"+r.Rule] {
			r.Status = plan.RiskClosed
		}
	}
	s.saveLocked()
	ios := s.bots
	s.mu.Unlock()
	for _, w := range wakes {
		io := ios[w.bot]
		if io == nil || io.Post == nil {
			continue
		}
		conv := w.conv
		if conv == "" && io.OwnerConv != nil {
			conv = io.OwnerConv()
		}
		if !io.Post(conv, w.text, w.owner) {
			slog.Warn("pm: risk for unknown conversation", "conv", conv)
		}
	}
}

func riskText(r plan.Risk, t plan.Task, raised bool) string {
	verb := "发现风险"
	if raised {
		verb = "风险升级"
	}
	who := "未分派"
	if t.Assignee != "" {
		who = t.Assignee
	}
	return fmt.Sprintf("%s %s【%s · %s】任务 %s「%s」（执行者 %s，截止 %s）\n%s\n可选应对：%s\n"+
		"请现在就告诉需求方：发生了什么、为什么、有哪些选项、你建议哪个；需要时先和执行者确认进度。处理后用 risk_update 标记（accepted / mitigated）。",
		verb, r.ID, r.Kind, r.Level, t.ID, t.Title, who, dueText(t.Due, time.Local), r.Summary, strings.Join(r.Options, " / "))
}

// followUps: remind people about offers and quiet work, expire ignored offers,
// and flag people who stop responding.
func (s *Service) followUps(ctx context.Context) {
	now := s.now()
	type nudge struct {
		bot  string
		a    workforce.Assignment
		note string
	}
	var nudges []nudge
	var expire []string
	s.mu.Lock()
	for _, a := range s.d.Assignments {
		if a.Status.Terminal() || s.personFor(a.Bot, a.Worker) == nil {
			continue
		}
		switch a.Status {
		case workforce.Offered:
			age := now.Sub(a.CreatedAt)
			if age > offerExpireAfter {
				expire = append(expire, a.ID)
			} else if age > offerNudgeAfter {
				nudges = append(nudges, nudge{a.Bot, *a, "还没收到你是否接下的回复"})
			}
		case workforce.Accepted, workforce.Working, workforce.Revising:
			if a.Brief.Due == nil {
				continue
			}
			last := a.Progress
			if last.IsZero() {
				last = a.CreatedAt
			}
			left := a.Brief.Due.Sub(now)
			switch {
			case left < 0 && now.Sub(last) > 2*time.Hour:
				nudges = append(nudges, nudge{a.Bot, *a, fmt.Sprintf("已经过了截止时间（%s），现在进展如何？", a.Brief.Due.Format("01-02 15:04"))})
			case left > 0 && left < 24*time.Hour && now.Sub(last) > quietNudgeAfter/2 && now.Sub(a.CreatedAt) > quietNudgeAfter/2:
				nudges = append(nudges, nudge{a.Bot, *a, fmt.Sprintf("截止 %s，进展如何？有卡点可以直接说", a.Brief.Due.Format("01-02 15:04"))})
			}
		}
	}
	s.mu.Unlock()

	for _, id := range expire {
		_ = s.Emit(id, workforce.Event{Type: workforce.EvExpire, Note: "no answer within 48h"})
	}
	for _, n := range nudges {
		p := s.personFor(n.bot, n.a.Worker)
		count, last := n.a.Nudges(now.Add(-24 * time.Hour))
		if count >= p.maxNudges() {
			s.flagUnresponsive(n.a, count)
			continue
		}
		if !last.IsZero() && now.Sub(last) < p.minInterval() {
			continue
		}
		if _, err := reach.Plan(p.cfg.Contact, reach.Message{Urgency: reach.Reminder}, p.hours(), now); err != nil {
			continue // outside their hours: try again later
		}
		ev := workforce.Event{Type: workforce.EvNudge, At: now, Note: n.note}
		if err := p.Notify(ctx, n.a, ev); err != nil {
			slog.Info("pm: nudge failed", "assignment", n.a.ID, "err", err)
			continue
		}
		_ = s.Emit(n.a.ID, ev)
	}
}

// flagUnresponsive tells the planner once that a person isn't answering
// reminders, instead of nagging them further.
func (s *Service) flagUnresponsive(a workforce.Assignment, count int) {
	s.mu.Lock()
	key := a.ID + "/" + s.now().Format("2006-01-02")
	if s.d.Unresponded[key] {
		s.mu.Unlock()
		return
	}
	s.d.Unresponded[key] = true
	if a.TaskID != "" {
		r := &plan.Risk{ID: s.nextID("R"), Bot: a.Bot, Subject: a.TaskID, Kind: plan.RiskResource, Level: plan.Medium,
			Summary:  fmt.Sprintf("%s 今天已提醒 %d 次仍没有回应（委托 %s）", a.Worker, count, a.ID),
			Options:  []string{"换个方式联系（如当面 / 电话）", "换人", "请主人出面"},
			RaisedBy: "followup", Status: plan.RiskOpen, At: s.now()}
		s.d.Risks[r.ID] = r
	}
	s.saveLocked()
	io := s.bots[a.Bot]
	s.mu.Unlock()
	if io != nil && io.Post != nil {
		io.Post(a.ConvKey, fmt.Sprintf("委托 %s：%s 今天已提醒 %d 次仍没有回应，不再继续催。请决定：换个方式联系、换人，还是请主人出面。", a.ID, a.Worker, count), a.Requester)
	}
}

// escalate sends high risks nobody handled straight to the owner, once.
func (s *Service) escalate(ctx context.Context) {
	now := s.now()
	type msg struct{ bot, text string }
	var out []msg
	s.mu.Lock()
	for _, r := range s.d.Risks {
		if r.Status != plan.RiskOpen || r.Level < plan.High || s.d.Escalated[r.ID] || now.Sub(r.At) < escalateAfter {
			continue
		}
		s.d.Escalated[r.ID] = true
		r.Notified = now
		title := r.Subject
		if t := s.d.Tasks[r.Subject]; t != nil {
			title = t.ID + "「" + t.Title + "」"
		}
		out = append(out, msg{r.Bot, fmt.Sprintf("⚠️ 风险 %s 已 %s 没人处理：%s\n%s\n可选：%s",
			r.ID, now.Sub(r.At).Round(time.Minute), title, r.Summary, strings.Join(r.Options, " / "))})
	}
	if len(out) > 0 {
		s.saveLocked()
	}
	ios := s.bots
	s.mu.Unlock()
	for _, m := range out {
		if io := ios[m.bot]; io != nil && io.NotifyOwner != nil {
			if err := io.NotifyOwner(ctx, m.text); err != nil {
				slog.Warn("pm: escalation failed", "err", err)
			}
		}
	}
}
