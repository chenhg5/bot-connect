package observe

import (
	"errors"
	"strings"
	"testing"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

func TestWatchFindings(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	w, err := NewWatch("W1", "P1", "open PRs", Probe{Kind: Cmd, Target: "gh", Args: []string{"pr", "list"}}, time.Hour, 48*time.Hour, "", t0, Actor{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewWatch("W2", "P1", "x", Probe{Kind: Cmd, Target: "gh"}, time.Minute, 0, "", t0, Actor{}); err == nil {
		t.Fatal("too frequent")
	}
	if f, _ := w.Record(Observe("a\nb", nil, t0)); f != First {
		t.Fatalf("baseline: %s", f)
	}
	if w.Due(t0.Add(30*time.Minute)) || !w.Due(t0.Add(time.Hour)) {
		t.Fatal("due on its rhythm")
	}
	if f, _ := w.Record(Observe("a\nb", nil, t0.Add(time.Hour))); f != Nothing {
		t.Fatalf("same content: %s", f)
	}
	f, prev := w.Record(Observe("a\nc", nil, t0.Add(2*time.Hour)))
	if f != Changed || !strings.Contains(Diff(prev.Text, w.Last.Text, 10), "+ c") {
		t.Fatalf("changed: %s %q", f, Diff(prev.Text, w.Last.Text, 10))
	}
	for i := 1; i < FailAfter; i++ {
		if f, _ := w.Record(Observe("", errors.New("boom"), t0.Add(time.Duration(2+i)*time.Hour))); f != Nothing {
			t.Fatalf("one failure is not news: %s", f)
		}
	}
	if f, _ := w.Record(Observe("", errors.New("boom"), t0.Add(6*time.Hour))); f != Failing || w.Last.Text != "a\nc" {
		t.Fatalf("failing, keeping the last good content: %s %q", f, w.Last.Text)
	}
	if f, _ := w.Record(Observe("a\nc", nil, t0.Add(51*time.Hour))); f != Stale {
		t.Fatalf("no change for 48h: %s", f)
	}
	if f, _ := w.Record(Observe("a\nc", nil, t0.Add(52*time.Hour))); f != Nothing {
		t.Fatalf("stale is said once per period: %s", f)
	}
}

func TestDossierLimit(t *testing.T) {
	var d Dossier
	if err := d.Write(strings.Repeat("字", DossierLimit+1), nil, time.Now()); err == nil {
		t.Fatal("too long")
	}
	if err := d.Write("目标…", []Source{{Name: "x", Probe: Probe{Kind: "ftp"}}}, time.Now()); err == nil {
		t.Fatal("bad probe")
	}
}

func TestPermits(t *testing.T) {
	home := "/home/c"
	w := Action{Kind: ActWrite, Path: "/home/c/code/projects/cc/handoffs/2026-10-15.md", Content: "x"}
	if !Permits("write ~/code/projects/*/handoffs/**", w, home) {
		t.Fatal("a handoff in any project")
	}
	if Permits("write ~/code/projects/*/handoffs/**", Action{Kind: ActWrite, Path: "/home/c/code/projects/cc/state/s.json"}, home) {
		t.Fatal("not the state file")
	}
	if Permits("write ~/code/projects/*/handoffs/**", Action{Kind: ActWrite, Path: "/home/c/code/projects/cc/handoffs/../state/s.json"}, home) {
		t.Fatal("no escaping with ..")
	}
	c := Action{Kind: ActCmd, Program: "hp", Args: []string{"task", "update", "t1", "--status", "done"}}
	if !Permits("cmd hp task update *", c, home) || Permits("cmd hp task create *", c, home) || Permits("write hp", c, home) {
		t.Fatal("cmd patterns")
	}
	s := Action{Kind: ActSession, Harness: "codex", Session: "01a1", Message: "继续"}
	if !Permits("session codex *", s, home) || Permits("session claude *", s, home) {
		t.Fatal("session patterns")
	}
	for _, bad := range []string{"cmd hp", "cmd hp *", "write /**", "write ~/**", "write /tmp", "session codex", "rm -rf"} {
		if ValidPermission(bad) == nil {
			t.Fatalf("%q is too broad or malformed", bad)
		}
	}
	if err := ValidPermission("write ~/code/projects/*/handoffs/**"); err != nil {
		t.Fatal(err)
	}
}
