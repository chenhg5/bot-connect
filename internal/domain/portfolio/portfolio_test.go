package portfolio

import (
	"strings"
	"testing"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

var (
	now   = time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	owner = Actor{UserID: "u1", Role: RoleOwner}
)

func day(d int) *time.Time { t := time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC); return &t }

func TestMembershipsAndCard(t *testing.T) {
	p, _, err := New("P1", "", "官网改版", P1, Period{From: day(1), Until: day(31)}, now, owner)
	if err != nil || p.Parent != OrgProject {
		t.Fatalf("%v %+v", err, p)
	}
	if _, err := p.AddMember(Membership{Worker: "zs", Role: "设计负责人", Period: Period{From: day(1), Until: day(20)}}, now, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := p.AddMember(Membership{Worker: "zs", Role: "设计负责人", Period: Period{From: day(10)}}, now, owner); err == nil {
		t.Fatal("overlapping same role accepted")
	}
	if _, err := p.AddMember(Membership{Worker: "zs", Role: "设计负责人", Period: Period{From: day(20)}}, now, owner); err != nil {
		t.Fatalf("adjacent period: %v", err)
	}
	if got := p.Holding("设计负责人", now); len(got) != 1 {
		t.Fatal(got)
	}
	if _, err := p.UpdateCard(CardDelta{AddDecision: strings.Repeat("长", CardLimit)}, now, owner); err == nil {
		t.Fatal("card limit")
	}
	if _, err := p.UpdateCard(CardDelta{AddConvention: "对外发布需主人批准"}, now, owner); err != nil || len(p.Card.Conventions) != 1 {
		t.Fatal(err)
	}
	if e := p.Elapsed(now); e < 0.4 || e > 0.45 {
		t.Fatalf("elapsed %v", e)
	}
	org, _, _ := New(OrgProject, "", "公司", P2, Period{}, now, owner)
	if _, err := org.Transition(Cancelled, now, owner); err == nil {
		t.Fatal("root can't be cancelled")
	}
}

func TestChainRulesAndApprovers(t *testing.T) {
	org, _, _ := New(OrgProject, "", "公司", P2, Period{}, now, owner)
	org.SetPolicy(ApprovalRule{Action: "deploy:prod", Approvers: []string{"oncall"}}, now, owner)
	org.SetPolicy(ApprovalRule{Action: "merge:main", Scope: "repo:tapnow", Approvers: []string{"code_owner"}}, now, owner)
	org.AddMember(Membership{Worker: "lisi", Role: "code_owner", Duties: []string{"媒体服务"}}, now, owner)
	p3, _, _ := New("P3", "", "稳定性", P0, Period{}, now, owner)
	p3.AddMember(Membership{Worker: "zhaoliu", Role: "oncall", Period: Period{From: day(6), Until: day(13)}}, now, owner)
	p3.AddMember(Membership{Worker: "wangwu", Role: "oncall", Period: Period{From: day(13), Until: day(20)}}, now, owner)
	get := map[ProjectID]Project{OrgProject: org, "P3": p3}
	c := ChainOf("P3", func(id ProjectID) (Project, bool) { x, ok := get[id]; return x, ok })
	if len(c) != 2 || c[0].ID != "P3" {
		t.Fatal(c)
	}
	r, from, ok := c.Rule("deploy:prod", "")
	if !ok || from != OrgProject {
		t.Fatal("company rule inherited")
	}
	if got := c.Approvers(r, now, nil); len(got) != 1 || got[0] != "wangwu" {
		t.Fatalf("this week's on-call, got %v", got)
	}
	if got := c.Approvers(r, *day(8), nil); len(got) != 1 || got[0] != "zhaoliu" {
		t.Fatalf("last week's on-call, got %v", got)
	}
	r2, _, _ := c.Rule("merge:main", "repo:tapnow/web")
	if got := c.Approvers(r2, now, nil); len(got) != 1 || got[0] != "lisi" {
		t.Fatalf("code owner from the company, got %v", got)
	}
	if _, _, ok := c.Rule("merge:main", "repo:other"); ok {
		t.Fatal("scoped rule leaked")
	}
	if DutyMatch(Membership{Role: "数据负责人", Duties: []string{"埋点", "指标"}}, "定埋点方案") == 0 {
		t.Fatal("duty match")
	}
}
