package app_test

import (
	"context"
	"strings"

	"github.com/chenhg5/bot-connect/internal/domain/insight"
	"github.com/chenhg5/bot-connect/internal/domain/world"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/adapters/store/jsonstore"
	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

func TestPlanLifecycle(t *testing.T) {
	r := newRig(t, jsonstore.Memory())
	a := r.app
	_, err := a.CreateProject(ctx, principal, app.ProjectSpec{ID: "P9", Title: "周期发版", Priority: P1, Home: "chat-p9"})
	must(t, err)

	// structure errors reject the whole revision
	_, err = a.DraftPlan(ctx, principal, app.PlanDraft{Project: "P9", Nodes: []app.NodeSpec{
		{Ref: "c1", Title: "定发版节奏", Executor: "wangwu", Output: planning.OutDecision},
	}})
	if err == nil || !strings.Contains(err.Error(), "why") {
		t.Fatalf("a person without a reason: %v", err)
	}
	_, err = a.DraftPlan(ctx, principal, app.PlanDraft{Project: "P9", Nodes: []app.NodeSpec{
		{Ref: "x", Title: "替主人拍板", Executor: "ops-reader", Output: planning.OutDecision},
	}})
	if err == nil || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("an agent deciding: %v", err)
	}

	res, err := a.DraftPlan(ctx, principal, app.PlanDraft{Project: "P9", Spec: portfolio.PlanSpec{Kind: portfolio.KindTask},
		Nodes: []app.NodeSpec{
			{Ref: "r1", Title: "仓库现状调研", Executor: "ops-reader", Output: planning.OutFindings, Estimate: 2 * time.Hour},
			{Ref: "r2", Title: "同类项目发版策略调研", Executor: "tapnow-dev", Output: planning.OutFindings, Estimate: 2 * time.Hour},
			{Ref: "d1", Title: "发版方案草案", Executor: "tapnow-dev", Output: planning.OutDraft, DependsOn: []string{"r1", "r2"}, Estimate: 3 * time.Hour},
			{Ref: "c1", Title: "确定发版节奏", Executor: "wangwu", Why: NeedDecision, Output: planning.OutDecision, DependsOn: []string{"d1"}},
			{Ref: "b1", Title: "落地", DependsOn: []string{"c1"}, Routine: "release"},
		}})
	must(t, err)
	an := res.Analysis
	if len(an.Layers) != 4 || len(an.Layers[0]) != 2 {
		t.Fatalf("r1 and r2 run in parallel: %v", an.Layers)
	}
	if !strings.Contains(issues(an), "复利") {
		t.Fatalf("a routine without an investment is flagged: %s", issues(an))
	}
	if strings.Contains(issues(an), "执行者待定") {
		t.Fatalf("b1 may wait for c1 to decide who does it: %s", issues(an))
	}
	// research starts at once, before anyone agreed
	if n := len(r.agent.offers); n != 2 {
		t.Fatalf("the two research nodes go out while drafting: %d", n)
	}

	pr, err := a.ProposePlan(ctx, principal, "P9")
	must(t, err)
	if len(pr.Asked) != 1 {
		t.Fatalf("wangwu is asked to confirm his part: %+v", pr)
	}
	if w := a.PlanWaitingOn("P9"); len(w) != 2 {
		t.Fatalf("waiting on the principal and wangwu: %v", w)
	}
	if _, err := a.AgreePlan(ctx, Actor{Worker: "wangwu", Role: RoleMember}, "P9"); err == nil {
		t.Fatal("only the principal agrees to the plan")
	}
	_, err = a.AgreePlan(ctx, principal, "P9")
	must(t, err)
	if p, _, _ := a.PlanStatus("P9"); p.Plan.Stage != portfolio.PlanProposed {
		t.Fatalf("still waiting on wangwu: %s", p.Plan.Stage)
	}
	must(t, a.Report(ctx, Actor{Worker: "wangwu"}, pr.Asked[0], app.Report{Action: "answer", Choice: "approve"}))
	if p, _, _ := a.PlanStatus("P9"); p.Plan.Stage != portfolio.PlanActive {
		t.Fatalf("everyone confirmed: %s", p.Plan.Stage)
	}

	// d1 goes out once both research nodes are verified, with their results
	for _, as := range r.agent.offers[:2] {
		must(t, a.Report(ctx, System("driver:agent"), as.ID, app.Report{Action: "deliver", Result: "结论 " + string(as.Item)}))
		must(t, a.Review(ctx, principal, as.ID, "verify", ""))
	}
	a.Tick(ctx)
	if n := len(r.agent.offers); n != 3 || !strings.Contains(r.agent.offers[2].Brief.Context, "结论") {
		t.Fatalf("the draft node gets the findings: %+v", r.agent.offers)
	}
	if !strings.Contains(a.DescribePlan("P9"), "执行中") {
		t.Fatalf("describe: %s", a.DescribePlan("P9"))
	}

	// a revision needs agreeing again
	_, err = a.DraftPlan(ctx, principal, app.PlanDraft{Project: "P9", Nodes: []app.NodeSpec{{Ref: "c2", Title: "补一个发版 skill", Executor: "tapnow-dev", Output: planning.OutChange, Compounding: true, Routine: "release"}}})
	must(t, err)
	if p, _, _ := a.PlanStatus("P9"); p.Plan.Stage != portfolio.PlanDraft || p.Plan.Revision != 2 {
		t.Fatalf("revised: %+v", p.Plan)
	}
	if n := len(r.agent.offers); n != 3 {
		t.Fatalf("committing work waits for agreement: %d", n)
	}
}

func issues(an planning.Analysis) string {
	var b strings.Builder
	for _, i := range an.Issues {
		b.WriteString(i.String() + "\n")
	}
	return b.String()
}

func TestPlanPicksAgentsForResearch(t *testing.T) {
	r := newRig(t, jsonstore.Memory())
	a := r.app
	a.Staffing.Matchers = append(a.Staffing.Matchers, stubMatcher{"ops-reader"})
	_, err := a.CreateProject(ctx, principal, app.ProjectSpec{ID: "P8", Title: "日志调研", Priority: P2})
	must(t, err)
	res, err := a.DraftPlan(ctx, principal, app.PlanDraft{Project: "P8", Nodes: []app.NodeSpec{
		{Ref: "r1", Title: "查 gcloud-logging 里的错误日志", Output: planning.OutFindings},
	}})
	must(t, err)
	if len(res.Picked) != 1 || len(r.agent.offers) != 1 {
		t.Fatalf("research without an executor goes to an agent: %v %d", res.Picked, len(r.agent.offers))
	}
}

// stubMatcher stands in for the semantic matcher.
type stubMatcher struct{ w WorkerID }

func (stubMatcher) Name() string { return "stub" }
func (m stubMatcher) Match(insight.Need, *world.World) []insight.Candidate {
	return []insight.Candidate{{Worker: m.w, Score: 2}}
}

func TestExplorePlanRounds(t *testing.T) {
	r := newRig(t, jsonstore.Memory())
	a := r.app
	until := t0().Add(14 * 24 * time.Hour)
	_, err := a.CreateProject(ctx, principal, app.ProjectSpec{ID: "PX", Title: "记账工具", Priority: P2, Timebox: Period{Until: &until}})
	must(t, err)
	due := t0().Add(24 * time.Hour)
	_, err = a.DraftPlan(ctx, principal, app.PlanDraft{Project: "PX",
		Spec: portfolio.PlanSpec{Kind: portfolio.KindExplore, Questions: []string{"给谁用", "核心场景是什么"}},
		Nodes: []app.NodeSpec{
			{Ref: "a", Title: "方向 A 的原型草图", Executor: "tapnow-dev", Output: planning.OutDraft, Due: &due},
			{Ref: "b", Title: "方向 B 的原型草图", Executor: "ops-reader", Output: planning.OutDraft, Due: &due},
		}})
	must(t, err)
	p, _, _ := a.PlanStatus("PX")
	if p.Plan.Stage != portfolio.PlanActive || p.Plan.Round != 1 || len(r.agent.offers) != 2 {
		t.Fatalf("exploring runs at once, both probes out: %+v offers=%d", p.Plan, len(r.agent.offers))
	}
	// probes running past their dates are not risks while exploring
	r.clock.Advance(4 * 24 * time.Hour)
	a.Tick(ctx)
	text := ""
	for _, w := range r.wakes {
		text += wakeText(w)
	}
	if strings.Contains(text, "will_miss") || strings.Contains(text, "已过截止") {
		t.Fatalf("no deadline risks while exploring: %s", text)
	}
	if !strings.Contains(text, "没和委托人对过") {
		t.Fatalf("losing touch with the principal is the risk: %s", text)
	}
	// the principal saw both: one direction is wrong
	_, _, err = a.LearnPlan(ctx, principal, "PX", "B 方向不对：委托人要的是团队报销，不是个人记账", "pivot", []string{"给谁用"})
	must(t, err)
	p, _, _ = a.PlanStatus("PX")
	if p.Plan.Round != 2 || len(p.Plan.Questions) != 1 || len(p.Plan.Learned) != 1 {
		t.Fatalf("a round is recorded: %+v", p.Plan)
	}
	if !strings.Contains(a.DescribePlan("PX"), "团队报销") {
		t.Fatalf("what was learned stays in view: %s", a.DescribePlan("PX"))
	}
	_, _, err = a.LearnPlan(ctx, principal, "PX", "核心场景是月底报销汇总", "converge", []string{"核心场景是什么"})
	must(t, err)
	p, _, _ = a.PlanStatus("PX")
	if p.Plan.Kind != portfolio.KindTask || p.Plan.Stage != portfolio.PlanDraft {
		t.Fatalf("converged into a task plan to agree on: %+v", p.Plan)
	}
}

type fixedSorter []app.Sorted

func (f fixedSorter) Sort(context.Context, []string, string) ([]app.Sorted, error) { return f, nil }

func TestSortedBuckets(t *testing.T) {
	out := app.DescribeSorted([]app.Sorted{
		{Text: "官网首页改版", Bucket: app.BucketProgram, Confidence: 0.9, Mine: 0.8},
		{Text: "线上支付报错", Bucket: app.BucketHandoff, Confidence: 0.9, Urgent: 0.9, Mine: 0.9},
		{Text: "要不要换云厂商", Bucket: app.BucketDecision, Confidence: 0.8, Mine: 0.1},
		{Text: "张三下周请假", Bucket: app.BucketInfo, Confidence: 0.4, Mine: 0.9},
	})
	for _, want := range []string{"今天必须处理：「线上支付报错」", "【要拍板】", "要委托人参与", "【整件交出去】", "分类拿不准"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "【要拍板】") > strings.Index(out, "【需要规划】") {
		t.Fatalf("decisions come first (they block others):\n%s", out)
	}
	_ = fixedSorter(nil)
}
