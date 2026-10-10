// Package reach is how bot-connect gets a message to a person (or another
// bot) outside the conversation it is in: a Feishu direct message, a Feishu
// urgent notification (in-app, SMS, phone call), email, a phone call through
// a provider, a webhook. Each way is a Channel with a Reacher; a worker's
// profile lists its Routes in escalation order, and Plan picks which to use
// for a message's urgency at this moment, honouring quiet hours.
package reach

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Channel is one way to reach someone.
type Channel string

const (
	FeishuDM          Channel = "feishu_dm"           // direct message from the bot
	FeishuUrgentApp   Channel = "feishu_urgent_app"   // Feishu "加急": in-app buzz on a sent message
	FeishuUrgentSMS   Channel = "feishu_urgent_sms"   // Feishu "加急": SMS (uses tenant quota)
	FeishuUrgentPhone Channel = "feishu_urgent_phone" // Feishu "加急": phone call (uses tenant quota)
	Email             Channel = "email"
	SMS               Channel = "sms"
	Phone             Channel = "phone" // voice call through a provider
	Webhook           Channel = "webhook"
	A2A               Channel = "a2a" // an Agent2Agent endpoint (bots)
)

// Urgency of a message, in increasing order.
type Urgency int

const (
	Normal   Urgency = iota // an offer, an answer, a status note
	Reminder                // a nudge about something pending
	Urgent                  // overdue, or blocking others
	Critical                // needs a human now; may break quiet hours
)

func (u Urgency) String() string {
	return [...]string{"normal", "reminder", "urgent", "critical"}[u]
}

// ParseUrgency reads "normal" | "reminder" | "urgent" | "critical".
func ParseUrgency(s string) (Urgency, error) {
	for u := Normal; u <= Critical; u++ {
		if u.String() == s {
			return u, nil
		}
	}
	return Normal, fmt.Errorf("urgency must be normal, reminder, urgent or critical, not %q", s)
}

// Route is one way to reach one person.
type Route struct {
	Channel Channel `json:"channel" toml:"channel"`
	// Address on that channel: open_id / union_id / email / phone number / URL.
	Address string `json:"address" toml:"address"`
	// MinUrgency: only used for messages at least this urgent (a phone call
	// is for urgent things, not for an offer).
	MinUrgency Urgency `json:"min_urgency,omitempty" toml:"-"`
	MinLevel   string  `json:"-" toml:"min_urgency"` // config form of MinUrgency
	// Approval: the owner must approve each use (costly or intrusive channels).
	Approval bool `json:"approval,omitempty" toml:"approval"`
}

// Message is what to deliver.
type Message struct {
	Title   string
	Text    string
	Ref     string // what it is about (assignment id), for threading / replies
	Urgency Urgency
	// Prior receipts for the same Ref: urgent channels like Feishu 加急 act
	// on an already-sent message rather than sending a new one.
	Prior []Receipt
}

// Receipt records a delivery.
type Receipt struct {
	Channel   Channel   `json:"channel"`
	Address   string    `json:"address"`
	MessageID string    `json:"message_id,omitempty"`
	At        time.Time `json:"at"`
}

// Reacher delivers messages on one channel.
type Reacher interface {
	Channel() Channel
	Send(ctx context.Context, r Route, m Message) (Receipt, error)
}

// Window is a daily time range "HH:MM"–"HH:MM" in the person's time zone,
// optionally only on some weekdays. End before Start wraps past midnight.
type Window struct {
	Start string         `json:"start" toml:"start"`
	End   string         `json:"end" toml:"end"`
	Days  []time.Weekday `json:"days,omitempty" toml:"days"`
}

func minutes(hhmm string) (int, error) {
	h, m, ok := strings.Cut(hhmm, ":")
	if !ok {
		return 0, fmt.Errorf("time %q is not HH:MM", hhmm)
	}
	hi, err1 := strconv.Atoi(h)
	mi, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hi < 0 || hi > 24 || mi < 0 || mi > 59 {
		return 0, fmt.Errorf("time %q is not HH:MM", hhmm)
	}
	return hi*60 + mi, nil
}

// Validate checks the window's times.
func (w Window) Validate() error {
	if _, err := minutes(w.Start); err != nil {
		return err
	}
	_, err := minutes(w.End)
	return err
}

// Contains reports whether t (already in the right location) is inside.
func (w Window) Contains(t time.Time) bool {
	s, err1 := minutes(w.Start)
	e, err2 := minutes(w.End)
	if err1 != nil || err2 != nil {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	day := t.Weekday()
	inside := false
	if s <= e {
		inside = m >= s && m < e
	} else { // wraps midnight: the part after midnight belongs to the previous day's window
		if m >= s {
			inside = true
		} else if m < e {
			inside, day = true, (day+6)%7
		}
	}
	if !inside || len(w.Days) == 0 {
		return inside
	}
	for _, d := range w.Days {
		if d == day {
			return true
		}
	}
	return false
}

// In reports whether t falls in any window (in loc).
func In(ws []Window, t time.Time, loc *time.Location) bool {
	if loc != nil {
		t = t.In(loc)
	}
	for _, w := range ws {
		if w.Contains(t) {
			return true
		}
	}
	return false
}

// Hours are a person's availability rules.
type Hours struct {
	Location   *time.Location
	WorkHours  []Window // empty = any time
	QuietHours []Window
}

// Plan returns the routes to try for a message now, cheapest first, or why
// none may be used. Outside work hours and inside quiet hours only Critical
// messages go out. Routes needing approval are returned too; the caller
// must hold them until the owner approves.
func Plan(routes []Route, m Message, h Hours, now time.Time) ([]Route, error) {
	if m.Urgency < Critical {
		if In(h.QuietHours, now, h.Location) {
			return nil, fmt.Errorf("quiet hours; only critical messages go out now")
		}
		if len(h.WorkHours) > 0 && !In(h.WorkHours, now, h.Location) {
			return nil, fmt.Errorf("outside work hours; only critical messages go out now")
		}
	}
	var out []Route
	for _, r := range routes {
		if r.MinUrgency <= m.Urgency {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no contact route for a %s message", m.Urgency)
	}
	return out, nil
}

// Dispatcher sends through registered reachers.
type Dispatcher struct {
	reachers map[Channel]Reacher
}

func NewDispatcher(rs ...Reacher) *Dispatcher {
	d := &Dispatcher{reachers: map[Channel]Reacher{}}
	for _, r := range rs {
		d.reachers[r.Channel()] = r
	}
	return d
}

// Supports reports whether a channel has a reacher.
func (d *Dispatcher) Supports(c Channel) bool { return d.reachers[c] != nil }

// Send delivers via the most escalated usable route in plan (plan is in
// escalation order, so the last route that fits the urgency is the one the
// message is "for"); approval-gated routes are skipped unless approved.
// If it fails, it falls back down the ladder.
func (d *Dispatcher) Send(ctx context.Context, plan []Route, m Message, approved func(Route) bool) (Receipt, error) {
	var errs []string
	for i := len(plan) - 1; i >= 0; i-- {
		r := plan[i]
		if r.Approval && (approved == nil || !approved(r)) {
			errs = append(errs, string(r.Channel)+": needs the owner's approval")
			continue
		}
		rc := d.reachers[r.Channel]
		if rc == nil {
			errs = append(errs, string(r.Channel)+": not configured")
			continue
		}
		rec, err := rc.Send(ctx, r, m)
		if err == nil {
			return rec, nil
		}
		errs = append(errs, string(r.Channel)+": "+err.Error())
	}
	return Receipt{}, fmt.Errorf("could not reach: %s", strings.Join(errs, "; "))
}
