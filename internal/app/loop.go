package app

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/inbox"
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
	a.raiseFindings(ctx)
	a.tickInbox(ctx)
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
		text := fmt.Sprintf("%s（%s，%s）两天没有回应，已过期，事项回到待分派。", as.ID, as.Worker, oneLine(as.Brief.Goal, 60))
		_, _, _ = a.Ingest(ctx, SignalSpec{Dedupe: "assignment:" + string(as.ID) + ":expired", Source: inbox.WorkerReply, Reason: "expired",
			Actor: System("rule:expire"), Summary: text, Body: text, ReplyTo: inbox.ReplyTo{Conv: as.Conv},
			Refs: inbox.Refs{Project: as.Project, Item: as.Item, Assignment: as.ID, Worker: as.Worker}})
	}
}

// delegationStub carries refs to a driver for messages not tied to an assignment.
func delegationStub(r inbox.Refs) delegation.Assignment {
	return delegation.Assignment{ID: r.Assignment, Item: r.Item, Project: r.Project}
}
