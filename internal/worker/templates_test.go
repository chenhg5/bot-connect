package worker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/identity"
)

var (
	ownerScope  = Scope{Bot: "b", Caller: identity.User{ID: "me", Role: identity.RoleOwner}}
	memberScope = Scope{Bot: "b", Caller: identity.User{ID: "m", Role: identity.RoleMember}}
)

func dur(d time.Duration) *config.Duration { return &config.Duration{Duration: d} }

func TestTemplateCreateCapacityAndScope(t *testing.T) {
	data := t.TempDir()
	m, err := NewManager(nil, []config.Template{
		{Name: "scratch", Agent: "claudecode", Access: "workspace", Workspace: "dir", MaxInstances: 2},
		{Name: "secret", Agent: "claudecode", Access: "full", Workspace: "dir", MaxInstances: 1},
	}, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateFromTemplate("scratch", CreateOptions{Purpose: "x", Bot: "b"}, memberScope); err == nil {
		t.Fatal("members must not create workers")
	}
	a, err := m.CreateFromTemplate("scratch", CreateOptions{Purpose: "fix login", Bot: "b", By: ownerScope.Caller}, ownerScope)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "scratch-1" || a.Dir != filepath.Join(data, "workspaces", "scratch", "scratch-1") || a.Description != "fix login" {
		t.Fatalf("unexpected worker %+v", a)
	}
	if st, err := os.Stat(a.Dir); err != nil || !st.IsDir() {
		t.Fatal("workspace dir not created")
	}
	if _, err := m.CreateFromTemplate("scratch", CreateOptions{Name: "bad name!", Bot: "b"}, ownerScope); err == nil {
		t.Fatal("invalid name accepted")
	}
	if _, err := m.CreateFromTemplate("scratch", CreateOptions{Name: "two", Bot: "b"}, ownerScope); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateFromTemplate("scratch", CreateOptions{Bot: "b"}, ownerScope); err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("capacity not enforced: %v", err)
	}

	// The bot's template allowlist, other bots and members.
	limited := ownerScope
	limited.Template = map[string]bool{"scratch": true}
	if _, err := m.CreateFromTemplate("secret", CreateOptions{Bot: "b"}, limited); err == nil {
		t.Fatal("template outside the bot's allowlist accepted")
	}
	other := ownerScope
	other.Bot = "other"
	if m.Access("scratch-1", other).See || m.Access("scratch-1", memberScope).See {
		t.Fatal("a template worker must be visible only to its bot's owner/admins")
	}
	if !m.Access("scratch-1", limited).Delegate {
		t.Fatal("owner can't use their worker")
	}
	if ov := m.Overview(ownerScope); !strings.Contains(ov, "from template scratch") || !strings.Contains(ov, "in use 2/2") {
		t.Fatalf("overview lacks template info:\n%s", ov)
	}
	if ov := m.Overview(memberScope); strings.Contains(ov, "templates") {
		t.Fatalf("members must not see the template catalog:\n%s", ov)
	}

	// Persisted across restarts.
	m2, err := NewManager(nil, []config.Template{{Name: "scratch", Agent: "claudecode", Access: "workspace", Workspace: "dir", MaxInstances: 2}}, data)
	if err != nil {
		t.Fatal(err)
	}
	if !m2.Access("two", ownerScope).Delegate {
		t.Fatal("template worker lost on restart")
	}

	if _, err := m.Retire("scratch-1", false, ownerScope); err != nil {
		t.Fatal(err)
	}
	if m.Access("scratch-1", ownerScope).See {
		t.Fatal("retired worker still listed")
	}
	if _, err := m.CreateFromTemplate("scratch", CreateOptions{Bot: "b"}, ownerScope); err != nil {
		t.Fatalf("retiring must free capacity: %v", err)
	}
}

func TestTemplateExistingRoots(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	sub := filepath.Join(root, "proj")
	_ = os.Mkdir(sub, 0o700)
	m, _ := NewManager(nil, []config.Template{{Name: "rev", Agent: "claudecode", Access: "readonly", Workspace: "existing", Roots: []string{root}, MaxInstances: 3}}, t.TempDir())
	if _, err := m.CreateFromTemplate("rev", CreateOptions{Dir: outside, Bot: "b"}, ownerScope); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("directory outside roots accepted: %v", err)
	}
	if _, err := m.CreateFromTemplate("rev", CreateOptions{Dir: sub + "/../../" + filepath.Base(outside), Bot: "b"}, ownerScope); err == nil {
		t.Fatal("path escape accepted")
	}
	w, err := m.CreateFromTemplate("rev", CreateOptions{Dir: sub, Bot: "b"}, ownerScope)
	if err != nil {
		t.Fatal(err)
	}
	if w.Access != "readonly" {
		t.Fatalf("access must come from the template, got %s", w.Access)
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func TestTemplateWorktreeRetire(t *testing.T) {
	repo := gitRepo(t)
	m, _ := NewManager(nil, []config.Template{{Name: "dev", Agent: "claudecode", Access: "workspace", Workspace: "worktree", Repo: repo, MaxInstances: 3}}, t.TempDir())
	w, err := m.CreateFromTemplate("dev", CreateOptions{Name: "login", Bot: "b"}, ownerScope)
	if err != nil {
		t.Fatal(err)
	}
	if w.Branch != "bot-connect/login" {
		t.Fatalf("branch %q", w.Branch)
	}
	if _, err := os.Stat(filepath.Join(w.Dir, ".git")); err != nil {
		t.Fatal("worktree not created")
	}
	m.mu.Lock()
	writeDirs := m.workers["login"].Spec.WriteDirs
	m.mu.Unlock()
	if len(writeDirs) != 1 || !strings.HasSuffix(filepath.ToSlash(writeDirs[0]), ".git") {
		t.Fatalf("the repo's git dir must be writable for commits: %v", writeDirs)
	}

	_ = os.WriteFile(filepath.Join(w.Dir, "wip.txt"), []byte("x"), 0o600)
	if _, err := m.Retire("login", false, ownerScope); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty worktree retired: %v", err)
	}
	_ = os.Remove(filepath.Join(w.Dir, "wip.txt"))
	msg, err := m.Retire("login", false, ownerScope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Dir); !os.IsNotExist(err) || !strings.Contains(msg, "branch bot-connect/login kept") {
		t.Fatalf("clean worktree should be removed, branch kept: %s", msg)
	}
	// Same name again reuses the kept branch.
	if _, err := m.CreateFromTemplate("dev", CreateOptions{Name: "login", Bot: "b"}, ownerScope); err != nil {
		t.Fatalf("re-create on kept branch: %v", err)
	}
}

func TestReapIdleTemplateWorkers(t *testing.T) {
	m, _ := NewManager(nil, []config.Template{
		{Name: "short", Agent: "claudecode", Access: "workspace", Workspace: "dir", MaxInstances: 3, IdleTTL: dur(time.Hour)},
		{Name: "keep", Agent: "claudecode", Access: "workspace", Workspace: "dir", MaxInstances: 3, IdleTTL: dur(0)},
	}, t.TempDir())
	a, _ := m.CreateFromTemplate("short", CreateOptions{Bot: "b"}, ownerScope)
	k, _ := m.CreateFromTemplate("keep", CreateOptions{Bot: "b"}, ownerScope)
	if got := m.Reap(time.Now().Add(30 * time.Minute)); len(got) != 0 {
		t.Fatalf("reaped too early: %v", got)
	}
	m.mu.Lock()
	m.workers[a.Name].current = &Task{ID: "t1"} // busy
	m.mu.Unlock()
	if got := m.Reap(time.Now().Add(2 * time.Hour)); len(got) != 0 {
		t.Fatalf("reaped a busy worker: %v", got)
	}
	m.mu.Lock()
	m.workers[a.Name].current = nil
	m.mu.Unlock()
	if got := m.Reap(time.Now().Add(2 * time.Hour)); len(got) != 1 || got[0] != a.Name {
		t.Fatalf("reaped %v, want [%s]", got, a.Name)
	}
	if !m.Access(k.Name, ownerScope).See {
		t.Fatal("idle_ttl = 0 must never be reaped")
	}
}
