package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/worker"
)

func TestWorkerTemplateTools(t *testing.T) {
	wm, _ := worker.NewManager(nil, []config.Template{
		{Name: "scratch", Agent: "claudecode", Access: "workspace", Workspace: "dir", MaxInstances: 2, Description: "throwaway experiments"},
	}, t.TempDir())
	r := New(Env{Bot: "b", Workers: wm, Messenger: nop{}, Scope: worker.Scope{Bot: "b"}})
	ctx := context.Background()
	owner := TurnContext{ConvKey: "c", Caller: identity.User{ID: "me", Role: identity.RoleOwner}}
	member := TurnContext{ConvKey: "c2", Caller: identity.User{ID: "m", Role: identity.RoleMember}}

	if out, _ := r.Call(ctx, owner, "list_workers", nil); !strings.Contains(out, "scratch (claudecode, workspace; fresh empty dir) — throwaway experiments; in use 0/2") {
		t.Fatalf("catalog missing from list_workers:\n%s", out)
	}
	if _, err := r.Call(ctx, member, "worker_create", map[string]any{"template": "scratch", "purpose": "x"}); Kind(err) != KindPermission {
		t.Fatalf("member worker_create: %v", err)
	}
	if _, err := r.Call(ctx, member, "delegate", map[string]any{"template": "scratch", "instruction": "x"}); Kind(err) != KindPermission {
		t.Fatalf("member delegate with template: %v", err)
	}
	out, err := r.Call(ctx, owner, "delegate", map[string]any{"template": "scratch", "worker": "exp", "purpose": "try the new parser", "instruction": "benchmark it"})
	if err != nil || !strings.Contains(out, "Created worker exp from template scratch") || !strings.Contains(out, "on worker exp") {
		t.Fatalf("delegate with template: %v %s", err, out)
	}
	if _, err := r.Call(ctx, owner, "worker_update", map[string]any{"name": "exp", "purpose": "parser perf"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(ctx, owner, "worker_retire", map[string]any{"name": "exp"}); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("busy worker retired without force: %v", err)
	}
	if out, err := r.Call(ctx, owner, "worker_retire", map[string]any{"name": "exp", "force": true}); err != nil || !strings.Contains(out, "Retired exp") {
		t.Fatalf("force retire: %v %s", err, out)
	}
}
