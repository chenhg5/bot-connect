package plan

import (
	"testing"
	"time"
)

var now = time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)

func at(h float64) *time.Time { t := now.Add(time.Duration(h * float64(time.Hour))); return &t }

func rules(ds []Detected, id string) map[string]Level {
	out := map[string]Level{}
	for _, d := range ds {
		if d.Subject == id {
			out[d.Rule] = d.Level
		}
	}
	return out
}

func TestAssessScheduleRules(t *testing.T) {
	tasks := []Task{
		{ID: "late", Title: "late", Status: TaskActive, Assignee: "w", Due: at(-1), Estimate: time.Hour, StartedAt: now.Add(-3 * time.Hour), LastSignal: now},
		{ID: "miss", Title: "miss", Status: TaskActive, Assignee: "slow", Due: at(3), Estimate: 4 * time.Hour, StartedAt: now, LastSignal: now},
		{ID: "thin", Title: "thin", Status: TaskActive, Assignee: "w", Due: at(4.5), Estimate: 4 * time.Hour, StartedAt: now, LastSignal: now},
		{ID: "ok", Title: "ok", Status: TaskActive, Assignee: "w", Due: at(48), Estimate: 4 * time.Hour, StartedAt: now, LastSignal: now},
		{ID: "nobody", Title: "nobody", Status: TaskTodo, Due: at(5), Estimate: 4 * time.Hour},
		{ID: "toolate", Title: "toolate", Status: TaskTodo, Due: at(2), Estimate: 4 * time.Hour},
		{ID: "quiet", Title: "quiet", Status: TaskActive, Assignee: "w", Due: at(100), Estimate: 2 * time.Hour, StartedAt: now.Add(-5 * time.Hour), LastSignal: now.Add(-3 * time.Hour)},
		{ID: "stuck", Title: "stuck", Status: TaskBlocked, Blocked: "等设计稿", BlockedAt: now.Add(-5 * time.Hour)},
		{ID: "dep", Title: "dep", Status: TaskActive, Assignee: "w", Due: at(30), Estimate: 28 * time.Hour, StartedAt: now, LastSignal: now},
		{ID: "after", Title: "after", Status: TaskTodo, Assignee: "w", Due: at(30), Estimate: 4 * time.Hour, DependsOn: []string{"dep"}},
		{ID: "closed", Title: "closed", Status: TaskDone, Due: at(-10)},
	}
	pace := func(w string) float64 {
		if w == "slow" {
			return 1.5 // this worker usually takes 50% longer than estimated
		}
		return 1
	}
	ds := Assess(tasks, now, pace)
	want := map[string]map[string]Level{
		"late":    {"overdue": High},
		"miss":    {"will_miss": High},
		"thin":    {"thin_slack": Medium},
		"ok":      {},
		"nobody":  {"unassigned": Medium},
		"toolate": {"unassigned": High},
		"quiet":   {"silent": Medium},
		"stuck":   {"stuck": Medium},
		"after":   {"late_dep": High},
		"closed":  {},
	}
	for id, w := range want {
		got := rules(ds, id)
		if len(got) != len(w) {
			t.Errorf("%s: got %v, want %v", id, got, w)
			continue
		}
		for r, l := range w {
			if got[r] != l {
				t.Errorf("%s: rule %s = %v, want %v (all: %v)", id, r, got[r], l, got)
			}
		}
	}
	if ds[0].Level != High {
		t.Fatal("high risks first")
	}
}
