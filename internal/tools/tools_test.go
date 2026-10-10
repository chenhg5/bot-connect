package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/session"
	"github.com/chenhg5/bot-connect/internal/worker"
)

type fakeStore struct{ list []session.Info }

func (f fakeStore) Agent() string { return "claudecode" }
func (f fakeStore) List(limit int, keep func(string) bool) ([]session.Info, error) {
	var out []session.Info
	for _, in := range f.list {
		if keep == nil || keep(in.Dir) {
			out = append(out, in)
		}
	}
	return out, nil
}
func (f fakeStore) Find(id string) (session.Info, error) {
	for _, in := range f.list {
		if in.ID == id {
			return in, nil
		}
	}
	return session.Info{}, fmt.Errorf("none")
}
func (f fakeStore) History(id string, limit int) ([]session.Entry, error) {
	if _, err := f.Find(id); err != nil {
		return nil, err
	}
	return []session.Entry{{Role: "user", Text: "secret of " + id, Time: time.Now()}}, nil
}

type nop struct{}

func (nop) NotifyOwner(context.Context, string) error    { return nil }
func (nop) SendTo(context.Context, string, string) error { return nil }

func setup(t *testing.T, dirSessions bool, botWorkers map[string]bool) (*Registry, TurnContext) {
	dir, other := t.TempDir(), t.TempDir()
	cat := session.NewCatalog(fakeStore{list: []session.Info{
		{Agent: "claudecode", ID: "s-listed", Dir: dir, Title: "listed", Updated: time.Now()},
		{Agent: "claudecode", ID: "s-same-dir", Dir: dir, Title: "same dir", Updated: time.Now()},
		{Agent: "claudecode", ID: "s-private", Dir: other, Title: "private", Updated: time.Now()},
	}})
	wm, err := worker.NewManager([]config.Worker{{Name: "w", Agent: "claudecode", WorkDir: dir,
		Sessions: []string{"s-listed"}, DirSessions: dirSessions}}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wm.Sessions = cat
	r := New(Env{Bot: "b", Workers: wm, Sessions: cat, Messenger: nop{}, Scope: worker.Scope{Names: botWorkers}})
	return r, TurnContext{ConvKey: "c", Caller: identity.User{ID: "me", Role: identity.RoleOwner}}
}

func call(t *testing.T, r *Registry, tc TurnContext, name string, a map[string]any) (string, error) {
	t.Helper()
	return r.Call(context.Background(), tc, name, a)
}

func TestSessionsOnlyThroughWorkers(t *testing.T) {
	r, tc := setup(t, false, nil)
	out, _ := call(t, r, tc, "list_sessions", nil)
	if !strings.Contains(out, "s-listed") || strings.Contains(out, "s-same-dir") || strings.Contains(out, "s-private") {
		t.Fatalf("list_sessions leaked or missed sessions:\n%s", out)
	}
	if _, err := call(t, r, tc, "read_session", map[string]any{"session": "s-private"}); err == nil {
		t.Fatal("read_session must refuse a session no worker covers")
	}
	if out, err := call(t, r, tc, "read_session", map[string]any{"session": "s-listed"}); err != nil || !strings.Contains(out, "secret of s-listed") {
		t.Fatalf("read_session on an allowed session: %v %s", err, out)
	}
	if _, err := call(t, r, tc, "delegate", map[string]any{"session": "s-private", "instruction": "x"}); err == nil {
		t.Fatal("delegate must refuse a session no worker covers")
	}
	out, err := call(t, r, tc, "delegate", map[string]any{"session": "s-listed", "instruction": "x"})
	if err != nil || !strings.Contains(out, "w#s-listed") {
		t.Fatalf("delegate into a listed session should run on sub-worker w#s-listed: %v %s", err, out)
	}
}

func TestDirSessions(t *testing.T) {
	r, tc := setup(t, true, nil)
	out, _ := call(t, r, tc, "list_sessions", nil)
	if !strings.Contains(out, "s-same-dir") || strings.Contains(out, "s-private") {
		t.Fatalf("dir_sessions should add same-dir sessions only:\n%s", out)
	}
}

func TestBotScope(t *testing.T) {
	r, tc := setup(t, true, map[string]bool{"someone-else": true})
	out, _ := call(t, r, tc, "list_sessions", nil)
	if strings.Contains(out, "s-") {
		t.Fatalf("a bot without worker w must not see its sessions:\n%s", out)
	}
	if _, err := call(t, r, tc, "read_worker", map[string]any{"name": "w"}); err == nil {
		t.Fatal("read_worker outside the bot's scope must fail")
	}
}
