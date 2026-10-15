package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/adapters/store/jsonstore"
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
