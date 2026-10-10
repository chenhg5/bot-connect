package inbox

import (
	"testing"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

var now = time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)

func TestLifecycle(t *testing.T) {
	s, err := New("S1", "feishu:om_1", DirectMessage, "dm", now)
	if err != nil {
		t.Fatal(err)
	}
	s.MarkSeen(now)
	if s.Status != Seen {
		t.Fatal(s.Status)
	}
	if _, err := s.Transition(Ignored, "", nil, now, System("t")); err == nil {
		t.Fatal("ignoring needs a reason")
	}
	if _, err := s.Transition(Deferred, "", &now, now, System("t")); err == nil {
		t.Fatal("deferring needs a future time")
	}
	later := now.Add(time.Hour)
	if _, err := s.Transition(Deferred, "先处理延期风险", &later, now, System("t")); err != nil {
		t.Fatal(err)
	}
	if s.DueAgain(now.Add(30*time.Minute)) || !s.DueAgain(later) || s.Status != Pending {
		t.Fatal("deferred comes back on time")
	}
	s.Transition(Handled, "", nil, later, System("t"))
	if _, err := s.Transition(Handling, "", nil, later, System("t")); err == nil {
		t.Fatal("terminal signals are never reopened")
	}
}

func TestScore(t *testing.T) {
	dm, _ := New("a", "a", DirectMessage, "dm", now)
	risk, _ := New("b", "b", Risk, "will_miss", now)
	risk.Level = 2
	if dm.Score(2, now) >= risk.Score(8, now) {
		t.Fatal("a high risk on a P0 project outranks a fresh message")
	}
	if dm.Score(2, now.Add(5*time.Hour)) <= dm.Score(2, now) {
		t.Fatal("waiting raises the score")
	}
}
