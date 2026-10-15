package app_test

import (
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/adapters/store/jsonstore"
)

func wakeupSignals(r *rig) []string {
	var out []string
	for _, s := range r.app.Inbox(true) {
		if s.Reason == "wakeup" {
			out = append(out, s.Body)
		}
	}
	return out
}

func TestWakeups(t *testing.T) {
	r := newRig(t, jsonstore.Memory()) // t0: Tue 2026-10-14 10:00
	a := r.app
	if _, err := a.AddWakeup(ctx, principal, "", "2h", "", "", ""); err == nil {
		t.Fatal("a note is needed")
	}
	if _, err := a.AddWakeup(ctx, principal, "x", "2h", "@daily", "", ""); err == nil {
		t.Fatal("once or a rhythm, not both")
	}
	if _, err := a.AddWakeup(ctx, principal, "x", "", "@every 1m", "", ""); err == nil {
		t.Fatal("too frequent")
	}
	once, err := a.AddWakeup(ctx, principal, "跟 Dana 确认上线审批", "周五 15:00", "", "P3", "")
	must(t, err)
	if once.Next.Weekday() != time.Friday || once.Next.Hour() != 15 {
		t.Fatalf("friday 15:00: %s", once.Next)
	}
	weekly, err := a.AddWakeup(ctx, principal, "出官网项目周报", "", "0 9 * * 1", "P3", "")
	must(t, err)
	gone, err := a.AddWakeup(ctx, principal, "不用了", "3h", "", "", "")
	must(t, err)
	must(t, a.CancelWakeup(ctx, principal, gone.ID))
	if !strings.Contains(a.Overview(ctx), "接下来的唤醒") {
		t.Fatal("the overview shows what's coming")
	}

	r.clock.Advance(4 * time.Hour)
	a.Tick(ctx)
	if n := len(wakeupSignals(r)); n != 0 {
		t.Fatalf("nothing due yet (the cancelled one stays quiet): %d", n)
	}
	r.clock.Advance(3*24*time.Hour + 2*time.Hour) // Fri 16:00
	a.Tick(ctx)
	got := wakeupSignals(r)
	if len(got) != 1 || !strings.Contains(got[0], "Dana") {
		t.Fatalf("the friday reminder: %v", got)
	}
	if len(r.wakes) == 0 || !strings.Contains(wakeText(r.lastWake()), "Dana") {
		t.Fatal("a wakeup wakes the brain at once")
	}
	r.clock.Advance(3 * 24 * time.Hour) // Mon 16:00, past 9:00
	a.Tick(ctx)
	if got := wakeupSignals(r); len(got) != 2 || !strings.Contains(got[1]+got[0], "周报") {
		t.Fatalf("the weekly report: %v", got)
	}
	r.clock.Advance(time.Hour)
	a.Tick(ctx)
	if got := wakeupSignals(r); len(got) != 2 {
		t.Fatalf("once per week: %d", len(got))
	}
	ws := a.Wakeups()
	if len(ws) != 1 || ws[0].ID != weekly.ID || ws[0].Next.Weekday() != time.Monday || ws[0].Next.Format("01-02 15:04") != "10-26 09:00" {
		t.Fatalf("only the rhythm remains, next monday: %+v", ws)
	}
}
