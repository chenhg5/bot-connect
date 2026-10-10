package tools

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/adapters/store/jsonstore"
	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/inbox"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// The person's words decide a date, not the model's calendar.
func TestDueFollowsThePersonsWords(t *testing.T) {
	mon := time.Date(2026, 10, 12, 10, 0, 0, 0, time.Local)
	pm := app.New(jsonstore.Memory(), NewFakeClock(mon))
	sig, _, err := pm.Ingest(context.Background(), app.SignalSpec{Dedupe: "m1", Source: inbox.DirectMessage, Reason: "dm",
		Actor: Actor{UserID: "u", Role: RoleOwner}, Body: "下周三前把埋点方案定下来"})
	if err != nil {
		t.Fatal(err)
	}
	tc := TurnContext{Focus: []string{sig.ID}}
	for raw, ok := range map[string]bool{
		"下周三":              true,
		"2026-10-21 18:00": true,
		"周三":               false, // this Wednesday: same weekday, other week
		"10-14":            false,
		"10-30":            true, // an unrelated date is fine
	} {
		_, err := dueArg(pm, tc, raw)
		if (err == nil) != ok {
			t.Errorf("%s: err=%v, want ok=%v", raw, err, ok)
		}
	}
}
