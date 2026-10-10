package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/identity"
)

var owner = identity.User{ID: "me", Role: identity.RoleOwner}

func TestNextSpecs(t *testing.T) {
	now := time.Date(2026, 10, 10, 8, 0, 0, 0, time.Local)
	for spec, want := range map[string]string{
		"0 9 * * 1-5": "2026-10-12 09:00", // Saturday → Monday
		"@daily":      "2026-10-11 00:00",
		"@every 30m":  "2026-10-10 08:30",
		"@once 2h":    "2026-10-10 10:00",
		"@once 07:30": "2026-10-11 07:30", // past today → tomorrow
	} {
		got, _, err := Next(spec, now)
		if err != nil || got.Format("2006-01-02 15:04") != want {
			t.Errorf("%s: %v %v, want %s", spec, got, err, want)
		}
	}
	for _, bad := range []string{"* * * * *", "@every 1m", "nonsense", "@once yesterday"} {
		if _, _, err := Next(bad, now); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if _, _, err := Next("TZ=Asia/Shanghai 0 9 * * *", now); err != nil {
		t.Errorf("TZ prefix: %v", err)
	}
}

func TestOnlyPrivilegedCreate(t *testing.T) {
	s, _ := Open(t.TempDir())
	if _, err := s.Create(Job{Spec: "@daily", Prompt: "x", CreatedBy: identity.User{ID: "v", Role: identity.RoleMember}}); err == nil {
		t.Fatal("members must not create schedules")
	}
}

func TestFireSkipOverlapAndDone(t *testing.T) {
	s, _ := Open(t.TempDir())
	now := time.Date(2026, 10, 10, 8, 0, 0, 0, time.Local)
	s.now = func() time.Time { return now }
	var fired []string
	s.Fire = func(j Job) bool { fired = append(fired, j.ID); return true }
	j, err := s.Create(Job{Spec: "@every 30m", Prompt: "check CI", CreatedBy: owner, ConvKey: "c"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * time.Minute)
	s.Tick()
	now = now.Add(31 * time.Minute) // previous run still in progress
	s.Tick()
	if len(fired) != 1 {
		t.Fatalf("fired %d times, want 1 (second must be skipped while running)", len(fired))
	}
	if g, _ := s.Get(j.ID); g.LastStatus != "skipped_running" {
		t.Fatalf("status %q", g.LastStatus)
	}
	s.Done(j.ID)
	now = now.Add(31 * time.Minute)
	s.Tick()
	if len(fired) != 2 {
		t.Fatalf("after Done it must fire again, fired %d", len(fired))
	}
}

func TestOnceAndAuthorize(t *testing.T) {
	s, _ := Open(t.TempDir())
	now := time.Date(2026, 10, 10, 8, 0, 0, 0, time.Local)
	s.now = func() time.Time { return now }
	n := 0
	s.Fire = func(Job) bool { n++; return true }
	once, _ := s.Create(Job{Spec: "@once 1h", Prompt: "remind", CreatedBy: owner})
	now = now.Add(2 * time.Hour)
	s.Tick()
	s.Done(once.ID)
	now = now.Add(2 * time.Hour)
	s.Tick()
	if n != 1 {
		t.Fatalf("one-shot fired %d times", n)
	}
	allowed := true
	s.Authorize = func(Job) bool { return allowed }
	rec, _ := s.Create(Job{Spec: "@every 30m", Prompt: "x", CreatedBy: owner})
	allowed = false
	now = now.Add(time.Hour)
	s.Tick()
	if g, _ := s.Get(rec.ID); g.Enabled || !strings.HasPrefix(g.LastStatus, "disabled") || n != 1 {
		t.Fatalf("a job whose creator lost rights must be disabled, not fired: %+v n=%d", g, n)
	}
}

func TestReloadPicksUpExternalEdits(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(dir)
	j, _ := a.Create(Job{Spec: "@daily", Prompt: "x", CreatedBy: owner})
	b, _ := Open(dir) // e.g. the CLI
	time.Sleep(10 * time.Millisecond)
	if _, err := b.SetEnabled(j.ID, false); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	_ = os.Chtimes(filepath.Join(dir, "schedules.json"), future, future)
	if g, _ := a.Get(j.ID); g.Enabled {
		t.Fatal("the running store must see the CLI's pause")
	}
}
