package reach

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fake struct {
	ch   Channel
	fail bool
	sent *[]Channel
}

func (f fake) Channel() Channel { return f.ch }
func (f fake) Send(ctx context.Context, r Route, m Message) (Receipt, error) {
	if f.fail {
		return Receipt{}, errors.New("down")
	}
	*f.sent = append(*f.sent, f.ch)
	return Receipt{Channel: f.ch, Address: r.Address}, nil
}

var ladder = []Route{
	{Channel: FeishuDM, Address: "ou_1"},
	{Channel: FeishuUrgentApp, Address: "ou_1", MinUrgency: Reminder},
	{Channel: FeishuUrgentPhone, Address: "ou_1", MinUrgency: Critical, Approval: true},
}

func TestWindows(t *testing.T) {
	night := Window{Start: "22:00", End: "08:00"}
	mon := time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC) // Monday
	if !night.Contains(mon) || !night.Contains(mon.Add(8*time.Hour)) || night.Contains(mon.Add(-12*time.Hour)) {
		t.Fatal("wrapping window")
	}
	weekdays := Window{Start: "09:00", End: "18:00", Days: []time.Weekday{1, 2, 3, 4, 5}}
	if !weekdays.Contains(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)) || weekdays.Contains(time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)) {
		t.Fatal("weekday window")
	}
	if (Window{Start: "9", End: "18:00"}).Validate() == nil {
		t.Fatal("bad time accepted")
	}
}

func TestPlanRespectsHoursAndUrgency(t *testing.T) {
	h := Hours{Location: time.UTC, WorkHours: []Window{{Start: "09:00", End: "19:00"}}, QuietHours: []Window{{Start: "12:00", End: "13:00"}}}
	day := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	if p, _ := Plan(ladder, Message{Urgency: Normal}, h, day); len(p) != 1 || p[0].Channel != FeishuDM {
		t.Fatalf("normal: %v", p)
	}
	if p, _ := Plan(ladder, Message{Urgency: Reminder}, h, day); len(p) != 2 {
		t.Fatalf("reminder: %v", p)
	}
	if _, err := Plan(ladder, Message{Urgency: Urgent}, h, day.Add(2*time.Hour+10*time.Minute)); err == nil || !strings.Contains(err.Error(), "quiet") {
		t.Fatalf("quiet hours not honoured: %v", err)
	}
	if _, err := Plan(ladder, Message{Urgency: Urgent}, h, day.Add(12*time.Hour)); err == nil {
		t.Fatal("outside work hours a non-critical message must wait")
	}
	if p, err := Plan(ladder, Message{Urgency: Critical}, h, day.Add(12*time.Hour)); err != nil || len(p) != 3 {
		t.Fatalf("critical goes out any time: %v %v", p, err)
	}
}

func TestDispatcherEscalatesAndFallsBack(t *testing.T) {
	var sent []Channel
	d := NewDispatcher(fake{ch: FeishuDM, sent: &sent}, fake{ch: FeishuUrgentApp, fail: true, sent: &sent}, fake{ch: FeishuUrgentPhone, sent: &sent})
	// Critical without approval: phone is skipped, urgent_app fails, falls back to DM.
	rc, err := d.Send(context.Background(), ladder, Message{Urgency: Critical}, nil)
	if err != nil || rc.Channel != FeishuDM {
		t.Fatalf("fallback: %v %v", rc, err)
	}
	rc, err = d.Send(context.Background(), ladder, Message{Urgency: Critical}, func(Route) bool { return true })
	if err != nil || rc.Channel != FeishuUrgentPhone {
		t.Fatalf("approved phone call: %v %v", rc, err)
	}
	if _, err := NewDispatcher().Send(context.Background(), ladder[:1], Message{}, nil); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured channel: %v", err)
	}
}
