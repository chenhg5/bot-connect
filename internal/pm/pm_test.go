package pm

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/plan"
	"github.com/chenhg5/bot-connect/internal/reach"
	"github.com/chenhg5/bot-connect/internal/worker"
	"github.com/chenhg5/bot-connect/internal/workforce"
)

type rig struct {
	s     *Service
	mu    sync.Mutex
	posts []string // brain wake-ups
	dms   []string // messages to people ("ou_x: text")
	owner []string // direct messages to the owner
	now   time.Time
}

var (
	ownerU = identity.User{ID: "ou_me", Role: identity.RoleOwner, Name: "chicken"}
	zhangU = identity.User{ID: "ou_zs", Role: identity.RoleVisitor, Name: "张三"}
)

func newRig(t *testing.T, people ...config.Person) *rig {
	t.Helper()
	s, err := Open(t.TempDir(), nil, people)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{s: s, now: time.Date(2026, 10, 12, 10, 0, 0, 0, time.Local)} // Monday 10:00
	s.now = func() time.Time { return r.now }
	s.AddBot(&BotIO{Name: "b", OwnerName: "chicken",
		Post: func(conv, text string, _ identity.User) bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.posts = append(r.posts, conv+": "+text)
			return true
		},
		NotifyOwner: func(_ context.Context, text string) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.owner = append(r.owner, text)
			return nil
		},
		SendUser: func(_ context.Context, _, user, text string) (string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.dms = append(r.dms, user+": "+text)
			return "m1", nil
		},
		OwnerConv: func() string { return "owner-chat" },
	})
	return r
}

func zhang(consent bool) config.Person {
	return config.Person{ID: "zhangsan", Name: "张三", Identities: []string{"feishu:ou_zs"}, Consent: consent,
		WorkHours: []reach.Window{{Start: "09:00", End: "19:00"}},
		Contact:   []reach.Route{{Channel: reach.FeishuDM, Address: "ou_zs"}}}
}

func last(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

func TestHumanWorkerLifecycle(t *testing.T) {
	r := newRig(t, zhang(true))
	ctx := context.Background()
	due := r.now.Add(30 * time.Hour)
	task, err := r.s.CreateTask("b", "owner-chat", ownerU, TaskSpec{Title: "确认首页设计稿", Done: "设计稿链接 + 确认无误", Due: &due, Estimate: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.s.Assign(ctx, "b", task.ID, "zhangsan", ownerU, workerScope(), workforce.Brief{Evidence: "设计稿链接"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != workforce.Offered || !strings.Contains(last(r.dms), "ou_zs") || !strings.Contains(last(r.dms), "确认首页设计稿") {
		t.Fatalf("offer not delivered: %s %v", a.Status, r.dms)
	}
	// Only the assignee may respond.
	if err := r.s.Respond("b", a.ID, workforce.Event{Type: workforce.EvAccept}, identity.User{ID: "ou_other"}); err == nil {
		t.Fatal("a stranger answered for 张三")
	}
	if err := r.s.Respond("b", a.ID, workforce.Event{Type: workforce.EvAccept, Note: "下午给"}, zhangU); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(last(r.posts), "已接下") {
		t.Fatalf("requester not told: %v", r.posts)
	}
	if ctxText := r.s.Context("b", zhangU); !strings.Contains(ctxText, a.ID) || strings.Contains(ctxText, "projects:") {
		t.Fatalf("assignee context must show only their own work:\n%s", ctxText)
	}
	_ = r.s.Respond("b", a.ID, workforce.Event{Type: workforce.EvAsk, Note: "用哪版 logo？"}, zhangU)
	if tk, _, _ := r.s.Task("b", task.ID); tk.Status != plan.TaskBlocked {
		t.Fatalf("task should be blocked on the question: %s", tk.Status)
	}
	if err := r.s.UpdateAssignment("b", a.ID, workforce.Event{Type: workforce.EvAnswer, Note: "用新版"}, ownerU); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(last(r.dms), "用新版") {
		t.Fatal("answer not relayed to the person")
	}
	_ = r.s.Respond("b", a.ID, workforce.Event{Type: workforce.EvDeliver, Result: "已确认", Evidence: []string{"https://figma/x"}}, zhangU)
	if !strings.Contains(last(r.posts), "等待验收") || !strings.Contains(last(r.posts), "figma") {
		t.Fatalf("delivery report: %s", last(r.posts))
	}
	_ = r.s.UpdateAssignment("b", a.ID, workforce.Event{Type: workforce.EvVerify}, ownerU)
	if tk, _, _ := r.s.Task("b", task.ID); tk.Status != plan.TaskDone {
		t.Fatalf("verified → task done, got %s", tk.Status)
	}
}

func TestConsentAndCounter(t *testing.T) {
	r := newRig(t, zhang(false))
	ctx := context.Background()
	task, _ := r.s.CreateTask("b", "owner-chat", ownerU, TaskSpec{Title: "x"})
	if _, err := r.s.Assign(ctx, "b", task.ID, "zhangsan", ownerU, workerScope(), workforce.Brief{}); err == nil || !strings.Contains(err.Error(), "consent") {
		t.Fatalf("assigning without consent: %v", err)
	}
	r2 := newRig(t, zhang(true))
	due := r2.now.Add(24 * time.Hour)
	task, _ = r2.s.CreateTask("b", "owner-chat", ownerU, TaskSpec{Title: "x", Due: &due})
	a, _ := r2.s.Assign(ctx, "b", task.ID, "zhangsan", ownerU, workerScope(), workforce.Brief{})
	later := r2.now.Add(72 * time.Hour)
	_ = r2.s.Respond("b", a.ID, workforce.Event{Type: workforce.EvCounter, Due: &later, Note: "这周排满了"}, zhangU)
	if !strings.Contains(last(r2.posts), "还价") {
		t.Fatal("counter not reported")
	}
	if err := r2.s.UpdateAssignment("b", a.ID, workforce.Event{Type: workforce.EvAccept}, ownerU); err != nil {
		t.Fatal(err)
	}
	tk, _, _ := r2.s.Task("b", task.ID)
	cur, _ := r2.s.Assignment("b", a.ID)
	if !tk.Due.Equal(later) || cur.Status != workforce.Working {
		t.Fatalf("accepted counter: due %v status %s", tk.Due, cur.Status)
	}
}

func TestFollowUpsNudgeThenEscalateThenExpire(t *testing.T) {
	p := zhang(true)
	p.MaxNudgesPerDay, p.MinNudgeInterval = 2, config.Duration{Duration: time.Hour}
	r := newRig(t, p)
	ctx := context.Background()
	task, _ := r.s.CreateTask("b", "owner-chat", ownerU, TaskSpec{Title: "回复报价"})
	a, _ := r.s.Assign(ctx, "b", task.ID, "zhangsan", ownerU, workerScope(), workforce.Brief{})
	sent := len(r.dms)
	r.now = r.now.Add(5 * time.Hour) // 15:00, no answer
	r.s.Tick(ctx)
	if len(r.dms) != sent+1 || !strings.Contains(last(r.dms), "提醒") {
		t.Fatalf("expected a reminder: %v", r.dms)
	}
	r.now = r.now.Add(30 * time.Minute)
	r.s.Tick(ctx)
	if len(r.dms) != sent+1 {
		t.Fatal("min_nudge_interval not honoured")
	}
	r.now = r.now.Add(time.Hour)
	r.s.Tick(ctx)
	r.now = r.now.Add(10 * time.Hour) // 02:00: outside work hours
	n := len(r.dms)
	r.s.Tick(ctx)
	if len(r.dms) != n {
		t.Fatal("no reminders at night")
	}
	r.now = r.now.Add(8 * time.Hour) // 10:00 next day, two nudges in the last 24h → stop nagging, tell the planner
	r.s.Tick(ctx)
	found := false
	for _, x := range r.posts {
		found = found || strings.Contains(x, "不再继续催")
	}
	if !found {
		t.Fatalf("unresponsive person not flagged: %v", r.posts)
	}
	r.now = r.now.Add(30 * time.Hour) // > 48h since the offer
	r.s.Tick(ctx)
	if cur, _ := r.s.Assignment("b", a.ID); cur.Status != workforce.Expired {
		t.Fatalf("offer should expire, got %s", cur.Status)
	}
	if tk, _, _ := r.s.Task("b", task.ID); tk.Status != plan.TaskTodo || tk.Assignee != "" {
		t.Fatalf("expired → back to todo: %+v", tk)
	}
}

func TestRiskWakesBrainOnceAndEscalates(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	due := r.now.Add(6 * time.Hour)
	task, _ := r.s.CreateTask("b", "owner-chat", ownerU, TaskSpec{Title: "发版", Due: &due, Estimate: 8 * time.Hour})
	r.s.Tick(ctx)
	if len(r.posts) != 1 || !strings.Contains(r.posts[0], "发现风险") || !strings.Contains(r.posts[0], task.ID) {
		t.Fatalf("risk wake-up: %v", r.posts)
	}
	r.now = r.now.Add(10 * time.Minute)
	r.s.Tick(ctx)
	if len(r.posts) != 1 {
		t.Fatal("the same risk must not be raised twice")
	}
	r.now = r.now.Add(3 * time.Hour) // nobody handled it
	r.s.Tick(ctx)
	if len(r.owner) != 1 || !strings.Contains(r.owner[0], "没人处理") {
		t.Fatalf("escalation to owner: %v", r.owner)
	}
	rs := r.s.Risks("b", false)
	if len(rs) == 0 {
		t.Fatal("no open risks")
	}
	_, _ = r.s.UpdateTask("b", task.ID, nil, nil, nil, nil, nil, "dropped")
	r.s.Tick(ctx)
	if len(r.s.Risks("b", false)) != 0 {
		t.Fatal("risks of a dropped task must close")
	}
}

func TestRolesScopedToProjectAndTime(t *testing.T) {
	r := newRig(t, zhang(true))
	g, _ := r.s.CreateGoal("b", "owner-chat", ownerU, plan.Goal{Title: "十月官网改版"})
	until := r.now.Add(30 * 24 * time.Hour)
	if _, err := r.s.SetRole("b", plan.Role{Worker: "zhangsan", Scope: "org", Title: "设计师", Duties: []string{"视觉稿"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.SetRole("b", plan.Role{Worker: "zhangsan", Scope: g.ID, Title: "设计负责人", Duties: []string{"首页", "验收视觉"}, Until: &until}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.SetRole("b", plan.Role{Worker: "nobody", Title: "x"}); err == nil {
		t.Fatal("role for an unknown worker")
	}
	ctxText := r.s.Context("b", ownerU)
	if !strings.Contains(ctxText, "公司层面：zhangsan 是 设计师") || !strings.Contains(ctxText, "「十月官网改版」：zhangsan 是 设计负责人") {
		t.Fatalf("roles in context:\n%s", ctxText)
	}
	r.now = until.Add(time.Hour)
	if roles := r.s.Roles("b", false); len(roles) != 1 {
		t.Fatalf("project role should have ended: %+v", roles)
	}
	wf := r.s.Workforce(context.Background(), "b", workerScope())
	if !strings.Contains(wf, "zhangsan（张三，人）") || !strings.Contains(wf, "角色：设计师") || !strings.Contains(wf, "owner") {
		t.Fatalf("workforce:\n%s", wf)
	}
}

func TestPersist(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, nil, nil)
	_, _ = s.CreateTask("b", "c", ownerU, TaskSpec{Title: "x"})
	s2, _ := Open(dir, nil, nil)
	if len(s2.Tasks("b", true)) != 1 {
		t.Fatal("not persisted")
	}
	if id := s2.nextID("T"); id != "T2" {
		t.Fatalf("sequence not persisted: %s", id)
	}
}

func workerScope() worker.Scope {
	return worker.Scope{Caller: ownerU}
}
