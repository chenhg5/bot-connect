package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/attention"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
)

// Follow-up timing for people (agents are followed by their own timeouts).
const (
	OfferNudgeAfter  = 4 * time.Hour  // no answer to an offer → reminder
	OfferExpireAfter = 48 * time.Hour // still nothing → expired, back to planning
	EscalateAfter    = 2 * time.Hour  // a high signal nobody handled → straight to the owner
)

// Run ticks the control loop until ctx ends.
func (a *App) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.Tick(ctx)
		}
	}
}

// Tick is one round: follow up with people, then raise new or worse signals
// to the brain, then escalate high signals nobody handled. It never decides
// what to do about a problem; it makes sure someone hears about it in time.
func (a *App) Tick(ctx context.Context) {
	a.followUps(ctx)
	a.raise(ctx)
}

func (a *App) followUps(ctx context.Context) {
	w := a.World(ctx)
	ids := make([]AssignmentID, 0, len(w.Assignments))
	for id := range w.Assignments {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		as := w.Assignments[id]
		wk := w.Workers[as.Worker]
		if !as.Open() || wk.Kind != workforce.Human || wk.ID == Owner {
			continue
		}
		note := ""
		switch as.Status {
		case delegation.Offered:
			age := w.Now.Sub(as.CreatedAt)
			if age > OfferExpireAfter {
				a.expire(ctx, as)
				continue
			}
			if age > OfferNudgeAfter {
				note = "还没收到你是否接下的回复"
			}
		case delegation.Accepted, delegation.Working, delegation.Revising:
			if as.Brief.Due == nil {
				continue
			}
			last := as.LastLife
			if last.IsZero() {
				last = as.CreatedAt
			}
			left := as.Brief.Due.Sub(w.Now)
			switch {
			case left < 0 && w.Now.Sub(last) > 2*time.Hour:
				note = "已经过了截止时间（" + fmtDue(as.Brief.Due) + "），现在进展如何？"
			case left > 0 && left < 24*time.Hour && w.Now.Sub(last) > 12*time.Hour:
				note = "截止 " + fmtDue(as.Brief.Due) + "，进展如何？有卡点可以直接说"
			}
		}
		if note == "" {
			continue
		}
		if err := wk.Norms.NudgeAllowed(as.Nudges(), w.Now); err != nil {
			continue // outside hours, too soon, or budget spent (the agenda tells the planner)
		}
		u := workforce.Reminder
		if as.Brief.Due != nil && w.Now.After(*as.Brief.Due) {
			u = workforce.Urgent
		}
		d := a.driverFor(wk)
		if d == nil || d.Notify(ctx, wk, as, Notice{Kind: "nudge", Text: note, Urgency: u}) != nil {
			continue
		}
		_, _ = a.commit(ctx, func(s *State) ([]Event, error) {
			x := s.Assignments[as.ID]
			evs := x.Nudged(note, a.now())
			s.Assignments[as.ID] = x
			return evs, nil
		})
	}
}

func (a *App) expire(ctx context.Context, as delegation.Assignment) {
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		x := s.Assignments[as.ID]
		evs, err := x.Expire(a.now())
		if err != nil {
			return nil, err
		}
		s.Assignments[as.ID] = x
		return append(evs, syncItem(s, x, "expire", a.now(), System("rule:expire"))...), nil
	})
	if err == nil {
		a.wake(ctx, Trigger{Kind: "event", Conv: as.Conv, Project: as.Project, About: as.Requester,
			Text: fmt.Sprintf("%s（%s，%s）两天没有回应，已过期，事项回到待分派。", as.ID, as.Worker, oneLine(as.Brief.Goal, 60))})
	}
}

// raise wakes the brain once per new (or worsened) signal, grouped by
// project, and escalates high signals nobody handled within EscalateAfter.
func (a *App) raise(ctx context.Context) {
	ag, c := a.Agenda(ctx)
	now := c.Now
	current := map[string]attention.Scored{}
	for _, tier := range [][]attention.Scored{ag.Now, ag.Today, ag.Watch} {
		for _, s := range tier {
			current[s.Key] = s
		}
	}
	var fresh []attention.Scored
	var escalate []attention.Scored
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		for k := range s.Raised {
			if _, ok := current[k]; !ok {
				delete(s.Raised, k) // gone (or handled): may be raised again later
			}
		}
		for k, sig := range current {
			if sig.Level < attention.Medium {
				continue
			}
			r, seen := s.Raised[k]
			switch {
			case !seen || sig.Level > r.Level:
				s.Raised[k] = Raised{Level: sig.Level, At: now}
				fresh = append(fresh, sig)
			case sig.Level == attention.High && !r.Escalated && now.Sub(r.At) >= EscalateAfter:
				r.Escalated = true
				s.Raised[k] = r
				escalate = append(escalate, sig)
			}
		}
		return nil, nil
	})
	if err != nil {
		return
	}
	byProject := map[ProjectID][]attention.Signal{}
	for _, s := range fresh {
		byProject[s.Project] = append(byProject[s.Project], s.Signal)
	}
	projects := make([]ProjectID, 0, len(byProject))
	for p := range byProject {
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i] < projects[j] })
	for _, p := range projects {
		sigs := byProject[p]
		sort.Slice(sigs, func(i, j int) bool { return sigs[i].Level > sigs[j].Level })
		var lines []string
		for _, s := range sigs {
			lines = append(lines, fmt.Sprintf("- [%s %s] %s", s.Kind, s.Level, s.Summary))
		}
		a.wake(ctx, Trigger{Kind: "rule", Conv: c.Projects[p].Home, Project: p, Signals: sigs,
			Text: "需要注意：\n" + strings.Join(lines, "\n")})
	}
	for _, s := range escalate {
		a.wake(ctx, Trigger{Kind: "rule", Conv: "", Project: s.Project, Signals: []attention.Signal{s.Signal},
			Text: fmt.Sprintf("⚠️ 已 %s 没有处理：%s", EscalateAfter, s.Summary)})
	}
}
