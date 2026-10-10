package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/adapters/drivers/human"
	"github.com/chenhg5/bot-connect/internal/adapters/store/jsonstore"
	"github.com/chenhg5/bot-connect/internal/app"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/worker"
)

func TestPMToolsEndToEnd(t *testing.T) {
	ctx := context.Background()
	clock := NewFakeClock(time.Date(2026, 10, 14, 10, 0, 0, 0, time.Local))
	pm := app.New(jsonstore.Memory(), clock)
	var dms []string
	pm.RegisterDriver(string(workforce.Human), &human.Driver{Clock: clock, Reachers: map[workforce.RouteKind]human.Reacher{
		human.FeishuDM: human.ReacherFunc(func(_ context.Context, addr, text string, _ workforce.Urgency) (string, error) {
			dms = append(dms, addr+": "+text)
			return "m", nil
		})}})
	var wakes []string
	pm.Waker = app.WakerFunc(func(_ context.Context, tr app.Trigger) { wakes = append(wakes, tr.Text) })
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pm.EnsureOrg(ctx, "公司"))
	must(pm.UpsertWorker(ctx, System("t"), workforce.Worker{ID: "wangwu", Name: "王五", Kind: workforce.Human, Trust: workforce.External,
		Consent: workforce.Consent{Given: true}, Identities: []string{"feishu:ou_ww"}, Contact: []workforce.Route{{Kind: human.FeishuDM, Address: "ou_ww"}}}))
	wm, _ := worker.NewManager(nil, nil, t.TempDir())
	r := New(Env{Bot: "b", Workers: wm, Messenger: nop{}, App: pm})
	owner := TurnContext{ConvKey: "c-owner", Caller: identity.User{ID: "u-owner", Role: identity.RoleOwner}}
	ww := TurnContext{ConvKey: "c-ww", Caller: identity.User{ID: "ou_ww", Role: identity.RoleVisitor, Name: "王五"}}
	stranger := TurnContext{ConvKey: "c-x", Caller: identity.User{ID: "ou_x", Role: identity.RoleVisitor}}
	call := func(tc TurnContext, name string, a map[string]any) string {
		t.Helper()
		out, err := r.Call(ctx, tc, name, a)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}

	out := call(owner, "project_setup", map[string]any{"title": "十月官网改版", "id": "P1", "priority": "P1", "until": "2026-10-31", "objective": "上线新首页"})
	if !strings.Contains(out, "P1") {
		t.Fatal(out)
	}
	call(owner, "project_update", map[string]any{"project": "P1", "member_worker": "wangwu", "member_role": "数据负责人", "member_duties": []any{"埋点", "指标"}})
	call(owner, "plan_item", map[string]any{"project": "P1", "title": "定埋点方案", "due": "2026-10-17 18:00", "estimate": "1d", "done": []any{"指标清单", "事件定义"}})
	if out := call(owner, "find_people", map[string]any{"need": "埋点方案", "project": "P1"}); !strings.HasPrefix(out, "- wangwu") || !strings.Contains(out, "数据负责人") {
		t.Fatalf("find_people: %s", out)
	}
	if _, err := r.Call(ctx, owner, "delegate", map[string]any{"item": "I1", "worker": "wangwu"}); Kind(err) != KindInvalid {
		t.Fatalf("a person without a reason: %v", err)
	}
	out = call(owner, "delegate", map[string]any{"item": "I1", "worker": "wangwu", "why": "knowledge", "goal": "定埋点方案（指标、事件、字段）"})
	if !strings.HasPrefix(out, "A1 → wangwu") || len(dms) != 1 || !strings.Contains(dms[0], "定埋点方案") {
		t.Fatalf("delegate: %s %v", out, dms)
	}
	if _, err := r.Call(ctx, stranger, "respond", map[string]any{"assignment": "A1", "action": "accept"}); Kind(err) != KindPermission {
		t.Fatalf("a stranger answered for wangwu: %v", err)
	}
	if _, err := r.Call(ctx, stranger, "plan_item", map[string]any{"title": "x"}); Kind(err) != KindPermission {
		t.Fatalf("visitors can't plan: %v", err)
	}
	call(ww, "respond", map[string]any{"assignment": "A1", "action": "counter", "due": "2026-10-20 12:00", "note": "周五排不过来"})
	if len(wakes) == 0 || !strings.Contains(wakes[len(wakes)-1], "还价") {
		t.Fatalf("counter reported: %v", wakes)
	}
	call(owner, "review", map[string]any{"assignment": "A1", "action": "accept_counter"})
	call(ww, "respond", map[string]any{"assignment": "A1", "action": "deliver", "result": "方案文档", "evidence": []any{"https://doc/x"}})
	if out := call(owner, "brief", map[string]any{"project": "P1"}); !strings.Contains(out, "已交付待验收") || !strings.Contains(out, "waiting_on_us") {
		t.Fatalf("brief shows the delivery and the agenda: %s", out)
	}
	call(owner, "review", map[string]any{"assignment": "A1", "action": "verify"})
	if out := call(owner, "brief", map[string]any{"project": "P1"}); !strings.Contains(out, "进度：100%") {
		t.Fatalf("done: %s", out)
	}
	if v := pm.AssigneeView("wangwu"); v != "" {
		t.Fatalf("nothing open for wangwu: %s", v)
	}
}
