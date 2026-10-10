package workforce

import (
	"errors"
	"testing"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

var mon10 = time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC) // Monday

func TestCapabilityAndAuthority(t *testing.T) {
	w := Worker{ID: "ops", Kind: AgentSession, Trust: Controlled,
		Capability: []Capability{ParseCapability("gcloud-logging:read"), ParseCapability("repo:tapnow:write")},
		Authority:  []Authority{{Action: "merge:main", Scope: "repo:tapnow"}}}
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	if !w.Can(ParseCapability("repo:tapnow/web:read")) || w.Can(ParseCapability("gcloud-logging:write")) || w.Can(ParseCapability("billing")) {
		t.Fatal("capability coverage")
	}
	if !w.MayApprove("merge:main", "repo:tapnow") || w.MayApprove("deploy:prod", "repo:tapnow") {
		t.Fatal("authority")
	}
	bad := Worker{ID: "x", Kind: AgentSession, Trust: Controlled, Physical: true}
	if bad.Validate() == nil {
		t.Fatal("agents can't act in the real world")
	}
	if (Worker{ID: "p", Kind: Human, Trust: Controlled}).Validate() == nil {
		t.Fatal("people are external")
	}
}

func TestConsent(t *testing.T) {
	owner := Actor{UserID: "u1", Role: RoleOwner}
	member := Actor{UserID: "u2", Role: RoleMember}
	p := Worker{ID: "zs", Kind: Human, Trust: External}
	if p.AcceptsFrom(owner, mon10) {
		t.Fatal("no consent, no work")
	}
	p.Consent = Consent{Given: true}
	if !p.AcceptsFrom(owner, mon10) || p.AcceptsFrom(member, mon10) {
		t.Fatal("default consent is from the owner only")
	}
	agent := Worker{ID: "a", Kind: AgentSession, Trust: Controlled}
	if !agent.AcceptsFrom(owner, mon10) || agent.AcceptsFrom(member, mon10) {
		t.Fatal("controlled agents take work from owner/admin")
	}
}

func TestNormsAndNudges(t *testing.T) {
	n := Norms{Timezone: "UTC", WorkHours: []Window{{Start: "09:00", End: "19:00", Days: []time.Weekday{1, 2, 3, 4, 5}}},
		QuietHours: []Window{{Start: "12:00", End: "13:00"}}, MaxNudgesPerDay: 2, MinNudgeInterval: time.Hour}
	if n.Available(Normal, mon10) != nil {
		t.Fatal("work hours")
	}
	if n.Available(Urgent, mon10.Add(2*time.Hour+30*time.Minute)) == nil {
		t.Fatal("quiet hours")
	}
	if n.Available(Normal, mon10.Add(-48*time.Hour)) == nil { // Saturday
		t.Fatal("weekend")
	}
	if n.Available(Critical, mon10.Add(-48*time.Hour)) != nil {
		t.Fatal("critical ignores hours")
	}
	if err := n.NudgeAllowed([]time.Time{mon10.Add(-30 * time.Minute)}, mon10); err == nil {
		t.Fatal("min interval")
	}
	if err := n.NudgeAllowed([]time.Time{mon10.Add(-5 * time.Hour), mon10.Add(-3 * time.Hour)}, mon10); !errors.Is(err, ErrNudgeBudget) {
		t.Fatalf("budget: %v", err)
	}
	if err := n.NudgeAllowed([]time.Time{mon10.Add(-30 * time.Hour)}, mon10); err != nil {
		t.Fatalf("old nudges don't count: %v", err)
	}
}

func TestStateProvenance(t *testing.T) {
	s := WorkerState{Worker: "zs"}
	s.Record(Fact{Dim: Presence, Value: "available", Source: Observed, At: mon10})
	s.Record(Fact{Dim: Presence, Value: "away", Detail: "休假到周五", Source: Declared, At: mon10})
	s.Record(Fact{Dim: Receptiveness, Value: "low", Source: Inferred, At: mon10, Confidence: 1, Expires: mon10.Add(30 * 24 * time.Hour)})
	if ok, why := s.Available(mon10); ok || why == "" {
		t.Fatal("declared absence wins")
	}
	f, _ := s.Get(Receptiveness, mon10)
	if f.Confidence > 0.8 || f.Expires.Sub(f.At) > MaxInferredTTL {
		t.Fatal("guesses are capped")
	}
	if _, ok := s.Get(Receptiveness, mon10.Add(25*time.Hour)); ok {
		t.Fatal("guesses expire")
	}
	g := WorkerState{Worker: "x"}
	g.Record(Fact{Dim: Presence, Value: "offline", Source: Inferred, At: mon10})
	if ok, _ := g.Available(mon10); !ok {
		t.Fatal("a guess never blocks work")
	}
}
