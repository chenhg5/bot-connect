package sim

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/bot-connect/internal/adapters/drivers/human"
	"github.com/chenhg5/bot-connect/internal/adapters/store/jsonstore"
	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/assemble"
	"github.com/chenhg5/bot-connect/internal/audit"
	"github.com/chenhg5/bot-connect/internal/brain"
	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
	"github.com/chenhg5/bot-connect/internal/hub"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/tools"
	"github.com/chenhg5/bot-connect/internal/toolserver"
	"github.com/chenhg5/bot-connect/internal/worker"
)

const (
	platformName = "eval"
	ownerUser    = "u-owner"
	ownerChat    = "dm-owner"
)

func userOf(person string) string { return "u-" + person }
func dmOf(person string) string   { return "dm-" + person }

// Msg is something the bot sent.
type Msg struct {
	Chat string // "dm-owner", "dm-wangwu", "user:u-wangwu", a group id
	Text string
	At   time.Time
}

// ToolCall is a tool the brain used.
type ToolCall struct {
	Tool   string
	Caller string
	Args   any
	Error  string
	At     time.Time
}

// platform is the simulated chat platform: it records what the bot sends
// and lets the harness inject messages.
type platform struct {
	mu   sync.Mutex
	on   func(hub.Inbound)
	msgs []Msg
	seq  int
}

func (p *platform) Name() string { return platformName }
func (p *platform) Start(ctx context.Context, on func(hub.Inbound)) error {
	p.on = on
	return nil
}
func (p *platform) Ack(context.Context, string) {}
func (p *platform) Send(_ context.Context, chat, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.msgs = append(p.msgs, Msg{Chat: chat, Text: text, At: time.Now()})
	return nil
}
func (p *platform) SendUser(_ context.Context, user, text string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.msgs = append(p.msgs, Msg{Chat: "user:" + user, Text: text, At: time.Now()})
	return fmt.Sprintf("m%d", len(p.msgs)), nil
}

func (p *platform) snapshot() []Msg {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Msg(nil), p.msgs...)
}

// recorder captures tool calls from the tool server's audit stream.
type recorder struct {
	mu    sync.Mutex
	calls []ToolCall
}

func (r *recorder) Record(e audit.Event) {
	if e.Type != audit.ToolCall {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	caller := ""
	if e.User != nil {
		caller = e.User.ID
	}
	r.calls = append(r.calls, ToolCall{Tool: e.Tool, Caller: caller, Args: e.Args, Error: e.Error, At: time.Now()})
}

func (r *recorder) snapshot() []ToolCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ToolCall(nil), r.calls...)
}

// fakeAgent accepts offers at once; the case decides when it delivers.
type fakeAgent struct{ app *app.App }

func (f *fakeAgent) Observe(context.Context, workforce.Worker, []delegation.Assignment) []workforce.Fact {
	return nil
}
func (f *fakeAgent) Offer(ctx context.Context, w workforce.Worker, a delegation.Assignment) error {
	return f.app.Report(ctx, System("driver:agent"), a.ID, app.Report{Action: "accept", Ref: "sim-" + string(a.ID)})
}
func (f *fakeAgent) Notify(context.Context, workforce.Worker, delegation.Assignment, app.Notice) error {
	return nil
}

// Harness is one simulated bot.
type Harness struct {
	Case  Case
	Clock *FakeClock
	App   *app.App
	Hub   *hub.Hub
	plat  *platform
	rec   *recorder
	srv   *toolserver.Server
	items map[string]ItemID // case refs → ids
	dir   string
	stop  context.CancelFunc
}

// New builds a bot from the config's brain (and providers) around a fresh
// simulated world.
func New(ctx context.Context, cfg *config.Config, c Case) (*Harness, error) {
	start, err := parseTime(c.Now, time.Now())
	if err != nil || start == nil {
		return nil, fmt.Errorf("case %s: now: %v", c.ID, err)
	}
	dir, err := os.MkdirTemp("", "bot-connect-eval-")
	if err != nil {
		return nil, err
	}
	h := &Harness{Case: c, Clock: NewFakeClock(*start), plat: &platform{}, rec: &recorder{}, items: map[string]ItemID{}, dir: dir}
	bc := cfg.Bots[0]
	bc.Name, bc.OwnerName, bc.Dir = "eval bot", "chicken", dir
	bc.Brain.WorkDir = dir + "/brain"
	ctx, h.stop = context.WithCancel(ctx)

	policy := identity.NewStaticPolicy([]string{ownerUser}, nil)
	wm, err := worker.NewManager(nil, nil, dir)
	if err != nil {
		return nil, err
	}
	h.Hub = hub.New(hub.Options{Identity: identity.NewResolver(policy), Policy: policy, Audit: audit.Nop{},
		MaxConcurrent: 2, Debounce: 300 * time.Millisecond, TurnTimeout: 4 * time.Minute, HistoryLimit: 30, DataDir: dir,
		Scope: worker.Scope{Bot: bc.Name}}, wm)
	h.App = app.New(jsonstore.Memory(), h.Clock)
	h.App.RegisterDriver(string(workforce.AgentSession), &fakeAgent{app: h.App})
	h.App.RegisterDriver(string(workforce.Human), &human.Driver{Clock: h.Clock, Reachers: map[workforce.RouteKind]human.Reacher{
		human.FeishuDM: human.ReacherFunc(func(ctx context.Context, addr, text string, _ workforce.Urgency) (string, error) {
			return h.plat.SendUser(ctx, addr, text)
		}),
		human.OwnerChat: human.ReacherFunc(func(ctx context.Context, _, text string, _ workforce.Urgency) (string, error) {
			return "", h.Hub.NotifyOwner(ctx, text)
		}),
	}})
	assemble.WirePM(h.App, h.Hub, bc)

	reg := tools.New(tools.Env{Bot: bc.Name, Workers: wm, Messenger: h.Hub, Scope: worker.Scope{Bot: bc.Name}, App: h.App})
	h.srv = toolserver.New(reg)
	h.srv.Audit = h.rec
	if err := h.srv.Start("127.0.0.1:0"); err != nil {
		return nil, err
	}
	b, err := brain.New(bc.Brain, h.Hub, wm, h.srv, reg.Names(), worker.Scope{Bot: bc.Name}, bc.Name, bc.OwnerName)
	if err != nil {
		return nil, err
	}
	b.SetPM(h.App)
	h.Hub.SetBrain(b)
	h.Hub.AddPlatform(h.plat)
	h.Hub.SetOwnerChat(platformName, ownerChat)
	if err := h.Hub.Start(ctx); err != nil {
		return nil, err
	}
	if err := h.setup(ctx); err != nil {
		h.Close()
		return nil, fmt.Errorf("case %s: setup: %w", c.ID, err)
	}
	return h, nil
}

// Close stops the bot and removes its files.
func (h *Harness) Close() {
	h.stop()
	sctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	h.srv.Stop(sctx)
	os.RemoveAll(h.dir)
}

func (h *Harness) setup(ctx context.Context) error {
	a, sys, now := h.App, System("setup"), h.Clock.Now()
	if err := a.EnsureOrg(ctx, "公司"); err != nil {
		return err
	}
	owner := workforce.Worker{ID: Owner, Name: "chicken", Kind: workforce.Human, Trust: workforce.External, Physical: true,
		Consent: workforce.Consent{Given: true}, Identities: []string{platformName + ":" + ownerUser},
		Contact: []workforce.Route{{Kind: human.OwnerChat, Address: "owner"}}}
	if err := a.UpsertWorker(ctx, sys, owner); err != nil {
		return err
	}
	for _, p := range h.Case.People {
		w := workforce.Worker{ID: WorkerID(p.ID), Name: p.Name, Kind: workforce.Human, Trust: workforce.External, Description: p.Description,
			Skills: p.Skills, Physical: p.Physical == nil || *p.Physical, Consent: workforce.Consent{Given: p.Consent},
			Identities: []string{platformName + ":" + userOf(p.ID)}, Contact: []workforce.Route{{Kind: human.FeishuDM, Address: userOf(p.ID)}}}
		if p.WorkHours != "" {
			s, e, _ := strings.Cut(p.WorkHours, "-")
			w.Norms.WorkHours = []workforce.Window{{Start: s, End: e}}
		}
		for _, act := range p.Authority {
			action, scope, _ := strings.Cut(act, "@")
			w.Authority = append(w.Authority, workforce.Authority{Action: action, Scope: scope})
		}
		if err := a.UpsertWorker(ctx, sys, w); err != nil {
			return err
		}
		if p.Away != "" {
			if err := a.NoteWorker(ctx, Actor{Worker: w.ID}, w.ID, workforce.Fact{Dim: workforce.Presence, Value: "away", Detail: p.Away}); err != nil {
				return err
			}
		}
	}
	for _, ag := range h.Case.Agents {
		w := workforce.Worker{ID: WorkerID(ag.ID), Name: firstNonEmpty(ag.Name, ag.ID), Kind: workforce.AgentSession, Trust: workforce.Controlled,
			Description: ag.Description, Interaction: workforce.Interaction{Sessions: true}}
		for _, c := range ag.Capabilities {
			w.Capability = append(w.Capability, workforce.ParseCapability(c))
		}
		if err := a.UpsertWorker(ctx, sys, w); err != nil {
			return err
		}
	}
	for _, p := range h.Case.Projects {
		id := ProjectID(p.ID)
		if id != OrgProject {
			pr, err := ParsePriority(p.Priority)
			if err != nil {
				return err
			}
			from, err := parseTime(p.From, now)
			if err != nil {
				return err
			}
			until, err := parseTime(p.Until, now)
			if err != nil {
				return err
			}
			if from == nil && until != nil {
				from = &now
			}
			if _, err := a.CreateProject(ctx, sys, app.ProjectSpec{ID: id, Title: p.Title, Priority: pr, Timebox: Period{From: from, Until: until},
				Objective: portfolio.Objective{Text: p.Objective}}); err != nil {
				return err
			}
		}
		for _, m := range p.Members {
			from, _ := parseTime(m.From, now)
			until, _ := parseTime(m.Until, now)
			if _, err := a.ChangeProject(ctx, sys, id, func(pj *portfolio.Project, at time.Time) ([]Event, error) {
				return pj.AddMember(portfolio.Membership{Worker: WorkerID(m.Worker), Role: m.Role, Duties: m.Duties, Period: Period{From: from, Until: until}}, at, sys)
			}); err != nil {
				return err
			}
		}
	}
	for _, pl := range h.Case.Policies {
		pid := ProjectID(firstNonEmpty(pl.Project, string(OrgProject)))
		if _, err := a.ChangeProject(ctx, sys, pid, func(pj *portfolio.Project, at time.Time) ([]Event, error) {
			return pj.SetPolicy(portfolio.ApprovalRule{Action: pl.Action, Scope: pl.Scope, Approvers: pl.Approvers}, at, sys)
		}); err != nil {
			return err
		}
	}
	for _, it := range h.Case.Items {
		due, err := parseTime(it.Due, now)
		if err != nil {
			return err
		}
		est, err := app.ParseEffort(it.Estimate)
		if err != nil {
			return err
		}
		sp := planning.Spec{Project: ProjectID(firstNonEmpty(it.Project, string(OrgProject))), Title: it.Title, Acceptance: it.Done, Due: due, Estimate: est}
		for _, n := range it.Needs {
			k, d, _ := strings.Cut(n, ":")
			sp.Needs = append(sp.Needs, planning.Need{Kind: NeedKind(k), Detail: d})
		}
		for _, d := range it.DependsOn {
			sp.DependsOn = append(sp.DependsOn, h.items[d])
		}
		x, err := a.PlanItem(ctx, Actor{UserID: ownerUser, Worker: Owner, Role: RoleOwner}, sp)
		if err != nil {
			return err
		}
		h.items[firstNonEmpty(it.Ref, it.Title)] = x.ID
		if it.Status == "done" {
			if err := a.CompleteItem(ctx, Actor{UserID: ownerUser, Role: RoleOwner}, x.ID); err != nil {
				return err
			}
		}
	}
	ownerActor := Actor{UserID: ownerUser, Worker: Owner, Role: RoleOwner, Via: "setup"}
	for _, as := range h.Case.Assignments {
		id, ok := h.items[as.Item]
		if !ok {
			return fmt.Errorf("assignment: no item ref %q", as.Item)
		}
		x, err := a.Delegate(ctx, ownerActor, app.DelegateSpec{Item: id, Worker: WorkerID(as.Worker), Kind: delegation.AskKind(as.Kind),
			Why: NeedKind(as.Why), Options: as.Options, Brief: delegation.Brief{Goal: as.Goal}, Conv: platformName + ":" + ownerChat})
		if err != nil {
			return err
		}
		worker := Actor{UserID: userOf(as.Worker), Worker: WorkerID(as.Worker), Role: RoleVisitor}
		if x.Status == delegation.Offered && (as.Status == "accepted" || as.Status == "delivered") {
			if err := a.Report(ctx, worker, x.ID, app.Report{Action: "accept"}); err != nil {
				return err
			}
		}
		if as.Status == "delivered" {
			by := worker
			if a.WorkerKind(x.Worker).Agentic() {
				by = System("driver:agent")
			}
			if err := a.Report(ctx, by, x.ID, app.Report{Action: "deliver", Result: as.Result}); err != nil {
				return err
			}
		}
	}
	for _, it := range h.Case.Items {
		if it.Started != "" {
			// backdate the start of work, for pace and forecast rules
			t, err := parseTime(it.Started, now)
			if err != nil {
				return err
			}
			a.Backdate(h.items[firstNonEmpty(it.Ref, it.Title)], *t)
		}
	}
	// Setup noise (offers, deliveries) is not part of the case.
	h.waitIdle(30 * time.Second)
	a.ClearInbox()
	return nil
}

func firstNonEmpty(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}

// waitIdle waits until the bot has nothing running or queued and nothing
// new was sent for a moment.
func (h *Harness) waitIdle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	quietSince, last := time.Now(), len(h.plat.snapshot())+len(h.rec.snapshot())
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		n := len(h.plat.snapshot()) + len(h.rec.snapshot())
		if n != last || !h.Hub.Idle() {
			quietSince, last = time.Now(), n
			continue
		}
		if time.Since(quietSince) > 1500*time.Millisecond {
			return true
		}
	}
	return false
}

func hubInbound(chat, id, user, name, text string, group bool) hub.Inbound {
	return hub.Inbound{Platform: platformName, ChatID: chat, MessageID: id, SenderID: user, Sender: name, Text: text, IsGroup: group}
}
