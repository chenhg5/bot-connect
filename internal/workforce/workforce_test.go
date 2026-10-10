package workforce

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)

func TestStateSourcesAndDecay(t *testing.T) {
	var s State
	s.Put(Fact{Dim: Presence, Value: "available", Source: Observed, At: t0})
	s.Put(Fact{Dim: Presence, Value: "away", Detail: "on leave until Fri", Source: Declared, At: t0})
	s.Put(Fact{Dim: Receptiveness, Value: "low", Detail: "declined t12", Source: Inferred, At: t0, Confidence: 1, Expires: t0.Add(30 * 24 * time.Hour)})
	if f, _ := s.Get(Presence, t0); f.Value != "away" {
		t.Fatalf("declared must beat observed, got %s", f.Value)
	}
	f, _ := s.Get(Receptiveness, t0)
	if f.Confidence > 0.8 || f.Expires.Sub(f.At) > MaxInferredTTL {
		t.Fatalf("inferred facts must be capped: %+v", f)
	}
	if sum := s.Summary(t0); !strings.Contains(sum, "receptiveness low?") || !strings.Contains(sum, "presence away") {
		t.Fatalf("summary %q", sum)
	}
	later := t0.Add(25 * time.Hour)
	if _, ok := s.Get(Receptiveness, later); ok {
		t.Fatal("a guess about a person must expire within a day")
	}
	s.Prune(later)
	if len(s.Facts) != 2 {
		t.Fatalf("prune kept %d facts", len(s.Facts))
	}
}

func TestUnreachableIgnoresGuesses(t *testing.T) {
	var s State
	s.Put(Fact{Dim: Presence, Value: "offline", Source: Inferred, At: t0})
	if u, _ := s.Unreachable(t0); u {
		t.Fatal("a guess must not block work")
	}
	s.Put(Fact{Dim: Presence, Value: "unavailable", Detail: "quota exhausted", Source: Observed, At: t0})
	if u, why := s.Unreachable(t0); !u || !strings.Contains(why, "quota") {
		t.Fatalf("observed outage must block: %v %s", u, why)
	}
}

func TestAssignmentLifecycle(t *testing.T) {
	a := &Assignment{ID: "a1"}
	step := func(ty EventType, want Status) {
		t.Helper()
		if err := a.Apply(Event{Type: ty, At: t0, Note: "q", Result: "r"}); err != nil {
			t.Fatalf("%s: %v", ty, err)
		}
		if a.Status != want {
			t.Fatalf("after %s: %s, want %s", ty, a.Status, want)
		}
	}
	step(EvOffer, Offered)
	step(EvAccept, Accepted)
	step(EvStart, Working)
	step(EvAsk, Blocked)
	if a.Question != "q" {
		t.Fatal("question not kept")
	}
	step(EvAnswer, Working)
	step(EvDeliver, Delivered)
	step(EvRevise, Revising)
	step(EvDeliver, Delivered)
	step(EvVerify, Verified)
	if err := a.Apply(Event{Type: EvCancel}); err == nil {
		t.Fatal("a verified assignment can't be cancelled")
	}
	if a.Status.A2A() != "completed" {
		t.Fatal(a.Status.A2A())
	}

	b := &Assignment{ID: "b"}
	_ = b.Apply(Event{Type: EvOffer, At: t0})
	due := t0.Add(48 * time.Hour)
	if err := b.Apply(Event{Type: EvCounter, Due: &due, At: t0}); err != nil || b.Status != Countered || !b.Brief.Due.Equal(due) {
		t.Fatalf("counter: %v %s", err, b.Status)
	}
	if err := b.Apply(Event{Type: EvStart}); err == nil {
		t.Fatal("can't start before the counter is settled")
	}
	_ = b.Apply(Event{Type: EvNudge, At: t0.Add(time.Hour)})
	_ = b.Apply(Event{Type: EvNudge, At: t0.Add(2 * time.Hour)})
	if n, last := b.Nudges(t0); n != 2 || !last.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("nudges %d %v", n, last)
	}
	if err := b.Apply(Event{Type: EvDecline}); err != nil || !b.Status.Terminal() {
		t.Fatalf("decline: %v %s", err, b.Status)
	}
}
