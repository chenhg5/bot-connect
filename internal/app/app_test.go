package app_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/adapters/drivers/human"
	"github.com/chenhg5/bot-connect/internal/adapters/store/jsonstore"
	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/attention"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
)

// fakeAgent accepts every offer at once and records it; tests finish the work.
type fakeAgent struct {
	app    *app.App
	mu     sync.Mutex
	offers []delegation.Assignment
}

func (f *fakeAgent) Observe(context.Context, workforce.Worker, []delegation.Assignment) []workforce.Fact {
	return nil
}
func (f *fakeAgent) Offer(ctx context.Context, w workforce.Worker, a delegation.Assignment) error {
	f.mu.Lock()
	f.offers = append(f.offers, a)
	f.mu.Unlock()
	return f.app.Report(ctx, System("driver:agent"), a.ID, app.Report{Action: "accept", Ref: "t-" + string(a.ID)})
}
func (f *fakeAgent) Notify(context.Context, workforce.Worker, delegation.Assignment, app.Notice) error {
	return nil
}

type rig struct {
	app   *app.App
	clock *FakeClock
	agent *fakeAgent
	mu    sync.Mutex
	dms   []string // "address: text"
	wakes []app.Trigger
}

var (
	ctx   = context.Background()
	owner = Actor{UserID: "u-owner", Worker: Owner, Role: RoleOwner}
)

func t0() time.Time { return time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC) } // Tuesday

func person(id WorkerID, consent bool) workforce.Worker {
	return workforce.Worker{ID: id, Name: string(id), Kind: workforce.Human, Trust: workforce.External, Physical: true,
		Consent: workforce.Consent{Given: consent}, Identities: []string{"feishu:ou_" + string(id)},
		Norms:   workforce.Norms{Timezone: "UTC", WorkHours: []workforce.Window{{Start: "09:00", End: "19:00"}}},
		Contact: []workforce.Route{{Kind: human.FeishuDM, Address: "ou_" + string(id)}}}
}

func agentW(id WorkerID, caps ...string) workforce.Worker {
	w := workforce.Worker{ID: id, Name: string(id), Kind: workforce.AgentSession, Trust: workforce.Controlled}
	for _, c := range caps {
		w.Capability = append(w.Capability, workforce.ParseCapability(c))
	}
	return w
}

func newRig(t *testing.T, store app.Store) *rig {
	t.Helper()
	r := &rig{clock: NewFakeClock(t0())}
	a := app.New(store, r.clock)
	r.app = a
	r.agent = &fakeAgent{app: a}
	a.RegisterDriver(string(workforce.AgentSession), r.agent)
	a.RegisterDriver(string(workforce.Human), &human.Driver{Clock: r.clock, Reachers: map[workforce.RouteKind]human.Reacher{
		human.FeishuDM: human.ReacherFunc(func(_ context.Context, addr, text string, _ workforce.Urgency) (string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.dms = append(r.dms, addr+": "+text)
			return "m", nil
		}),
		human.OwnerChat: human.ReacherFunc(func(_ context.Context, addr, text string, _ workforce.Urgency) (string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.dms = append(r.dms, "owner: "+text)
			return "m", nil
		}),
	}})
	a.Waker = app.WakerFunc(func(_ context.Context, tr app.Trigger) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.wakes = append(r.wakes, tr)
	})

	must(t, a.EnsureOrg(ctx, "公司"))
	ownerW := person(Owner, true)
	ownerW.Contact = []workforce.Route{{Kind: human.OwnerChat, Address: "owner"}}
	ownerW.Norms = workforce.Norms{}
	for _, w := range []workforce.Worker{ownerW, person("lisi", true), person("wangwu", true), person("zhaoliu", true), person("jerry", true),
		agentW("tapnow-dev", "repo:tapnow:write"), agentW("ops-reader", "gcloud-logging:read", "metrics:read")} {
		must(t, a.UpsertWorker(ctx, owner, w))
	}
	day := func(d int) *time.Time { x := time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC); return &x }
	_, err := a.ChangeProject(ctx, owner, OrgProject, func(p *portfolio.Project, now time.Time) ([]Event, error) {
		var evs []Event
		for _, r := range []portfolio.ApprovalRule{{Action: "deploy:prod", Approvers: []string{"oncall"}},
			{Action: "merge:main", Scope: "repo:tapnow", Approvers: []string{"code_owner"}}} {
			e, err := p.SetPolicy(r, now, owner)
			if err != nil {
				return nil, err
			}
			evs = append(evs, e...)
		}
		return p.AddMember(portfolio.Membership{Worker: "lisi", Role: "code_owner", Duties: []string{"媒体服务"}}, now, owner)
	})
	must(t, err)
	_, err = a.CreateProject(ctx, owner, app.ProjectSpec{ID: "P3", Title: "TapNow 稳定性", Priority: P0, Home: "chat-p3"})
	must(t, err)
	_, err = a.ChangeProject(ctx, owner, "P3", func(p *portfolio.Project, now time.Time) ([]Event, error) {
		p.AddMember(portfolio.Membership{Worker: "zhaoliu", Role: "oncall", Period: Period{From: day(6), Until: day(13)}}, now, owner)
		p.AddMember(portfolio.Membership{Worker: "wangwu", Role: "oncall", Period: Period{From: day(13), Until: day(20)}}, now, owner)
		return p.AddMember(portfolio.Membership{Worker: "jerry", Role: "业务方", Duties: []string{"报障", "验收效果"}}, now, owner)
	})
	must(t, err)
	return r
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (r *rig) lastDM() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.dms) == 0 {
		return ""
	}
	return r.dms[len(r.dms)-1]
}

func (r *rig) lastWake() app.Trigger {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.wakes) == 0 {
		return app.Trigger{}
	}
	return r.wakes[len(r.wakes)-1]
}

func TestVertex500FlowThroughCommands(t *testing.T) {
	r := newRig(t, jsonstore.Memory())
	a := r.app
	due := t0().Add(13 * time.Hour)
	plan := func(title string, est time.Duration, needs []planning.Need, deps ...ItemID) planning.Item {
		it, err := a.PlanItem(ctx, owner, planning.Spec{Project: "P3", Title: title, Due: &due, Estimate: est, Needs: needs, DependsOn: deps})
		must(t, err)
		return it
	}
	i31 := plan("查日志定位 500", time.Hour, []planning.Need{{Kind: NeedCapability, Detail: "gcloud-logging:read"}})
	i32 := plan("修复并提 PR", 3*time.Hour, []planning.Need{{Kind: NeedCapability, Detail: "repo:tapnow:write"}}, i31.ID)

	// Agents first: giving log reading to a person "for capability" is refused.
	if _, err := a.Delegate(ctx, owner, app.DelegateSpec{Item: i31.ID, Worker: "lisi", Why: NeedCapability}); err == nil || !strings.Contains(err.Error(), "ops-reader") {
		t.Fatalf("agent-first not enforced: %v", err)
	}
	if _, err := a.Delegate(ctx, owner, app.DelegateSpec{Item: i31.ID, Worker: "lisi"}); err == nil {
		t.Fatal("a person without a reason")
	}
	as31, err := a.Delegate(ctx, owner, app.DelegateSpec{Item: i31.ID, Worker: "ops-reader"})
	must(t, err)
	if as31.Status != delegation.Accepted || as31.Ref == "" {
		t.Fatalf("agent accepted at once: %+v", as31)
	}
	if _, err := a.Delegate(ctx, owner, app.DelegateSpec{Item: i31.ID, Worker: "tapnow-dev"}); err == nil {
		t.Fatal("two open assignments on one item")
	}

	// ops-reader delivers a finding with two options: a decision for the owner.
	must(t, a.Report(ctx, System("driver:agent"), as31.ID, app.Report{Action: "deliver", Result: "Vertex 区域配额收紧", Evidence: []string{"logs://q"}}))
	if w := r.lastWake(); w.Conv != "chat-p3" || !strings.Contains(w.Text, "等待验收") {
		t.Fatalf("delivery wakes the brain in the project's chat: %+v", w)
	}
	must(t, a.Review(ctx, owner, as31.ID, "verify", ""))
	dec, err := a.Delegate(ctx, owner, app.DelegateSpec{Item: i32.ID, Worker: Owner, Kind: delegation.Decision, Why: NeedDecision,
		Options: []string{"A 换区域（+2000/月）", "B 重试+降级"}, Brief: delegation.Brief{Goal: "选修复方案"}})
	must(t, err)
	if !strings.HasPrefix(r.lastDM(), "owner: 🤔") || !strings.Contains(r.lastDM(), "B 重试+降级") {
		t.Fatalf("decision sent to the owner with options: %s", r.lastDM())
	}
	must(t, a.Report(ctx, owner, dec.ID, app.Report{Action: "answer", Choice: "B 重试+降级"}))
	var child planning.Item
	r.app.Store.Read(func(s *app.State) { child = s.Items[dec.Item] })
	if child.Parent != i32.ID || child.Status != planning.Done {
		t.Fatalf("the decision lives on a child item and closes it: %+v", child)
	}

	// tapnow-dev fixes it; merging needs the code owner's approval.
	as32, err := a.Delegate(ctx, owner, app.DelegateSpec{Item: i32.ID, Worker: "tapnow-dev", Brief: delegation.Brief{Goal: "按方案 B 修复并提 PR"}})
	must(t, err)
	g, err := a.Authorize(ctx, System("driver:agent"), i32.ID, "merge:main", "repo:tapnow", "PR #12，CI 通过")
	must(t, err)
	if g.Allowed || g.Assignment == nil || g.Assignment.Worker != "lisi" || !strings.Contains(r.lastDM(), "ou_lisi: 🔏") {
		t.Fatalf("merge needs lisi's approval: %+v %s", g, r.lastDM())
	}
	g2, _ := a.Authorize(ctx, System("driver:agent"), i32.ID, "merge:main", "repo:tapnow", "")
	if g2.Allowed || g2.Assignment.ID != g.Assignment.ID {
		t.Fatal("a pending approval is not requested twice")
	}
	lisi := Actor{UserID: "ou_lisi", Worker: "lisi", Role: RoleVisitor}
	if err := a.Report(ctx, Actor{Worker: "jerry"}, g.Assignment.ID, app.Report{Action: "answer", Choice: "approve"}); err == nil {
		t.Fatal("someone else approved for lisi")
	}
	must(t, a.Report(ctx, lisi, g.Assignment.ID, app.Report{Action: "answer", Choice: "approve", Note: "LGTM"}))
	if g3, _ := a.Authorize(ctx, System("driver:agent"), i32.ID, "merge:main", "repo:tapnow", ""); !g3.Allowed {
		t.Fatal("approved merge is allowed")
	}
	gd, err := a.Authorize(ctx, System("driver:agent"), i32.ID, "deploy:prod", "", "")
	must(t, err)
	if gd.Assignment == nil || gd.Assignment.Worker != "wangwu" {
		t.Fatalf("deploy approval goes to this week's on-call: %+v", gd)
	}
	must(t, a.Report(ctx, System("driver:agent"), as32.ID, app.Report{Action: "deliver", Result: "已合并并发布"}))
	must(t, a.Review(ctx, owner, as32.ID, "verify", ""))
	var it32 planning.Item
	r.app.Store.Read(func(s *app.State) { it32 = s.Items[i32.ID] })
	if it32.Status != planning.Done {
		t.Fatalf("verified delivery completes the item: %s", it32.Status)
	}
	if id, ok := a.WorkerFor("ou_lisi"); !ok || id != "lisi" {
		t.Fatal("platform identity → worker")
	}
}

func TestPeopleFollowUpsAndExpiry(t *testing.T) {
	r := newRig(t, jsonstore.Memory())
	a := r.app
	it, err := a.PlanItem(ctx, owner, planning.Spec{Project: "P3", Title: "确认问题消失"})
	must(t, err)
	as, err := a.Delegate(ctx, owner, app.DelegateSpec{Item: it.ID, Worker: "jerry", Kind: delegation.Work, Why: NeedJudgment})
	must(t, err)
	n := len(r.dms)
	r.clock.Advance(5 * time.Hour) // 15:00
	a.Tick(ctx)
	if len(r.dms) != n+1 || !strings.Contains(r.lastDM(), "⏰") {
		t.Fatalf("a reminder after 4h: %v", r.dms[n:])
	}
	r.clock.Advance(time.Hour)
	a.Tick(ctx)
	if len(r.dms) != n+1 {
		t.Fatal("reminders are spaced")
	}
	r.clock.Advance(12 * time.Hour) // 04:00: outside hours
	a.Tick(ctx)
	if len(r.dms) != n+1 {
		t.Fatal("no reminders at night")
	}
	r.clock.Advance(32 * time.Hour) // > 48h since the offer
	a.Tick(ctx)
	cur, _ := a.Assignment(as.ID)
	if cur.Status != delegation.Expired || !strings.Contains(r.lastWake().Text, "已过期") {
		t.Fatalf("expired and reported: %s %+v", cur.Status, r.lastWake())
	}
}

func TestSignalsRaiseOnceThenEscalate(t *testing.T) {
	r := newRig(t, jsonstore.Memory())
	a := r.app
	due := t0().Add(2 * time.Hour)
	_, err := a.PlanItem(ctx, owner, planning.Spec{Project: "P3", Title: "发版", Due: &due, Estimate: 4 * time.Hour})
	must(t, err)
	a.Tick(ctx)
	if w := r.lastWake(); w.Kind != "rule" || w.Conv != "chat-p3" || len(w.Signals) == 0 {
		t.Fatalf("new risk wakes the brain in the project chat: %+v", w)
	}
	n := len(r.wakes)
	r.clock.Advance(10 * time.Minute)
	a.Tick(ctx)
	if len(r.wakes) != n {
		t.Fatal("the same signal is raised once")
	}
	r.clock.Advance(2 * time.Hour) // still unhandled; now overdue too
	a.Tick(ctx)
	escalated := false
	for _, w := range r.wakes[n:] {
		escalated = escalated || (w.Conv == "" && strings.Contains(w.Text, "没有处理"))
	}
	if !escalated {
		t.Fatalf("unhandled high signal escalates to the owner: %+v", r.wakes[n:])
	}
	// Handling it keeps it quiet.
	ag, _ := a.Agenda(ctx)
	for _, s := range append(ag.Now, ag.Today...) {
		until := r.clock.Now().Add(3 * time.Hour)
		must(t, a.Resolve(ctx, owner, s.Key, attention.Acted, &until, "已和需求方确认延期"))
	}
	if ag2, _ := a.Agenda(ctx); len(ag2.Now)+len(ag2.Today) != 0 || ag2.Suppressed == 0 {
		t.Fatalf("handled signals stay off the agenda: %+v", ag2)
	}
}

func TestPersistenceAndAtomicity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := jsonstore.Open(path)
	must(t, err)
	r := newRig(t, st)
	if _, err := r.app.PlanItem(ctx, owner, planning.Spec{Project: "nope", Title: "x"}); err == nil {
		t.Fatal("unknown project")
	}
	it, err := r.app.PlanItem(ctx, owner, planning.Spec{Project: "P3", Title: "x"})
	must(t, err)
	if _, err := r.app.PlanItem(ctx, Actor{Role: RoleVisitor}, planning.Spec{Project: "P3", Title: "y"}); err == nil {
		t.Fatal("visitors can't plan")
	}
	st2, err := jsonstore.Open(path)
	must(t, err)
	var got planning.Item
	var workers int
	st2.Read(func(s *app.State) { got, workers = s.Items[it.ID], len(s.Workers) })
	if got.Title != "x" || workers != 7 {
		t.Fatalf("reloaded: %+v workers=%d", got, workers)
	}
}
