package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/inbox"
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
	return s
}

// MaxWakeups bounds standing wakeups: each one costs a brain turn.
const MaxWakeups = 50

// MinWakeupEvery bounds recurring wakeups.
const MinWakeupEvery = 15 * time.Minute

// AddWakeup schedules a wakeup: when (one-shot, in the person's words or a
// duration) or every (a recurring spec).
func (a *App) AddWakeup(ctx context.Context, by Actor, note, when, every string, pid ProjectID, item ItemID) (Wakeup, error) {
	if err := requirePrivileged(by, "schedule wakeups"); err != nil {
		return Wakeup{}, err
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
		out = Wakeup{ID: s.NextID("WK"), Note: note, When: when, Every: every, Project: pid, Item: item, Next: next, By: by, CreatedAt: now}
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
func (a *App) tickWakeups(ctx context.Context) {
	now := a.now()
	var fire []Wakeup
	_, _ = a.commit(ctx, func(s *State) ([]Event, error) {
		for id, w := range s.Wakeups {
			if w.Done || now.Before(w.Next) {
				continue
			}
			fired := w
			w.Last, w.Runs = now, w.Runs+1
			if w.Every == "" {
				w.Done = true
			} else if n, _, err := schedule.Next(w.Every, now); err == nil {
				w.Next = n
			} else {
				w.Done = true
			}
			s.Wakeups[id] = w
			fire = append(fire, fired)
		}
		return nil, nil
	})
	sort.Slice(fire, func(i, j int) bool { return fire[i].Next.Before(fire[j].Next) })
	for _, w := range fire {
		body := "到点了（你之前给自己定的）：" + w.Note
		if w.Every != "" {
			body += fmt.Sprintf("\n（周期唤醒 %s，第 %d 次；不再需要就 wakeup cancel %s）", w.Every, w.Runs+1, w.ID)
		}
		if w.Next.Before(now.Add(-time.Hour)) {
			body += fmt.Sprintf("\n（本该 %s 叫醒，晚了 %s）", w.Next.Format("01-02 15:04"), now.Sub(w.Next).Round(time.Minute))
		}
		_, _, _ = a.Ingest(ctx, SignalSpec{Dedupe: fmt.Sprintf("wakeup:%s:%d", w.ID, w.Runs+1), Source: inbox.Wakeup, Reason: "wakeup",
			Actor: System("wakeup"), Summary: oneLine(w.Note, 100), Body: body, Level: 1,
			Refs: inbox.Refs{Project: w.Project, Item: w.Item}})
	}
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
