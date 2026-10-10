package plan

import (
	"fmt"
	"sort"
	"time"
)

// Pace is what the scaffold knows about how a worker usually does: its
// typical lateness (actual / estimated duration) from its track record.
type Pace func(worker string) (lateness float64)

// Detected is a risk found by a rule, before it is stored.
type Detected struct {
	Subject string
	Kind    RiskKind
	Level   Level
	Rule    string
	Summary string
	Options []string
}

// Assess runs the schedule rules over open tasks. Rules are deliberately
// simple and deterministic: they decide *that* the brain must look, not what
// to do about it.
//
//	overdue      — past due and not done                                   → high
//	will_miss    — now + remaining work (estimate × lateness − elapsed)
//	               lands after the due date                                → high
//	thin_slack   — finishes before due, but with less than a quarter of
//	               the estimate to spare                                   → medium
//	unassigned   — nobody on it and due within 1.5× its estimate           → medium (high if it can no longer fit)
//	silent       — active, but no sign of life for max(estimate/4, 2h)     → medium
//	stuck        — blocked for more than 4h                                → medium
//	late_dep     — a dependency is expected to finish too late for this
//	               task to fit before its own due date                     → level of the squeeze
func Assess(tasks []Task, now time.Time, pace Pace) []Detected {
	byID := map[string]Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	finish := map[string]time.Time{}
	var expect func(t Task, depth int) time.Time
	expect = func(t Task, depth int) time.Time {
		if f, ok := finish[t.ID]; ok {
			return f
		}
		start := now
		if depth < 20 {
			for _, d := range t.DependsOn {
				if dt, ok := byID[d]; ok && dt.Open() {
					if f := expect(dt, depth+1); f.After(start) {
						start = f
					}
				}
			}
		}
		f := start.Add(remaining(t, now, pace))
		finish[t.ID] = f
		return f
	}

	var out []Detected
	for _, t := range tasks {
		if !t.Open() {
			continue
		}
		rem := remaining(t, now, pace)
		if t.Due != nil {
			due := *t.Due
			own := now.Add(rem)
			switch {
			case now.After(due):
				out = append(out, Detected{t.ID, RiskSchedule, High, "overdue",
					fmt.Sprintf("「%s」已超过截止时间 %s", t.Title, ago(due, now)),
					[]string{"和执行者确认新的完成时间", "砍范围先交付一部分", "换人或加人", "告诉需求方延期"}})
			case t.Assignee == "" && t.Estimate > 0 && now.Add(t.Estimate).After(due):
				out = append(out, Detected{t.ID, RiskResource, High, "unassigned",
					fmt.Sprintf("「%s」还没人接，按预估 %s 已经来不及在 %s 前完成", t.Title, t.Estimate, due.Format("01-02 15:04")),
					[]string{"立刻分派给有空的 worker", "砍范围", "和需求方商量延期"}})
			case t.Assignee == "" && t.Estimate > 0 && now.Add(t.Estimate*3/2).After(due):
				out = append(out, Detected{t.ID, RiskResource, Medium, "unassigned",
					fmt.Sprintf("「%s」还没人接，离截止只剩 %s（预估 %s）", t.Title, due.Sub(now).Round(time.Minute), t.Estimate),
					[]string{"尽快分派"}})
			case own.After(due):
				out = append(out, Detected{t.ID, RiskSchedule, High, "will_miss",
					fmt.Sprintf("「%s」按当前进度预计 %s 完成，晚于截止 %s", t.Title, own.Format("01-02 15:04"), due.Format("01-02 15:04")),
					[]string{"确认执行者的实际进度和预计时间", "砍范围", "换人或加人", "提前告诉需求方"}})
			case t.Estimate > 0 && due.Sub(own) < t.Estimate/4:
				out = append(out, Detected{t.ID, RiskSchedule, Medium, "thin_slack",
					fmt.Sprintf("「%s」预计 %s 完成，离截止只剩 %s 余量", t.Title, own.Format("01-02 15:04"), due.Sub(own).Round(time.Minute)),
					[]string{"盯紧进度", "准备备选方案"}})
			}
			// A late dependency squeezes this task even if it hasn't started.
			if f := expect(t, 0); f.After(due) && !own.After(due) && !now.After(due) {
				var late []string
				for _, d := range t.DependsOn {
					if dt, ok := byID[d]; ok && dt.Open() {
						late = append(late, dt.Title)
					}
				}
				out = append(out, Detected{t.ID, RiskDependency, High, "late_dep",
					fmt.Sprintf("「%s」依赖的 %v 预计太晚完成，留给它的时间不够（预计 %s，截止 %s）", t.Title, late, f.Format("01-02 15:04"), due.Format("01-02 15:04")),
					[]string{"加速或并行依赖项", "调整截止", "砍范围"}})
			}
		}
		if t.Status == TaskActive {
			quiet := 2 * time.Hour
			if t.Estimate/4 > quiet {
				quiet = t.Estimate / 4
			}
			last := t.LastSignal
			if last.IsZero() {
				last = t.StartedAt
			}
			if !last.IsZero() && now.Sub(last) > quiet {
				out = append(out, Detected{t.ID, RiskSchedule, Medium, "silent",
					fmt.Sprintf("「%s」已经 %s 没有进展消息", t.Title, now.Sub(last).Round(time.Minute)),
					[]string{"问一下执行者进度"}})
			}
		}
		if t.Status == TaskBlocked && !t.BlockedAt.IsZero() && now.Sub(t.BlockedAt) > 4*time.Hour {
			out = append(out, Detected{t.ID, RiskDependency, Medium, "stuck",
				fmt.Sprintf("「%s」卡在「%s」已经 %s", t.Title, t.Blocked, now.Sub(t.BlockedAt).Round(time.Minute)),
				[]string{"找能解决阻塞的人", "换个做法绕过去"}})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Level > out[j].Level })
	return out
}

// remaining estimates the work left on a task.
func remaining(t Task, now time.Time, pace Pace) time.Duration {
	if t.Estimate <= 0 {
		return 0
	}
	f := 1.0
	if pace != nil && t.Assignee != "" {
		if l := pace(t.Assignee); l > 0 {
			f = l
		}
	}
	total := time.Duration(float64(t.Estimate) * f)
	if t.Status == TaskActive && !t.StartedAt.IsZero() {
		total -= now.Sub(t.StartedAt)
	}
	if min := t.Estimate / 10; total < min {
		total = min // started tasks are never "already done" by the clock alone
	}
	return total
}

func ago(t, now time.Time) string {
	d := now.Sub(t).Round(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("%.1f 小时", d.Hours())
	}
	return fmt.Sprintf("%d 天", int(d.Hours()/24))
}
