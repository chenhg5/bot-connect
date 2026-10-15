package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/adapters/store/jsonstore"
	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/observe"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

type fakeProber struct{ out map[string]string }

func (f *fakeProber) Probe(_ context.Context, p observe.Probe) (string, error) {
	if v, ok := f.out[p.String()]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}

func TestWatchesWakeOnlyForNews(t *testing.T) {
	r := newRig(t, jsonstore.Memory())
	a := r.app
	pr := &fakeProber{out: map[string]string{"gh pr list": "#1 fix\n#2 docs"}}
	a.Prober = pr
	p := observe.Probe{Kind: observe.Cmd, Target: "gh", Args: []string{"pr", "list"}}

	if _, _, err := a.AddWatch(ctx, principal, "P3", "开着的 PR", observe.Probe{Kind: observe.Cmd, Target: "gh", Args: []string{"nope"}}, time.Hour, 0, ""); err == nil {
		t.Fatal("a probe that doesn't work is refused at once")
	}
	w, first, err := a.AddWatch(ctx, principal, "P3", "开着的 PR", p, time.Hour, 48*time.Hour, "PR 数量应该下降")
	must(t, err)
	if !strings.Contains(first, "#2 docs") || w.Last.Hash == "" {
		t.Fatalf("the first look is the baseline: %q %+v", first, w.Last)
	}
	if _, _, err := a.AddWatch(ctx, principal, "P3", "又一个", p, time.Hour, 0, ""); err == nil {
		t.Fatal("the same source twice")
	}

	r.clock.Advance(time.Hour)
	a.Tick(ctx)
	sigs := a.Inbox(true)
	for _, s := range sigs {
		if strings.HasPrefix(s.Reason, "watch_") {
			t.Fatalf("nothing changed, nothing raised: %+v", s)
		}
	}
	pr.out["gh pr list"] = "#2 docs\n#3 release"
	r.clock.Advance(time.Hour)
	a.Tick(ctx)
	var changed string
	for _, s := range a.Inbox(true) {
		if s.Reason == "watch_changed" {
			changed = s.Body
		}
	}
	if !strings.Contains(changed, "- #1 fix") || !strings.Contains(changed, "+ #3 release") || !strings.Contains(changed, "PR 数量应该下降") {
		t.Fatalf("a change comes with its diff and what progress looks like: %q", changed)
	}
	r.clock.Advance(49 * time.Hour)
	a.Tick(ctx)
	stale := false
	for _, s := range a.Inbox(true) {
		stale = stale || s.Reason == "watch_stale"
	}
	if !stale {
		t.Fatal("no change for longer than expected is news")
	}

	if !strings.Contains(a.DescribeObserved("P3"), "开着的 PR") {
		t.Fatal(a.DescribeObserved("P3"))
	}
	if err := a.AllowCommand(ctx, Actor{Role: RoleAdmin}, "hp * get"); err == nil {
		t.Fatal("only the principal allows commands")
	}
	if err := a.AllowCommand(ctx, principal, "hp"); err == nil {
		t.Fatal("a bare program would allow everything")
	}
	must(t, a.AllowCommand(ctx, principal, "hp  *  get"))
	if got := a.AllowedCommands(); len(got) != 1 || got[0] != "hp * get" {
		t.Fatal(got)
	}
	_, err = a.WriteDossier(ctx, principal, "P3", "目标：稳定性。进展看 gh pr list。", []observe.Source{{Name: "PR", Probe: p}})
	must(t, err)
	if !strings.Contains(a.DescribeObserved("P3"), "目标：稳定性") {
		t.Fatal("the dossier is shown")
	}
}

type fakeHands struct {
	mu   sync.Mutex
	done []observe.Action
}

func (f *fakeHands) Do(_ context.Context, a observe.Action) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.done = append(f.done, a)
	return "ok: " + a.Describe(), nil
}

func TestActNeedsThePrincipal(t *testing.T) {
	r := newRig(t, jsonstore.Memory())
	a := r.app
	h := &fakeHands{}
	a.Hands = h
	home, _ := os.UserHomeDir()
	handoff := observe.Action{Kind: observe.ActWrite, Path: filepath.Join(home, "code/projects/cc/handoffs/h.md"), Content: "交接"}

	_, err := a.Act(ctx, principal, "P3", handoff, false, "")
	var need app.NeedsApproval
	if !errors.As(err, &need) || !strings.Contains(err.Error(), "handoffs/h.md") {
		t.Fatalf("no permission: ask, saying exactly what: %v", err)
	}
	if _, err := a.Act(ctx, principal, "P3", handoff, true, "S1"); err != nil || len(h.done) != 1 {
		t.Fatalf("approved once: %v", err)
	}
	if err := a.AllowAct(ctx, principal, "write /**"); err == nil {
		t.Fatal("too broad")
	}
	if err := a.AllowAct(ctx, Actor{Role: RoleAdmin}, "write ~/code/projects/*/handoffs/**"); err == nil {
		t.Fatal("only the principal grants")
	}
	must(t, a.AllowAct(ctx, principal, "write ~/code/projects/*/handoffs/**"))
	if _, err := a.Act(ctx, principal, "P3", handoff, false, ""); err != nil || len(h.done) != 2 {
		t.Fatalf("covered by a standing permission: %v", err)
	}
	state := handoff
	state.Path = filepath.Join(home, "code/projects/cc/state/s.json")
	if _, err := a.Act(ctx, principal, "P3", state, false, ""); err == nil {
		t.Fatal("the permission is only for handoffs")
	}
	if got := a.RecentActs("P3", 10); len(got) != 2 || got[0].Grant != "write ~/code/projects/*/handoffs/**" || got[1].Grant != "once" {
		t.Fatalf("audit trail: %+v", got)
	}
	if !strings.Contains(a.DescribeObserved("P3"), "做过") {
		t.Fatal("recent actions are shown with the project")
	}
	// a session hand-off runs in the background and answers as a signal
	_, err = a.Act(ctx, principal, "P3", observe.Action{Kind: observe.ActSession, Harness: "codex", Session: "S1", Message: "继续"}, true, "S9")
	must(t, err)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range a.Inbox(true) {
			if s.Reason == "act_result" {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the session's answer comes back as a signal")
}

func TestConfirmationsBindToActions(t *testing.T) {
	r := newRig(t, jsonstore.Memory())
	a := r.app
	h := &fakeHands{}
	a.Hands = h
	home, _ := os.UserHomeDir()
	w := func(name string) observe.Action {
		return observe.Action{Kind: observe.ActWrite, Path: filepath.Join(home, "code/projects/cc/state", name), Content: "x"}
	}
	if _, err := a.Act(ctx, principal, "P3", w("a.json"), true, "S1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Act(ctx, principal, "P3", w("b.json"), true, "S1"); err == nil {
		t.Fatal("one message of the principal confirms one action")
	}
	p, err := a.ProposeActs(ctx, principal, "P3", []observe.Action{w("b.json"), w("c.json")})
	must(t, err)
	if !strings.Contains(p.Describe(), "b.json") || !strings.Contains(p.Describe(), "c.json") {
		t.Fatal(p.Describe())
	}
	lines, err := a.CarryOut(ctx, principal, p.ID)
	if err != nil || len(lines) != 2 || len(h.done) != 3 {
		t.Fatalf("carried out as proposed: %v %v", err, lines)
	}
	if _, err := a.CarryOut(ctx, principal, p.ID); err == nil {
		t.Fatal("a proposal runs once")
	}
	if got := a.RecentActs("P3", 1); got[0].Grant != "proposal "+p.ID {
		t.Fatalf("audit says which approval: %+v", got[0])
	}
}

type codeProber struct{}

func (codeProber) Probe(_ context.Context, p observe.Probe) (string, error) {
	if p.Kind == observe.ToolProbe {
		if p.Code == "" {
			return "", errors.New("unresolved")
		}
		return "ran " + observe.CodeHash(p.Code), nil
	}
	return "x", nil
}

func TestToolboxNeedsApprovalOfTheCode(t *testing.T) {
	r := newRig(t, jsonstore.Memory())
	a := r.app
	a.Prober = codeProber{}
	a.Hands = &fakeHands{}
	tool := observe.Probe{Kind: observe.ToolProbe, Target: "backlog"}
	tl, err := a.AddTool(ctx, principal, "P3", "backlog", "counts open items", observe.ToolLook, "python3", "print(1)")
	must(t, err)
	if _, err := a.Look(ctx, principal, tool); err == nil {
		t.Fatal("not approved yet")
	}
	if _, _, err := a.AddWatch(ctx, principal, "P3", "存量", tool, time.Hour, 0, ""); err == nil {
		t.Fatal("can't watch an unapproved tool")
	}
	if _, err := a.ApproveTool(ctx, Actor{Role: RoleAdmin}, "backlog", tl.Hash); err == nil {
		t.Fatal("only the principal approves")
	}
	if _, err := a.ApproveTool(ctx, principal, "backlog", "0000"); err == nil {
		t.Fatal("approving a version that isn't the current code")
	}
	_, err = a.ApproveTool(ctx, principal, "backlog", tl.Hash)
	must(t, err)
	out, err := a.Look(ctx, principal, tool)
	if err != nil || out != "ran "+tl.Hash {
		t.Fatalf("approved code runs: %v %q", err, out)
	}
	_, _, err = a.AddWatch(ctx, principal, "P3", "存量", tool, time.Hour, 0, "")
	must(t, err)
	_, err = a.AddTool(ctx, principal, "P3", "backlog", "counts open items", observe.ToolLook, "python3", "print(2)")
	must(t, err)
	if _, err := a.Look(ctx, principal, tool); err == nil {
		t.Fatal("changed code needs approval again")
	}
	if _, err := a.Act(ctx, principal, "P3", observe.Action{Kind: observe.ActTool, Program: "backlog"}, true, "S1"); err == nil {
		t.Fatal("a look tool is not an action")
	}
	if got, _ := a.Tool("backlog"); got.Uses != 2 {
		t.Fatalf("uses counted: %d", got.Uses)
	}
}
