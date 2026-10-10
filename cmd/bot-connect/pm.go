package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/adapters/drivers/agentsession"
	"github.com/chenhg5/bot-connect/internal/adapters/drivers/human"
	"github.com/chenhg5/bot-connect/internal/adapters/store/jsonstore"
	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/audit"
	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/domain/inbox"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
	"github.com/chenhg5/bot-connect/internal/hub"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/platform/console"
	"github.com/chenhg5/bot-connect/internal/worker"
)

// auditSink writes domain events to the audit log.
type auditSink struct {
	bot  string
	sink audit.Sink
}

func (s auditSink) Publish(_ context.Context, evs []Event) {
	for _, e := range evs {
		extra := map[string]any{"subject": e.Subject}
		for k, v := range e.Data {
			extra[k] = v
		}
		by := identity.User{ID: firstNonEmpty(e.By.UserID, string(e.By.Worker), e.By.Via), Role: identity.Role(e.By.Role)}
		s.sink.Record(audit.Event{Type: e.Type, Bot: s.bot, User: &by, Extra: extra})
	}
}

// setupPM builds a bot's project-management core: its own state file,
// drivers for agent sessions and people, a waker into the hub, and the
// workers / company setup from config.
func setupPM(cfg *config.Config, bc config.BotConfig, h *hub.Hub, workers *worker.Manager, scope worker.Scope, sink audit.Sink, withConsole bool) (*app.App, *agentsession.Driver, error) {
	store, err := jsonstore.Open(filepath.Join(bc.Dir, "state.json"))
	if err != nil {
		return nil, nil, err
	}
	a := app.New(store, RealClock{})
	a.Log = slog.Default().With("bot", bc.Name)
	a.Sink = auditSink{bot: bc.Name, sink: sink}
	agents := &agentsession.Driver{M: workers, App: a, Bot: bc.Name}
	a.RegisterDriver(string(workforce.AgentSession), agents)
	a.RegisterDriver(string(workforce.Human), &human.Driver{Clock: RealClock{}, Reachers: map[workforce.RouteKind]human.Reacher{
		human.FeishuDM: human.ReacherFunc(func(ctx context.Context, addr, text string, _ workforce.Urgency) (string, error) {
			return h.SendUser(ctx, "feishu", addr, text)
		}),
		human.OwnerChat: human.ReacherFunc(func(ctx context.Context, _, text string, _ workforce.Urgency) (string, error) {
			return "", h.NotifyOwner(ctx, text)
		}),
	}})
	a.Waker = app.WakerFunc(func(ctx context.Context, w app.Wake) {
		// One audience per wake-up: the owner side together (and the
		// scaffold's own signals), each other person on their own, so a turn
		// runs with the rights of whoever it is for.
		type group struct {
			from identity.User
			sigs []inbox.Signal
		}
		var groups []*group
		byKey := map[string]*group{}
		for _, x := range w.Signals {
			from, key := hub.MainUser, "owner"
			if r := identity.Role(x.Actor.Role); x.Actor.UserID != "" && r != identity.RoleOwner && r != identity.RoleAdmin && x.Source != inbox.Relay {
				from = identity.User{ID: x.Actor.UserID, Name: x.ActorName, Role: r}
				key = x.Actor.UserID
			}
			g := byKey[key]
			if g == nil {
				g = &group{from: from}
				byKey[key] = g
				groups = append(groups, g)
			}
			g.sigs = append(g.sigs, x)
		}
		for _, g := range groups {
			ids := make([]string, len(g.sigs))
			reply := w.ReplyConv
			for i, x := range g.sigs {
				ids[i] = x.ID
				if x.ReplyTo.Conv != "" {
					reply = x.ReplyTo.Conv
				}
			}
			h.WakeMain(app.FocusText(g.sigs), ids, reply, g.from)
		}
	})
	for k, v := range bc.Wake.Modes() {
		a.Policy.Modes[k] = v
	}
	if bc.Wake.BatchEvery.Duration > 0 {
		a.Policy.BatchEvery = bc.Wake.BatchEvery.Duration
	}
	// One continuous session: everyone's messages become signals for the
	// main session (unless visitors are isolated, then only the owner side's).
	h.SetIntake(func(in hub.Inbound, u identity.User, conv string) bool {
		if bc.IsolateVisitors && !u.Privileged() {
			return false
		}
		by := Actor{UserID: u.ID, Role: Role(u.Role), Via: "chat"}
		if u.Role == identity.RoleOwner {
			by.Worker = Owner
		} else if id, ok := a.WorkerFor(u.ID, u.UnionID, u.Email); ok {
			by.Worker = id
		}
		src, reason := inbox.DirectMessage, "dm"
		if in.IsGroup {
			src, reason = inbox.Mention, "mention"
		}
		_, _, err := a.Ingest(context.Background(), app.SignalSpec{Dedupe: "msg:" + in.Platform + ":" + firstNonEmpty(in.MessageID, fmt.Sprint(time.Now().UnixNano())),
			Source: src, Reason: reason, Actor: by, ActorName: u.Display(), Summary: oneLine(in.Text, 120), Body: in.Text,
			Refs: inbox.Refs{Worker: by.Worker}, ReplyTo: inbox.ReplyTo{Conv: conv, MessageID: in.MessageID}})
		if err != nil {
			slog.Error("pm: could not take the message in", "err", err)
			return false
		}
		return true
	}, func(ids []string, answered bool) { a.TurnDone(context.Background(), ids, answered) })

	ctx := context.Background()
	if err := a.EnsureOrg(ctx, firstNonEmpty(cfg.Org.Title, "公司")); err != nil {
		return nil, nil, err
	}
	setup := System("config")
	owners := append([]string(nil), bc.Owners...)
	if withConsole {
		owners = append(owners, console.OwnerID)
	}
	if err := a.UpsertWorker(ctx, setup, workforce.Worker{ID: Owner, Name: firstNonEmpty(bc.OwnerName, "owner"), Kind: workforce.Human,
		Trust: workforce.External, Physical: true, Consent: workforce.Consent{Given: true}, Identities: owners,
		Contact: []workforce.Route{{Kind: human.OwnerChat, Address: "owner"}}}); err != nil {
		return nil, nil, err
	}
	for _, w := range cfg.Workers {
		if scope.Names != nil && !scope.Names[w.Name] {
			continue
		}
		if err := a.UpsertWorker(ctx, setup, AgentWorker(w)); err != nil {
			return nil, nil, fmt.Errorf("worker %s: %w", w.Name, err)
		}
	}
	for _, p := range cfg.People {
		if len(p.Bots) > 0 && !contains(p.Bots, bc.Name) {
			continue
		}
		pw, err := PersonWorker(p)
		if err != nil {
			return nil, nil, err
		}
		if err := a.UpsertWorker(ctx, setup, pw); err != nil {
			return nil, nil, fmt.Errorf("person %s: %w", p.ID, err)
		}
	}
	if len(cfg.Org.Policies)+len(cfg.Org.Roles) > 0 {
		_, err := a.ChangeProject(ctx, setup, OrgProject, func(p *portfolio.Project, now time.Time) ([]Event, error) {
			var evs []Event
			for _, pl := range cfg.Org.Policies {
				e, err := p.SetPolicy(portfolio.ApprovalRule{Action: pl.Action, Scope: pl.Scope, Approvers: pl.Approvers, Quorum: pl.Quorum}, now, setup)
				if err != nil {
					return nil, err
				}
				evs = append(evs, e...)
			}
			for _, r := range cfg.Org.Roles {
				if len(p.Holding(r.Role, now)) > 0 && contains(workerIDs(p.Holding(r.Role, now)), r.Worker) {
					continue
				}
				e, err := p.AddMember(portfolio.Membership{Worker: WorkerID(r.Worker), Role: r.Role, Duties: r.Duties}, now, setup)
				if err != nil {
					return nil, err
				}
				evs = append(evs, e...)
			}
			return append(evs, NewEvent("org.synced", "project:org", now, setup)), nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("org: %w", err)
		}
	}
	return a, agents, nil
}

// AgentWorker turns a configured agent session into a worker profile.
func AgentWorker(w config.Worker) workforce.Worker {
	caps := w.Capabilities
	if len(caps) == 0 {
		access := "write"
		if w.Access == "readonly" {
			access = "read"
		}
		caps = []string{"dir:" + w.WorkDir + ":" + access}
	}
	out := workforce.Worker{ID: WorkerID(w.Name), Name: w.Name, Kind: workforce.AgentSession, Trust: workforce.Controlled,
		Description: w.Description, Skills: w.Skills, Interaction: workforce.Interaction{Sessions: true}}
	for _, c := range caps {
		out.Capability = append(out.Capability, workforce.ParseCapability(c))
	}
	return out
}

// PersonWorker turns a [[people]] entry into a worker profile.
func PersonWorker(p config.Person) (workforce.Worker, error) {
	w := workforce.Worker{ID: WorkerID(p.ID), Name: firstNonEmpty(p.Name, p.ID), Kind: workforce.Human, Trust: workforce.External,
		Description: p.Description, Skills: p.Skills, Physical: true, Identities: p.Identities,
		Consent:     workforce.Consent{Given: p.Consent, From: p.ConsentFrom},
		Interaction: workforce.Interaction{Sessions: true, CanAsk: true, CanRefuse: true, CanCounter: true},
		Norms: workforce.Norms{Timezone: p.Timezone, WorkHours: windows(p.WorkHours), QuietHours: windows(p.QuietHours),
			MaxNudgesPerDay: p.MaxNudgesPerDay, MinNudgeInterval: p.MinNudgeInterval.Duration}}
	for _, g := range p.Authority {
		w.Authority = append(w.Authority, workforce.Authority{Action: g.Action, Scope: g.Scope})
	}
	for _, c := range p.Capabilities {
		w.Capability = append(w.Capability, workforce.ParseCapability(c))
	}
	for _, c := range p.Contact {
		u := map[string]workforce.Urgency{"": workforce.Normal, "normal": workforce.Normal, "reminder": workforce.Reminder,
			"urgent": workforce.Urgent, "critical": workforce.Critical}[strings.ToLower(c.MinUrgency)]
		w.Contact = append(w.Contact, workforce.Route{Kind: workforce.RouteKind(c.Channel), Address: c.Address, MinUrgency: u, Approval: c.Approval})
	}
	return w, w.Validate()
}

func windows(ws []config.Window) []workforce.Window {
	var out []workforce.Window
	for _, w := range ws {
		x := workforce.Window{Start: w.Start, End: w.End}
		for _, d := range w.Days {
			x.Days = append(x.Days, time.Weekday(d))
		}
		out = append(out, x)
	}
	return out
}

func contains(v []string, x string) bool {
	for _, s := range v {
		if s == x {
			return true
		}
	}
	return false
}

func workerIDs(v []WorkerID) []string {
	out := make([]string, len(v))
	for i, x := range v {
		out[i] = string(x)
	}
	return out
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
