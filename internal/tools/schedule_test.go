package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/schedule"
	"github.com/chenhg5/bot-connect/internal/worker"
)

func TestScheduleTools(t *testing.T) {
	wm, _ := worker.NewManager(nil, nil, t.TempDir())
	st, _ := schedule.Open(t.TempDir())
	r := New(Env{Bot: "b", Workers: wm, Messenger: nop{}, Schedules: st})
	owner := TurnContext{ConvKey: "c", Caller: identity.User{ID: "me", Role: identity.RoleOwner}}
	member := TurnContext{ConvKey: "c2", Caller: identity.User{ID: "m", Role: identity.RoleMember}}

	out, err := r.Call(context.Background(), owner, "schedule_create", map[string]any{"spec": "0 9 * * 1-5", "prompt": "check CI", "description": "CI"})
	if err != nil || !strings.Contains(out, "next:") {
		t.Fatalf("owner create: %v %s", err, out)
	}
	if _, err := r.Call(context.Background(), member, "schedule_create", map[string]any{"spec": "@daily", "prompt": "x"}); Kind(err) != KindPermission {
		t.Fatalf("member create must be permission_denied, got %v", err)
	}
	if _, err := r.Call(context.Background(), owner, "schedule_create", map[string]any{"spec": "* * * * *", "prompt": "x"}); Kind(err) != KindInvalid {
		t.Fatalf("every-minute spec must be invalid_arguments, got %v", err)
	}
	list, _ := r.Call(context.Background(), owner, "schedule_list", nil)
	id := strings.Fields(strings.TrimPrefix(list, "- "))[0]
	if _, err := r.Call(context.Background(), owner, "schedule_delete", map[string]any{"id": id}); err != nil {
		t.Fatal(err)
	}
	if len(st.List("", "")) != 0 {
		t.Fatal("delete did not remove the job")
	}
}
