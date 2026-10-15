package app

import (
	"fmt"
	"strings"
	"time"
)

// Working rhythm: when non-urgent wake-ups may happen, and how many a day.
// Urgent signals (messages to the bot, high risks, the brain's own timed
// reminders) are not held by either.

// InWindow reports whether t is inside the working hours and days (no
// window configured = always).
func (p WakePolicy) InWindow(t time.Time) bool {
	return p.dayOK(t) && p.hourOK(t)
}

func (p WakePolicy) dayOK(t time.Time) bool {
	d := strings.ToLower(strings.TrimSpace(p.Days))
	switch d {
	case "", "all", "everyday", "daily":
		return true
	case "weekdays":
		return t.Weekday() != time.Saturday && t.Weekday() != time.Sunday
	case "weekends":
		return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday
	}
	name := strings.ToLower(t.Weekday().String()[:3])
	for _, x := range strings.Split(d, ",") {
		if strings.TrimSpace(x)[:min(3, len(strings.TrimSpace(x)))] == name {
			return true
		}
	}
	return false
}

func (p WakePolicy) hourOK(t time.Time) bool {
	from, to, ok := parseHours(p.Hours)
	if !ok {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if from <= to {
		return m >= from && m < to
	}
	return m >= from || m < to // overnight
}

// NextWindow is the first moment at or after t inside the window.
func (p WakePolicy) NextWindow(t time.Time) time.Time {
	if p.InWindow(t) {
		return t
	}
	from, _, ok := parseHours(p.Hours)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	for i := 0; i < 8; i++ {
		start := day.AddDate(0, 0, i)
		if ok {
			start = start.Add(time.Duration(from) * time.Minute)
		}
		if start.Before(t) {
			if p.InWindow(t) {
				return t
			}
			continue
		}
		if p.InWindow(start) {
			return start
		}
	}
	return t
}

func parseHours(s string) (from, to int, ok bool) {
	var h1, m1, h2, m2 int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d-%d:%d", &h1, &m1, &h2, &m2); err != nil {
		return 0, 0, false
	}
	return h1*60 + m1, h2*60 + m2, true
}

func dayKey(t time.Time) string { return t.Format("2006-01-02") }

// wakesToday counts the brain's wake-ups today.
func (a *App) wakesToday() int {
	n := 0
	a.Store.Read(func(s *State) { n = s.WakeCounts[dayKey(a.now())] })
	return n
}

// OverBudget: today's non-urgent wake-ups are used up.
func (a *App) OverBudget() bool {
	return a.Policy.DailyBudget > 0 && a.wakesToday() >= a.Policy.DailyBudget
}

// countWake records a wake-up (and forgets days older than two weeks).
func countWake(s *State, now time.Time) {
	if s.WakeCounts == nil {
		s.WakeCounts = map[string]int{}
	}
	s.WakeCounts[dayKey(now)]++
	cutoff := dayKey(now.AddDate(0, 0, -14))
	for k := range s.WakeCounts {
		if k < cutoff {
			delete(s.WakeCounts, k)
		}
	}
}

// rhythmLine says how the day's wake-ups stand, for the overview.
func (a *App) rhythmLine() string {
	n := a.wakesToday()
	line := fmt.Sprintf("今天已唤醒 %d 次", n)
	if a.Policy.DailyBudget > 0 {
		line += fmt.Sprintf("（非紧急预算 %d）", a.Policy.DailyBudget)
	}
	if a.Policy.Hours != "" || a.Policy.Days != "" {
		line += fmt.Sprintf("；工作时段 %s %s", firstNonEmpty(a.Policy.Days, "每天"), firstNonEmpty(a.Policy.Hours, "全天"))
	}
	return line + "\n"
}
