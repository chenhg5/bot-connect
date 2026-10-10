package planning

import (
	"testing"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

var now = time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)

func TestGraphAndCompletion(t *testing.T) {
	by := Actor{UserID: "u", Role: RoleOwner}
	mk := func(id ItemID, deps ...ItemID) Item {
		it, _, err := New(id, Spec{Project: "P", Title: string(id), DependsOn: deps, Acceptance: []string{"a", " "}}, now, by)
		if err != nil {
			t.Fatal(err)
		}
		return it
	}
	a, b := mk("A"), mk("B", "A")
	if len(a.Acceptance) != 1 {
		t.Fatal("blank criteria dropped")
	}
	g := Graph{Items: map[ItemID]Item{"A": a, "B": b}}
	if err := g.Validate("A", "P", "", []ItemID{"B"}); err == nil {
		t.Fatal("cycle accepted")
	}
	if err := g.Validate("C", "P", "", []ItemID{"Z"}); err == nil {
		t.Fatal("unknown dependency")
	}
	if g.Ready("B") || !g.Ready("A") || len(g.Dependents("A")) != 1 {
		t.Fatal("readiness")
	}
	if _, err := b.Complete(false, now, Actor{Role: RoleMember, UserID: "m"}); err == nil {
		t.Fatal("a member can't just close an item")
	}
	if _, err := b.Complete(true, now, System("rule")); err != nil || b.Status != Done {
		t.Fatal(err)
	}
	if _, _, err := New("X", Spec{Project: "P", Title: "x", Needs: []Need{{Kind: "magic"}}}, now, by); err == nil {
		t.Fatal("unknown need kind")
	}
}
