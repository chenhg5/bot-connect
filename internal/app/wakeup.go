package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/inbox"
	"github.com/chenhg5/bot-connect/internal/domain/observe"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/schedule"
)

// Wakeups: the brain asks to be woken later — once ("周五 15:00 跟 Dana 确认
// 审批") or on a rhythm ("每周一 9:00 出官网项目的周报"). At the time, a
// signal carrying the note it left itself comes into the inbox and wakes it.
// The bot's own clock drives them, so they can be tested in virtual time.

// Wakeup is one standing reminder to the brain.
type Wakeup struct {
	ID        string    `json:"id"`
	Note      string    `json:"note"`            // what to do then, self-contained
	When      string    `json:"when,omitempty"`  // one-shot, in the words given ("周五 15:00")
	Every     string    `json:"every,omitempty"` // recurring spec (cron, @every 1d, @daily, TZ=… prefix)
	Project   ProjectID `json:"project,omitempty"`
	Item      ItemID    `json:"item,omitempty"`
	Next      time.Time `json:"next"`
	Last      time.Time `json:"last,omitempty"`
	Runs      int       `json:"runs,omitempty"`
	By        Actor     `json:"by"`
	CreatedAt time.Time `json:"created_at"`
	Done      bool      `json:"done,omitempty"`
	// If: looked at when due (by code, no brain turn); an empty result or a
	// failure skips this time. Its output goes into the wake-up.
	If *observe.Probe `json:"if,omitempty"`
	// Window: keep to the working hours and days (non-urgent rhythms).
	Window bool `json:"window,omitempty"`
	// Owner: "" for the brain's own, "config" for the principal's routines
	// (the brain can't cancel those).
	Owner    string `json:"owner,omitempty"`
	Skips    int    `json:"skips,omitempty"`
	LastSkip string `json:"last_skip,omitempty"`
}

// WakeupSpec is what a wakeup is made from.
type WakeupSpec struct {
	Note, When, Every string
	Project           ProjectID
	Item              ItemID
	If                *observe.Probe
	Window            bool
}

func (w Wakeup) Describe() string {
	when := w.When
	if w.Every != "" {
		when = "每次 " + w.Every
	}
	s := fmt.Sprintf("%s %s（下次 %s）：%s", w.ID, when, w.Next.Format("01-02 Mon 15:04"), oneLine(w.Note, 80))
	if w.Project != "" {
		s += " [" + string(w.Project) + "]"
	}
	if w.If != nil {
		s += "；条件：" + w.If.String()
	}
	if w.Window {
		s += "；只在工作时段"
	}
	if w.Owner == "config" {
		s += "；委托人的例行"
	}
	if w.Runs > 0 || w.Skips > 0 {
		s += fmt.Sprintf("；醒过 %d 次，跳过 %d 次", w.Runs, w.Skips)
		if w.LastSkip != "" {
			s += "（上次跳过：" + w.LastSkip + "）"
		}
	}
	return s
}

// MaxWakeups bounds standing wakeups: each one costs a brain turn.
const MaxWakeups = 50

// MinWakeupEvery bounds recurring wakeups.
const MinWakeupEvery = 15 * time.Minute

// AddWakeup schedules a wakeup: when (one-shot, in the person's words or a
// duration) or every (a recurring spec).
func (a *App) AddWakeup(ctx context.Context, by Actor, note, when, every string, pid ProjectID, item ItemID) (Wakeup, error) {
	return a.ScheduleWakeup(ctx, by, WakeupSpec{Note: note, When: when, Every: every, Project: pid, Item: item, Window: every != ""})
}

// ScheduleWakeup schedules a wakeup from a spec.
func (a *App) ScheduleWakeup(ctx context.Context, by Actor, sp WakeupSpec) (Wakeup, error) {
	note, when, every, pid, item := sp.Note, sp.When, sp.Every, sp.Project, sp.Item
	if err := requirePrivileged(by, "schedule wakeups"); err != nil {
		return Wakeup{}, err
	}
	if sp.If != nil {
		if err := sp.If.Validate(); err != nil {
			return Wakeup{}, fmt.Errorf("if: %w", err)
		}
	}
	note = strings.TrimSpace(note)
	if note == "" {
		return Wakeup{}, Invalid("leave yourself a note: what to do when woken, self-contained")
	}
	if (when == "") == (every == "") {
		return Wakeup{}, Invalid("give either when (once: 周五 15:00 / 明天上午 / 2h) or every (cron \"0 9 * * 1\", @every 1d, @daily)")
	}
	now := a.now()
	var next time.Time
	switch {
	case when != "":
		t, err := ParseWhen(when, now)
		if err != nil {
			return Wakeup{}, Invalid("when: %v", err)
		}
		if t == nil || !t.After(now) {
			return Wakeup{}, Invalid("when %q is not in the future", when)
		}
		next = *t
	default:
		n, once, err := schedule.Next(every, now)
		if err != nil {
			return Wakeup{}, Invalid("every: %v", err)
		}
		if once {
			return Wakeup{}, Invalid("every is for rhythms; for once use when")
		}
		if n2, _, _ := schedule.Next(every, n); n2.Sub(n) < MinWakeupEvery {
			return Wakeup{}, Invalid("at most every %s", MinWakeupEvery)
		}
		next = n
	}
	var out Wakeup
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		active := 0
		for _, w := range s.Wakeups {
			if !w.Done {
				active++
			}
		}
		if active >= MaxWakeups {
			return nil, Conflict("%d wakeups are already set; cancel some first", active)
		}
		if pid != "" {
			if _, ok := s.Projects[pid]; !ok {
				return nil, NotFound("no project %s", pid)
			}
		}
		out = Wakeup{ID: s.NextID("WK"), Note: note, When: when, Every: every, Project: pid, Item: item, Next: next, By: by, CreatedAt: now,
			If: sp.If, Window: sp.Window}
		s.Wakeups[out.ID] = out
		return []Event{NewEvent("wakeup.added", "wakeup:"+out.ID, now, by, "next", next, "every", every)}, nil
	})
	return out, err
}

// CancelWakeup stops a wakeup.
func (a *App) CancelWakeup(ctx context.Context, by Actor, id string) error {
	if err := requirePrivileged(by, "cancel wakeups"); err != nil {
		return err
	}
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		w, ok := s.Wakeups[id]
		if !ok || w.Done {
			return nil, NotFound("no active wakeup %s", id)
		}
		if w.Owner == "config" && by.Role != RolePrincipal {
			return nil, Forbidden("%s is one of the principal's routines (configuration); ask them", id)
		}
		w.Done = true
		s.Wakeups[id] = w
		return []Event{NewEvent("wakeup.cancelled", "wakeup:"+id, a.now(), by)}, nil
	})
	return err
}

// Wakeups lists active wakeups, soonest first.
func (a *App) Wakeups() []Wakeup {
	var out []Wakeup
	a.Store.Read(func(s *State) {
		for _, w := range s.Wakeups {
			if !w.Done {
				out = append(out, w)
			}
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Next.Before(out[j].Next) })
	return out
}

// tickWakeups raises due wakeups as signals and moves recurring ones on.
// Rhythms keep to the working window and the day's budget; a condition is
// looked at by code first, and an empty answer skips this time.
func (a *App) tickWakeups(ctx context.Context) {
	now := a.now()
	var due []Wakeup
	a.Store.Read(func(s *State) {
		for _, w := range s.Wakeups {
			if !w.Done && !now.Before(w.Next) {
				due = append(due, w)
			}
		}
	})
	sort.Slice(due, func(i, j int) bool { return due[i].Next.Before(due[j].Next) })
	for _, w := range due {
		switch {
		case w.Window && !a.Policy.InWindow(now):
			a.moveWakeup(ctx, w.ID, a.Policy.NextWindow(now), "")
			continue
		case w.Window && a.OverBudget():
			tomorrow := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
			a.moveWakeup(ctx, w.ID, a.Policy.NextWindow(tomorrow), "")
			continue
		}
		gate := ""
		if w.If != nil {
			out, err := a.Look(ctx, System("wakeup"), *w.If)
			if err != nil || strings.TrimSpace(out) == "" {
				why := "条件没有结果"
				if err != nil {
					why = "条件看不了：" + oneLine(err.Error(), 100)
				}
				a.advanceWakeup(ctx, w, now, why)
				continue
			}
			gate = out
		}
		a.advanceWakeup(ctx, w, now, "")
		body := "到点了（你之前给自己定的）：" + w.Note
		if w.Owner == "config" {
			body = "例行（委托人定的作息）：" + w.Note
		}
		if gate != "" {
			body += "\n条件 " + w.If.String() + " 的结果：\n" + clipRunes(gate, 2000)
		}
		if w.Every != "" {
			body += fmt.Sprintf("\n（周期唤醒 %s，第 %d 次", w.Every, w.Runs+1)
			if w.Owner != "config" {
				body += "；不再需要就 wakeup cancel " + w.ID
			}
			body += "）"
		}
		if w.Next.Before(now.Add(-time.Hour)) {
			body += fmt.Sprintf("\n（本该 %s 叫醒，晚了 %s）", w.Next.Format("01-02 15:04"), now.Sub(w.Next).Round(time.Minute))
		}
		_, _, _ = a.Ingest(ctx, SignalSpec{Dedupe: fmt.Sprintf("wakeup:%s:%d", w.ID, w.Runs+1), Source: inbox.Wakeup, Reason: "wakeup",
			Actor: System("wakeup"), Summary: oneLine(w.Note, 100), Body: body, Level: 1,
			Refs: inbox.Refs{Project: w.Project, Item: w.Item}})
	}
}

// advanceWakeup records a run (or a skip) and moves a wakeup on.
func (a *App) advanceWakeup(ctx context.Context, w Wakeup, now time.Time, skip string) {
	_, _ = a.commit(ctx, func(s *State) ([]Event, error) {
		x, ok := s.Wakeups[w.ID]
		if !ok || x.Done {
			return nil, nil
		}
		if skip != "" {
			x.Skips++
			x.LastSkip = now.Format("01-02 15:04") + " " + skip
		} else {
			x.Last, x.Runs = now, x.Runs+1
		}
		if x.Every == "" {
			x.Done = true
		} else if n, _, err := schedule.Next(x.Every, now); err == nil {
			x.Next = n
		} else {
			x.Done = true
		}
		s.Wakeups[w.ID] = x
		return nil, nil
	})
}

// moveWakeup postpones one occurrence (outside the window, over budget).
func (a *App) moveWakeup(ctx context.Context, id string, to time.Time, _ string) {
	_, _ = a.commit(ctx, func(s *State) ([]Event, error) {
		if x, ok := s.Wakeups[id]; ok && !x.Done && to.After(x.Next) {
			x.Next = to
			s.Wakeups[id] = x
		}
		return nil, nil
	})
}

// SyncRoutines makes the principal's configured routines exist as
// wakeups (owner "config"), keeping their schedule when unchanged and
// ending those no longer configured.
func (a *App) SyncRoutines(ctx context.Context, rs []WakeupSpec, names []string) error {
	now := a.now()
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		keep := map[string]bool{}
		for i, r := range rs {
			id := "RT-" + names[i]
			keep[id] = true
			cur, ok := s.Wakeups[id]
			if ok && !cur.Done && cur.Every == r.Every && cur.Note == r.Note {
				cur.If, cur.Window = r.If, r.Window
				s.Wakeups[id] = cur
				continue
			}
			next, once, err := schedule.Next(r.Every, now)
			if err != nil || once {
				return nil, Invalid("routine %s: every %q: %v", names[i], r.Every, err)
			}
			s.Wakeups[id] = Wakeup{ID: id, Note: r.Note, Every: r.Every, Project: r.Project, Next: next, By: System("config"),
				CreatedAt: now, If: r.If, Window: r.Window, Owner: "config"}
		}
		for id, w := range s.Wakeups {
			if w.Owner == "config" && !keep[id] && !w.Done {
				w.Done = true
				s.Wakeups[id] = w
			}
		}
		return nil, nil
	})
	return err
}

// wakeupLine lists the next few wakeups for the overview.
func (a *App) wakeupLine(n int) string {
	ws := a.Wakeups()
	if len(ws) == 0 {
		return ""
	}
	var parts []string
	for i, w := range ws {
		if i == n {
			parts = append(parts, fmt.Sprintf("…共 %d 个", len(ws)))
			break
		}
		parts = append(parts, fmt.Sprintf("%s %s %s", w.ID, w.Next.Format("01-02 15:04"), oneLine(w.Note, 30)))
	}
	return "接下来的唤醒：" + strings.Join(parts, "；") + "\n"
}
