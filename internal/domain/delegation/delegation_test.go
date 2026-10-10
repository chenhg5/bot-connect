package delegation

import (
	"testing"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

var (
	now   = time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	owner = Actor{UserID: "u-owner", Role: RoleOwner}
	zs    = Actor{UserID: "u-zs", Worker: "zs", Role: RoleVisitor}
	other = Actor{UserID: "u-x", Worker: "x", Role: RoleVisitor}
)

func TestPersonNeedsAReason(t *testing.T) {
	_, _, err := New(Offer{ID: "A1", Item: "I1", Worker: "zs", ToPerson: true, Brief: Brief{Goal: "g"}}, now, owner)
	if err == nil {
		t.Fatal("asking a person without a reason")
	}
	if _, _, err := New(Offer{ID: "A1", Item: "I1", Worker: "agent", Brief: Brief{Goal: "g"}}, now, owner); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New(Offer{ID: "A1", Item: "I1", Worker: "zs", ToPerson: true, Why: NeedDecision, Kind: Decision, Brief: Brief{Goal: "g"}}, now, owner); err == nil {
		t.Fatal("a decision needs options")
	}
}

func TestWorkLifecycleAndParties(t *testing.T) {
	a, _, _ := New(Offer{ID: "A1", Item: "I1", Worker: "zs", ToPerson: true, Why: NeedJudgment, Brief: Brief{Goal: "审设计稿"}}, now, owner)
	if _, err := a.Accept(nil, "", now, other); err == nil {
		t.Fatal("someone else accepted for zs")
	}
	if _, err := a.Verify("", now, owner); err == nil {
		t.Fatal("verify before delivery")
	}
	due := now.Add(72 * time.Hour)
	if _, err := a.Counter(due, "这周排满了", now, zs); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AcceptCounter(now, zs); err == nil {
		t.Fatal("the worker accepted their own counter")
	}
	if evs, err := a.AcceptCounter(now, owner); err != nil || a.Status != Working || !a.Brief.Due.Equal(due) || evs[0].Type != "assignment.counter_accepted" {
		t.Fatalf("%v %s", err, a.Status)
	}
	a.Ask("用哪版 logo？", now, zs)
	if w, ok := a.WaitingOnRequester(); !ok || w != "question" {
		t.Fatal("waiting on our answer")
	}
	a.AnswerQuestion("新版", now, owner)
	a.Deliver("done", []string{"https://figma/x"}, now.Add(time.Hour), zs)
	a.Revise("缺移动端", now, owner)
	a.Deliver("done2", nil, now.Add(2*time.Hour), zs)
	if evs, err := a.Verify("", now, owner); err != nil || evs[0].Type != "assignment.verified" || !a.Status.Terminal() {
		t.Fatal(err)
	}
	if a.DeliveredAt() != now.Add(2*time.Hour) || a.Started() != now {
		t.Fatal("timeline")
	}
}

func TestApprovalIsAnsweredWithAChoice(t *testing.T) {
	a, _, _ := New(Offer{ID: "A2", Item: "I33", Worker: "lisi", ToPerson: true, Why: NeedApproval, Kind: Approval, Brief: Brief{Goal: "批准合并 PR #12"}}, now, owner)
	lisi := Actor{UserID: "u-lisi", Worker: "lisi"}
	if _, err := a.Deliver("x", nil, now, lisi); err == nil {
		t.Fatal("approvals aren't delivered")
	}
	if _, err := a.Answer("maybe", "", now, lisi); err == nil {
		t.Fatal("choice outside options")
	}
	if _, err := a.Answer("approve", "LGTM", now, lisi); err != nil || a.Status != Verified || a.Choice != "approve" {
		t.Fatalf("%v %s", err, a.Status)
	}
	agent, _, _ := New(Offer{ID: "A3", Item: "I31", Worker: "ops", Brief: Brief{Goal: "查日志"}}, now, owner)
	driver := System("driver:agent")
	if _, err := agent.Accept(nil, "", now, driver); err != nil {
		t.Fatal("drivers act for agents")
	}
}
