package shared

import (
	"testing"
	"time"
)

func TestPeriodAndScope(t *testing.T) {
	d := func(day int) *time.Time { x := time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC); return &x }
	a := Period{From: d(1), Until: d(10)}
	if !a.Contains(*d(1)) || a.Contains(*d(10)) {
		t.Fatal("half-open period")
	}
	if !a.Overlaps(Period{From: d(9)}) || a.Overlaps(Period{From: d(10), Until: d(12)}) || !a.Overlaps(Period{}) {
		t.Fatal("overlaps")
	}
	for rule, actual := range map[string]string{"": "x", "repo:tapnow": "repo:tapnow/web", "*": "anything"} {
		if !ScopeMatches(rule, actual) {
			t.Errorf("%q should cover %q", rule, actual)
		}
	}
	if ScopeMatches("repo:tap", "repo:tapnow") {
		t.Fatal("prefix must stop at a boundary")
	}
	if P0.Weight() <= P1.Weight() {
		t.Fatal("weights")
	}
}
