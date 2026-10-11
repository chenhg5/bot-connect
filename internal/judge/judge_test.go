package judge

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/adapters/store/jsonstore"
	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/inbox"
	"github.com/chenhg5/bot-connect/internal/domain/insight"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
)

// scripted answers questions by id.
type scripted map[string]Answer

func (s scripted) Ask(_ context.Context, _ any, qs map[string]Question) (map[string]Answer, error) {
	out := map[string]Answer{}
	for k := range qs {
		if a, ok := s[k]; ok {
			out[k] = a
		} else if a, ok := s["*"]; ok {
			out[k] = a
		}
	}
	return out, nil
}

func newApp(t *testing.T) *app.App {
	a := app.New(jsonstore.Memory(), NewFakeClock(time.Date(2026, 10, 12, 10, 0, 0, 0, time.Local)))
	if err := a.EnsureOrg(context.Background(), "公司"); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestTriageMapsAnswers(t *testing.T) {
	tr := &Triage{J: scripted{
		"intent": {Choice: "reply"},
		"action": {Choice: "accept", Probabilities: map[string]float64{"accept": 0.95}},
		"urgent": {Noul: 0.8},
	}}
	r, err := tr.Assess(context.Background(), inbox.Signal{Body: "接了，周三给你"})
	if err != nil || r.Intent != "reply" || r.Action != "accept" || r.Confidence != 0.95 || *r.Level != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	tr = &Triage{J: scripted{"intent": {Choice: "chat"}, "action": {Choice: "accept", Probabilities: map[string]float64{"accept": 0.9}}, "urgent": {Noul: 0.1}}}
	if r, _ := tr.Assess(context.Background(), inbox.Signal{}); r.Action != "" {
		t.Fatal("an action only counts for a reply")
	}
}

func TestGuard(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	principal := Actor{UserID: "u", Role: RolePrincipal}
	a.UpsertWorker(ctx, principal, workforce.Worker{ID: "ww", Name: "王五", Kind: workforce.Human, Trust: workforce.External, Identities: []string{"feishu:ou_ww"}})
	a.CreateProject(ctx, principal, app.ProjectSpec{ID: "P9", Title: "收购星河科技"})
	g := &Guard{App: a} // no judge: exact titles only
	if v := g.Check(ctx, "ou_ww", "", "chicken 在忙收购星河科技的事"); !v.Block {
		t.Fatal("a private project's title must be caught by code")
	}
	if v := g.Check(ctx, "ou_ww", "", "埋点找赵六"); v.Block {
		t.Fatal("harmless text passes")
	}
	g.J = scripted{"leak": {Noul: 0.9}}
	if v := g.Check(ctx, "ou_ww", "", "他在谈一笔并购"); !v.Block {
		t.Fatal("a paraphrased leak is caught by the judge")
	}
}

func TestMatcher(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	principal := Actor{UserID: "u", Role: RolePrincipal}
	a.UpsertWorker(ctx, principal, workforce.Worker{ID: "zl", Name: "赵六", Kind: workforce.Human, Trust: workforce.External, Description: "数据分析"})
	a.UpsertWorker(ctx, principal, workforce.Worker{ID: "ls", Name: "李四", Kind: workforce.Human, Trust: workforce.External, Description: "设计"})
	m := Matcher{J: scripted{"w_zl": {Score: 2.8}, "w_ls": {Score: 0.4}}}
	cs := m.Match(insight.Need{Text: "定埋点方案"}, a.World(ctx))
	if len(cs) != 1 || cs[0].Worker != "zl" || !strings.Contains(cs[0].Reasons[0], "语义") {
		t.Fatalf("%+v", cs)
	}
}

func TestTriageLandsAnAcceptance(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	principal := Actor{UserID: "u", Worker: Principal, Role: RolePrincipal}
	a.UpsertWorker(ctx, principal, workforce.Worker{ID: "ww", Name: "王五", Kind: workforce.Human, Trust: workforce.External, Consent: workforce.Consent{Given: true}})
	a.RegisterDriver(string(workforce.Human), nopDriver{})
	it, _ := a.PlanItem(ctx, principal, planningSpec("P", "埋点方案"))
	as, err := a.Delegate(ctx, principal, app.DelegateSpec{Item: it.ID, Worker: "ww", Why: NeedKnowledge})
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	a.Say = func(_ context.Context, conv, text string) error { said = append(said, conv+": "+text); return nil }
	woke := 0
	a.Waker = app.WakerFunc(func(context.Context, app.Wake) { woke++ })
	a.Triage = &Triage{J: scripted{"intent": {Choice: "reply"}, "action": {Choice: "accept", Probabilities: map[string]float64{"accept": 0.97}}, "urgent": {Noul: 0.1}}, App: a}
	sig, _, err := a.Ingest(ctx, app.SignalSpec{Dedupe: "m1", Source: inbox.DirectMessage, Reason: "dm", Actor: Actor{UserID: "ou_ww", Worker: "ww", Role: RoleVisitor},
		Body: "接了，周三给你", ReplyTo: inbox.ReplyTo{Conv: "dm-ww"}})
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := a.Assignment(as.ID)
	if cur.Status != "accepted" || sig.Status != inbox.Handled || woke != 0 || len(said) != 1 || !strings.HasPrefix(said[0], "dm-ww: 收到") {
		t.Fatalf("landed without the brain: status=%s signal=%s woke=%d said=%v", cur.Status, sig.Status, woke, said)
	}
}

type nopDriver struct{}

func (nopDriver) Observe(context.Context, workforce.Worker, []delegation.Assignment) []workforce.Fact {
	return nil
}
func (nopDriver) Offer(context.Context, workforce.Worker, delegation.Assignment) error { return nil }
func (nopDriver) Notify(context.Context, workforce.Worker, delegation.Assignment, app.Notice) error {
	return nil
}

func planningSpec(p, title string) planning.Spec {
	return planning.Spec{Project: OrgProject, Title: title}
}
