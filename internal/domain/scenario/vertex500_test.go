// Package scenario checks the domain end to end on the worked example in
// docs/domain.md §8 (fixing view_media's Vertex 500s): who the rules pick,
// which approvals they route where, and what the agenda raises — everything
// the scaffold computes before a brain is asked anything.
package scenario

import (
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/attention"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/insight"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
	"github.com/chenhg5/bot-connect/internal/domain/world"
)

var (
	now   = time.Date(2026, 10, 14, 14, 0, 0, 0, time.UTC) // Tuesday 14:00
	owner = Actor{UserID: "u-owner", Worker: Owner, Role: RoleOwner}
)

func day(d int) *time.Time { t := time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC); return &t }

func must[T any](v T, _ []Event, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func person(id WorkerID, consent bool, auth ...workforce.Authority) workforce.Worker {
	return workforce.Worker{ID: id, Name: string(id), Kind: workforce.Human, Trust: workforce.External, Physical: true,
		Consent: workforce.Consent{Given: consent}, Authority: auth}
}

func agent(id WorkerID, caps ...string) workforce.Worker {
	w := workforce.Worker{ID: id, Name: string(id), Kind: workforce.AgentSession, Trust: workforce.Controlled}
	for _, c := range caps {
		w.Capability = append(w.Capability, workforce.ParseCapability(c))
	}
	return w
}

func build(t *testing.T) *world.World {
	t.Helper()
	w := world.New(now)
	org := must(portfolio.New(OrgProject, "", "公司", P2, Period{}, now, owner))
	org.SetPolicy(portfolio.ApprovalRule{Action: "deploy:prod", Approvers: []string{"oncall"}}, now, owner)
	org.SetPolicy(portfolio.ApprovalRule{Action: "merge:main", Scope: "repo:tapnow", Approvers: []string{"code_owner"}}, now, owner)
	org.AddMember(portfolio.Membership{Worker: "lisi", Role: "code_owner", Duties: []string{"媒体服务"}}, now, owner)
	p3 := must(portfolio.New("P3", "", "TapNow 稳定性", P0, Period{}, now, owner))
	p3.AddMember(portfolio.Membership{Worker: "zhaoliu", Role: "oncall", Period: Period{From: day(6), Until: day(13)}}, now, owner)
	p3.AddMember(portfolio.Membership{Worker: "wangwu", Role: "oncall", Period: Period{From: day(13), Until: day(20)}}, now, owner)
	p3.AddMember(portfolio.Membership{Worker: "jerry", Role: "业务方", Duties: []string{"报障", "验收效果"}}, now, owner)
	w.Projects[OrgProject], w.Projects["P3"] = org, p3

	for _, wk := range []workforce.Worker{
		person(Owner, true),
		person("lisi", true, workforce.Authority{Action: "merge:main", Scope: "repo:tapnow"}),
		person("wangwu", true), person("zhaoliu", true), person("jerry", true),
		person("bystander", false),
		agent("tapnow-dev", "repo:tapnow:write"),
		agent("ops-reader", "gcloud-logging:read", "metrics:read"),
	} {
		if err := wk.Validate(); err != nil {
			t.Fatal(err)
		}
		w.Workers[wk.ID] = wk
	}
	// lisi could also fix it himself — but an agent can, so he shouldn't be asked.
	lisi := w.Workers["lisi"]
	lisi.Capability = []workforce.Capability{workforce.ParseCapability("repo:tapnow:write")}
	w.Workers["lisi"] = lisi
	return w
}

func names(cs []insight.Candidate) []string {
	var out []string
	for _, c := range cs {
		n := string(c.Worker)
		if c.Excluded != "" {
			n += "(x)"
		}
		out = append(out, n)
	}
	return out
}

func TestStaffingAgentFirstAndApprovalsByRoleAndTime(t *testing.T) {
	w := build(t)
	st := insight.DefaultStaffing()
	logs := workforce.ParseCapability("gcloud-logging:read")
	if c := st.Candidates(insight.Need{Project: "P3", Text: "查日志定位 500", Capability: &logs, By: owner}, w); len(c) != 1 || c[0].Worker != "ops-reader" {
		t.Fatalf("logs → ops-reader only, got %v", names(c))
	}
	repo := workforce.ParseCapability("repo:tapnow:write")
	c := st.Candidates(insight.Need{Project: "P3", Text: "修复 view_media 重试与降级", Capability: &repo, By: owner}, w)
	if c[0].Worker != "tapnow-dev" || !strings.Contains(strings.Join(c[1].Reasons, ";"), "agent 也能做") {
		t.Fatalf("agent first, person demoted with a reason: %+v", c)
	}
	merge := st.Candidates(insight.Need{Project: "P3", Kind: NeedApproval, Action: "merge:main", Scope: "repo:tapnow", By: owner}, w)
	if merge[0].Worker != "lisi" {
		t.Fatalf("merge approval → code owner, got %v", names(merge))
	}
	deploy := st.Candidates(insight.Need{Project: "P3", Kind: NeedApproval, Action: "deploy:prod", By: owner}, w)
	if deploy[0].Worker != "wangwu" {
		t.Fatalf("deploy approval → this week's on-call, got %v", names(deploy))
	}
	w.Now = *day(8)
	if d := st.Candidates(insight.Need{Project: "P3", Kind: NeedApproval, Action: "deploy:prod", By: owner}, w); d[0].Worker != "zhaoliu" {
		t.Fatalf("a week earlier the on-call was zhaoliu, got %v", names(d))
	}
	w.Now = now
	conf := st.Candidates(insight.Need{Project: "P3", Kind: NeedJudgment, Text: "验收效果", By: owner}, w)
	if conf[0].Worker != "jerry" {
		t.Fatalf("reporter confirms, got %v", names(conf))
	}
	call := st.Candidates(insight.Need{Project: "P3", Kind: NeedPhysical, Text: "给客户打电话", By: owner}, w)
	for _, x := range call {
		if x.Agent {
			t.Fatal("agents can't make calls")
		}
		if x.Worker == "bystander" && x.Excluded == "" {
			t.Fatal("no consent → excluded")
		}
	}
	// Someone on leave is listed, but excluded with the reason.
	s := w.State("wangwu")
	s.Record(workforce.Fact{Dim: workforce.Presence, Value: "away", Detail: "病假", Source: workforce.Declared, At: now})
	w.States["wangwu"] = s
	d := st.Candidates(insight.Need{Project: "P3", Kind: NeedApproval, Action: "deploy:prod", By: owner}, w)
	if !containsExcluded(d, "wangwu") || !strings.Contains(d[0].Excluded, "病假") {
		t.Fatalf("unavailable approver must be excluded: %+v", d)
	}
}

func containsExcluded(cs []insight.Candidate, id WorkerID) bool {
	for _, c := range cs {
		if c.Worker == id && c.Excluded != "" {
			return true
		}
	}
	return false
}

// plan builds the five items of §8 and assigns I32 to tapnow-dev, who is slow.
func plan(t *testing.T, w *world.World) {
	t.Helper()
	due := time.Date(2026, 10, 14, 23, 0, 0, 0, time.UTC)
	mk := func(id ItemID, est time.Duration, needs []planning.Need, deps ...ItemID) planning.Item {
		it := must(planning.New(id, planning.Spec{Project: "P3", Title: string(id), Due: &due, Estimate: est, Needs: needs, DependsOn: deps}, now, owner))
		if err := w.Graph().Validate(id, "P3", "", deps); err != nil {
			t.Fatal(err)
		}
		w.Items[id] = it
		return it
	}
	mk("I31", time.Hour, []planning.Need{{Kind: NeedCapability, Detail: "gcloud-logging:read"}})
	mk("I32", 8*time.Hour, []planning.Need{{Kind: NeedCapability, Detail: "repo:tapnow:write"}}, "I31")
	mk("I33", 30*time.Minute, []planning.Need{{Kind: NeedApproval, Detail: "merge:main"}}, "I32")
	mk("I34", 30*time.Minute, []planning.Need{{Kind: NeedApproval, Detail: "deploy:prod"}}, "I33")
	mk("I35", time.Hour, []planning.Need{{Kind: NeedJudgment, Detail: "报障人确认"}}, "I34")

	// I31 is done; I32 is with tapnow-dev, started 2h ago.
	i31 := w.Items["I31"]
	i31.Complete(true, now, System("rule"))
	w.Items["I31"] = i31
	a := must(delegation.New(delegation.Offer{ID: "A32", Item: "I32", Project: "P3", Worker: "tapnow-dev", Brief: delegation.Brief{Goal: "修复", Estimate: 8 * time.Hour}}, now.Add(-2*time.Hour), owner))
	a.Accept(nil, "", now.Add(-2*time.Hour), System("driver:agent"))
	w.Assignments[a.ID] = a
	i32 := w.Items["I32"]
	i32.Attach("A32", "tapnow-dev", now, owner)
	i32.Progressed(now.Add(-2 * time.Hour))
	w.Items["I32"] = i32

	// History: tapnow-dev usually takes 1.5× its estimates.
	for i, id := range []AssignmentID{"H1", "H2"} {
		start := now.Add(-time.Duration(48+i*24) * time.Hour)
		h := must(delegation.New(delegation.Offer{ID: id, Item: "old", Project: "P3", Worker: "tapnow-dev", Brief: delegation.Brief{Goal: "old", Estimate: 2 * time.Hour}}, start, owner))
		d := System("driver:agent")
		h.Accept(nil, "", start, d)
		h.Deliver("ok", nil, start.Add(3*time.Hour), d)
		h.Verify("", start.Add(3*time.Hour), owner)
		w.Assignments[id] = h
	}
}

func TestAgendaRaisesTheDelayAndRespectsResolutions(t *testing.T) {
	w := build(t)
	plan(t, w)
	c := attention.NewContext(w)
	if f := c.Pace.Factor("tapnow-dev"); f < 1.4 || f > 1.6 {
		t.Fatalf("pace from history, got %v", f)
	}
	svc := attention.Default()
	ag := svc.Build(c, nil)
	if len(ag.Now) == 0 || ag.Now[0].Key != "will_miss:I32" {
		t.Fatalf("first thing now: I32 will miss today's deadline; agenda %+v", keys(ag.Now))
	}
	all := strings.Join(append(append(keys(ag.Now), keys(ag.Today)...), keys(ag.Watch)...), " ")
	for _, k := range []string{"late_dep:I33", "late_dep:I34", "late_dep:I35"} {
		if !strings.Contains(all, k) {
			t.Errorf("downstream %s should be raised: %s", k, all)
		}
	}
	// I31 is done and nothing about it shows up.
	if strings.Contains(all, ":I31") {
		t.Fatal("done items are quiet")
	}
	pr := insight.ProjectProgress(w, "P3", c.Forecast, attention.RiskCount(svc.Signals(c), "P3"))
	if pr.Health != insight.Red || pr.Done <= 0 || pr.Done >= 1 {
		t.Fatalf("progress %+v", pr)
	}

	// The brain acts on it and asks to be reminded in 2 hours.
	until := now.Add(2 * time.Hour)
	r, _, err := attention.Resolve(ag.Now[0].Signal, attention.Acted, &until, "已让 tapnow-dev 先做降级、重试后补", now, Actor{Role: RoleSystem, Via: "brain:L3"})
	if err != nil {
		t.Fatal(err)
	}
	res := map[string]attention.Resolution{r.Key: r}
	if ag2 := svc.Build(c, res); ag2.Suppressed == 0 || contains(keys(ag2.Now), "will_miss:I32") {
		t.Fatal("handled signal must not nag")
	}
	w.Now = now.Add(3 * time.Hour)
	if ag3 := svc.Build(attention.NewContext(w), res); !contains(append(keys(ag3.Now), keys(ag3.Today)...), "will_miss:I32") && !contains(keys(ag3.Now), "overdue:I32") {
		t.Fatalf("after the recheck time it comes back: %+v", keys(ag3.Now))
	}
}

func TestWaitingOnUsAndQuietWhenAllIsWell(t *testing.T) {
	w := build(t)
	// Nothing open: the agenda is empty (stand-up stays silent).
	if ag := attention.Default().Build(attention.NewContext(w), nil); len(ag.Now)+len(ag.Today) != 0 {
		t.Fatalf("all quiet expected, got %+v %+v", keys(ag.Now), keys(ag.Today))
	}
	due := now.Add(10 * time.Hour)
	it := must(planning.New("I40", planning.Spec{Project: "P3", Title: "设计稿", Due: &due}, now, owner))
	w.Items["I40"] = it
	a := must(delegation.New(delegation.Offer{ID: "A40", Item: "I40", Project: "P3", Worker: "jerry", ToPerson: true, Why: NeedJudgment, Brief: delegation.Brief{Goal: "确认"}}, now.Add(-5*time.Hour), owner))
	j := Actor{UserID: "u-jerry", Worker: "jerry"}
	a.Accept(nil, "", now.Add(-5*time.Hour), j)
	a.Deliver("ok", nil, now.Add(-4*time.Hour), j)
	w.Assignments["A40"] = a
	ag := attention.Default().Build(attention.NewContext(w), nil)
	if len(ag.Now) == 0 || ag.Now[0].Kind != "waiting_on_us" || ag.Now[0].Suggest[0] != "review" {
		t.Fatalf("a delivery due today waits for our review: %+v", ag.Now)
	}
}

func keys(s []attention.Scored) []string {
	var out []string
	for _, x := range s {
		out = append(out, x.Key)
	}
	return out
}

func contains(v []string, x string) bool {
	for _, s := range v {
		if s == x {
			return true
		}
	}
	return false
}
