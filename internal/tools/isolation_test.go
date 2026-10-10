package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/worker"
)

type recRunner struct{}

func (recRunner) Run(ctx context.Context, spec config.Worker, sid, prompt string) (string, string, error) {
	// leave a file in the worker's dir so we can see where it ran
	_ = os.WriteFile(filepath.Join(spec.WorkDir, "ran.txt"), []byte(prompt), 0o600)
	return "done", "sess-" + filepath.Base(spec.WorkDir), nil
}

func isoSetup(t *testing.T) (*Registry, *worker.Manager, string) {
	data, shared, ro := t.TempDir(), t.TempDir(), t.TempDir()
	wm, err := worker.NewManager([]config.Worker{
		{Name: "proj", Agent: "claudecode", WorkDir: shared, PerUser: "dir"},
		{Name: "docs", Agent: "claudecode", WorkDir: ro, Access: "readonly"},
		{Name: "mine", Agent: "claudecode", WorkDir: t.TempDir()},
	}, nil, data)
	if err != nil {
		t.Fatal(err)
	}
	r := New(Env{Bot: "b", Workers: wm, Messenger: nop{},
		Scope: worker.Scope{AskRoles: map[identity.Role]bool{identity.RoleMember: true}}})
	return r, wm, shared
}

func tcFor(id string, role identity.Role) TurnContext {
	return TurnContext{ConvKey: "c-" + id, Caller: identity.User{ID: id, Name: id, Role: role}}
}

func TestMemberGetsPrivateInstance(t *testing.T) {
	r, wm, shared := isoSetup(t)
	alice, bob := tcFor("ou_alice", identity.RoleMember), tcFor("ou_bob", identity.RoleMember)

	out, err := r.Call(context.Background(), alice, "delegate", map[string]any{"worker": "proj", "instruction": "secret plan A"})
	if err != nil || !strings.Contains(out, "proj@ou_alice") {
		t.Fatalf("alice should run on her own instance: %v %s", err, out)
	}
	if _, err := r.Call(context.Background(), bob, "delegate", map[string]any{"worker": "proj", "instruction": "plan B"}); err != nil {
		t.Fatal(err)
	}
	// instances live in separate dirs, not in the shared work_dir
	if wm.Access("proj@ou_alice", worker.Scope{Caller: alice.Caller}).Read != true {
		t.Fatal("alice must read her own instance")
	}
	if _, err := r.Call(context.Background(), bob, "read_worker", map[string]any{"name": "proj@ou_alice"}); err == nil {
		t.Fatal("bob must not read alice's instance")
	}
	list, _ := r.Call(context.Background(), bob, "list_workers", nil)
	if strings.Contains(list, "ou_alice") || strings.Contains(list, "mine") {
		t.Fatalf("bob's worker list leaks others' workers:\n%s", list)
	}
	if _, err := r.Call(context.Background(), alice, "delegate", map[string]any{"worker": "mine", "instruction": "x"}); err == nil {
		t.Fatal("a member must not use the owner's shared worker")
	}
	if _, err := os.Stat(filepath.Join(shared, "ran.txt")); err == nil {
		t.Fatal("member work must not touch the shared work_dir")
	}
}

func TestReadonlyAskAndOwnerOversight(t *testing.T) {
	r, _, _ := isoSetup(t)
	alice := tcFor("ou_alice", identity.RoleMember)
	if _, err := r.Call(context.Background(), alice, "delegate", map[string]any{"worker": "docs", "instruction": "what does X do?"}); err != nil {
		t.Fatalf("members may ask read-only workers: %v", err)
	}
	if _, err := r.Call(context.Background(), alice, "read_worker", map[string]any{"name": "docs"}); err == nil {
		t.Fatal("asking a read-only worker must not expose its other tasks")
	}
	visitor := tcFor("ou_v", identity.RoleVisitor)
	if _, err := r.Call(context.Background(), visitor, "delegate", map[string]any{"worker": "docs", "instruction": "?"}); err == nil {
		t.Fatal("visitors may not ask unless ask_roles includes visitor")
	}
	owner := tcFor("ou_me", identity.RoleOwner)
	if _, err := r.Call(context.Background(), alice, "delegate", map[string]any{"worker": "proj", "instruction": "x"}); err != nil {
		t.Fatal(err)
	}
	if out, err := r.Call(context.Background(), owner, "read_worker", map[string]any{"name": "proj@ou_alice"}); err != nil || !strings.Contains(out, "proj@ou_alice") {
		t.Fatalf("the owner keeps oversight of member instances: %v", err)
	}
}

func TestRuntimeDenyKeepsOwnDirHidesOthers(t *testing.T) {
	r, wm, _ := isoSetup(t)
	alice, bob := tcFor("ou_alice", identity.RoleMember), tcFor("ou_bob", identity.RoleMember)
	for _, tc := range []TurnContext{alice, bob} {
		if _, err := r.Call(context.Background(), tc, "delegate", map[string]any{"worker": "proj", "instruction": "x"}); err != nil {
			t.Fatal(err)
		}
	}
	deny := strings.Join(wm.DenyFor("proj@ou_alice"), "\n")
	aliceDir := wm.DirOf("proj@ou_alice")
	bobDir := wm.DirOf("proj@ou_bob")
	if !strings.Contains(deny, bobDir) || strings.Contains(deny, aliceDir+"\n") || strings.HasSuffix(deny, aliceDir) {
		t.Fatalf("alice must be denied bob's dir but not her own:\nalice=%s bob=%s\n%s", aliceDir, bobDir, deny)
	}
}
