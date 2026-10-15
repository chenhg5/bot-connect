package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/adapters/store/jsonstore"
	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/inbox"
	"github.com/chenhg5/bot-connect/internal/domain/observe"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
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

type gateProber struct{ out string }

func (g *gateProber) Probe(context.Context, observe.Probe) (string, error) { return g.out, nil }

func TestConditionalWakeupsSkipWhenNothingToShow(t *testing.T) {
	r := newRig(t, jsonstore.Memory())
	a := r.app
	g := &gateProber{}
	a.Prober = g
	_, err := a.ScheduleWakeup(ctx, principal, app.WakeupSpec{Note: "review 开着的 PR", Every: "@every 2h",
		If: &observe.Probe{Kind: observe.Cmd, Target: "gh", Args: []string{"pr", "list"}}})
	must(t, err)
	r.clock.Advance(2 * time.Hour)
	a.Tick(ctx)
	if n := len(wakeupSignals(r)); n != 0 {
		t.Fatal("no open PRs, no wake-up")
	}
	if w := a.Wakeups()[0]; w.Skips != 1 || !strings.Contains(w.Describe(), "跳过 1 次") {
		t.Fatalf("the skip is recorded: %+v", w)
	}
	g.out = "#12 fix login"
	r.clock.Advance(2 * time.Hour)
	a.Tick(ctx)
	got := wakeupSignals(r)
	if len(got) != 1 || !strings.Contains(got[0], "#12 fix login") {
		t.Fatalf("woken with what the condition showed: %v", got)
	}
}

func TestWorkingWindowAndBudget(t *testing.T) {
	r := newRig(t, jsonstore.Memory()) // Tue 10:00 UTC
	a := r.app
	a.Policy.Hours, a.Policy.Days, a.Policy.DailyBudget = "09:00-18:00", "weekdays", 2
	if !a.Policy.InWindow(t0()) || a.Policy.InWindow(t0().Add(9*time.Hour)) {
		t.Fatal("window")
	}
	if nw := a.Policy.NextWindow(time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)); nw.Format("01-02 15:04 Mon") != "10-19 09:00 Mon" {
		t.Fatalf("saturday → monday 9:00: %s", nw.Format("01-02 15:04 Mon"))
	}
	_, err := a.ScheduleWakeup(ctx, principal, app.WakeupSpec{Note: "晚上过一遍", Every: "0 20 * * *", Window: true})
	must(t, err)
	r.clock.Advance(10*time.Hour + time.Minute) // 20:01, outside
	a.Tick(ctx)
	if n := len(wakeupSignals(r)); n != 0 {
		t.Fatal("rhythms keep to the window")
	}
	if w := a.Wakeups()[0]; w.Next.Format("01-02 15:04") != "10-15 09:00" {
		t.Fatalf("moved to the next window: %s", w.Next)
	}
	r.clock.Advance(13 * time.Hour) // Wed 09:01
	a.Tick(ctx)
	if n := len(wakeupSignals(r)); n != 1 {
		t.Fatalf("fires when the window opens: %d", n)
	}
	// use up the budget, then a medium risk waits; a message from the principal doesn't
	r.app.WakeOn(ctx, "c", "S-none") // no such signal: not counted
	sig, _, err := a.Ingest(ctx, app.SignalSpec{Dedupe: "obs:1", Source: inbox.Observation, Reason: "watch_changed", Actor: System("watch"), Summary: "x"})
	must(t, err)
	_ = sig
	for a.OverBudget() == false {
		a.WakeOn(ctx, "c", a.Inbox(true)[0].ID)
	}
	n := len(r.wakes)
	r.clock.Advance(time.Hour)
	a.Tick(ctx)
	if len(r.wakes) != n {
		t.Fatal("over budget: the batch waits for tomorrow")
	}
	_, _, err = a.Ingest(ctx, app.SignalSpec{Dedupe: "msg:1", Source: inbox.DirectMessage, Reason: "dm", Actor: principal, Summary: "在吗"})
	must(t, err)
	if len(r.wakes) != n+1 {
		t.Fatal("the principal is never held by the budget")
	}
	if !strings.Contains(a.Overview(ctx), "今天已唤醒") {
		t.Fatal("the rhythm is visible")
	}
}

func TestRoutinesFromConfig(t *testing.T) {
	r := newRig(t, jsonstore.Memory())
	a := r.app
	must(t, a.SyncRoutines(ctx, []app.WakeupSpec{{Note: "早上过一遍所有项目", Every: "0 9 * * 1-5", Window: true}}, []string{"morning"}))
	ws := a.Wakeups()
	if len(ws) != 1 || ws[0].ID != "RT-morning" || ws[0].Owner != "config" {
		t.Fatalf("routine: %+v", ws)
	}
	if err := a.CancelWakeup(ctx, Actor{Role: RoleSystem, Via: "brain"}, "RT-morning"); err == nil {
		t.Fatal("the brain can't cancel the principal's routine")
	}
	next := ws[0].Next
	must(t, a.SyncRoutines(ctx, []app.WakeupSpec{{Note: "早上过一遍所有项目", Every: "0 9 * * 1-5", Window: true}}, []string{"morning"}))
	if a.Wakeups()[0].Next != next {
		t.Fatal("an unchanged routine keeps its schedule")
	}
	must(t, a.SyncRoutines(ctx, nil, nil))
	if len(a.Wakeups()) != 0 {
		t.Fatal("removed from the configuration, ended")
	}
}
