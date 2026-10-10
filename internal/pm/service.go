package pm

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/bot-connect/internal/audit"
	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/plan"
	"github.com/chenhg5/bot-connect/internal/reach"
	"github.com/chenhg5/bot-connect/internal/worker"
	"github.com/chenhg5/bot-connect/internal/workforce"
)

// BotIO is how the service talks through one bot.
type BotIO struct {
	Name      string
	OwnerName string
	// Post wakes the brain in a conversation with a project event.
	Post func(convKey, text string, about identity.User) bool
	// NotifyOwner messages the owner's private chat directly (escalations).
	NotifyOwner func(ctx context.Context, text string) error
	// SendUser messages a user directly on a platform.
	SendUser func(ctx context.Context, platform, userID, text string) (string, error)
	// OwnerConv is the owner's private conversation, for events with no other home.
	OwnerConv func() string
}

type Service struct {
	mu     sync.Mutex
	path   string
	d      data
	now    func() time.Time
	agents *worker.Manager
	people []config.Person
	bots   map[string]*BotIO

	adMu     sync.Mutex
	adapters map[string]*worker.AgentWorker

	Audit audit.Sink
}

// Open loads (or starts) the project state in dataDir.
func Open(dataDir string, agents *worker.Manager, people []config.Person) (*Service, error) {
	s := &Service{path: filepath.Join(dataDir, "pm.json"), now: time.Now, agents: agents, people: people,
		bots: map[string]*BotIO{}, adapters: map[string]*worker.AgentWorker{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// AddBot registers a bot's I/O.
func (s *Service) AddBot(io *BotIO) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bots[io.Name] = io
}

func (s *Service) botName(bot string) string {
	if b := s.bots[bot]; b != nil && b.Name != "" {
		return b.Name
	}
	return bot
}

func (s *Service) ownerName(bot string) string {
	if b := s.bots[bot]; b != nil && b.OwnerName != "" {
		return b.OwnerName
	}
	return "主人"
}

func (s *Service) agentAdapter(bot, name string) *worker.AgentWorker {
	s.adMu.Lock()
	defer s.adMu.Unlock()
	k := bot + "/" + name
	if a := s.adapters[k]; a != nil {
		return a
	}
	a := &worker.AgentWorker{M: s.agents, Name: name, Bot: bot, Events: s, Lookup: s.assignmentByRef}
	s.adapters[k] = a
	return a
}

func (s *Service) assignmentByRef(ref string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, a := range s.d.Assignments {
		if a.Ref == ref && !a.Status.Terminal() {
			return id
		}
	}
	return ""
}

// TaskFinished routes a finished agent task to its assignment. It reports
// whether the task belonged to one (then the plain task report is not needed).
func (s *Service) TaskFinished(t worker.Task) bool {
	if t.Bot == "" {
		return false
	}
	return s.agentAdapter(t.Bot, t.Worker).Finished(t)
}

// reachPerson delivers a message to a person through their routes.
func (s *Service) reachPerson(ctx context.Context, bot string, p *person, m reach.Message) (reach.Receipt, error) {
	routes, err := reach.Plan(p.cfg.Contact, m, p.hours(), s.now())
	if err != nil {
		return reach.Receipt{}, err
	}
	io := s.bots[bot]
	if io == nil {
		return reach.Receipt{}, fmt.Errorf("bot %s is not running", bot)
	}
	d := reach.NewDispatcher(ioReachers(io)...)
	rc, err := d.Send(ctx, routes, m, nil) // approval-gated routes are skipped until approvals exist
	if s.Audit != nil {
		e := audit.Event{Type: "reach", Bot: bot, Worker: p.cfg.ID, Text: audit.Clip(m.Text, 1000), Status: string(rc.Channel), Extra: map[string]any{"urgency": m.Urgency.String(), "ref": m.Ref}}
		if err != nil {
			e.Error = err.Error()
		}
		s.Audit.Record(e)
	}
	return rc, err
}

// Event is the Events implementation: workers and people report here.
func (s *Service) Emit(id string, ev workforce.Event) error {
	s.mu.Lock()
	a := s.d.Assignments[id]
	if a == nil {
		s.mu.Unlock()
		return fmt.Errorf("no assignment %q", id)
	}
	if ev.At.IsZero() {
		ev.At = s.now()
	}
	if err := a.Apply(ev); err != nil {
		s.mu.Unlock()
		return err
	}
	t := s.d.Tasks[a.TaskID]
	if t != nil {
		syncTask(t, a, ev)
	}
	s.saveLocked()
	cp := *a
	var task plan.Task
	if t != nil {
		task = *t
	}
	io := s.bots[a.Bot]
	s.mu.Unlock()

	s.record(cp, ev)
	if text := s.reportText(cp, task, ev); text != "" && io != nil && io.Post != nil {
		conv := cp.ConvKey
		if conv == "" && io.OwnerConv != nil {
			conv = io.OwnerConv()
		}
		if !io.Post(conv, text, cp.Requester) {
			slog.Warn("pm: event for unknown conversation", "assignment", cp.ID, "conv", conv)
		}
	}
	return nil
}

func syncTask(t *plan.Task, a *workforce.Assignment, ev workforce.Event) {
	if t.Assignment != a.ID {
		return
	}
	t.UpdatedAt = ev.At
	switch ev.Type {
	case workforce.EvAccept, workforce.EvStart, workforce.EvProgress, workforce.EvAnswer, workforce.EvResume, workforce.EvRevise:
		t.Status, t.LastSignal, t.Blocked = plan.TaskActive, ev.At, ""
		if t.StartedAt.IsZero() {
			t.StartedAt = ev.At
		}
	case workforce.EvAsk, workforce.EvBlock:
		t.Status, t.Blocked, t.BlockedAt, t.LastSignal = plan.TaskBlocked, firstNonEmpty(ev.Note, "等待回复"), ev.At, ev.At
	case workforce.EvDeliver:
		t.Status, t.LastSignal, t.Blocked = plan.TaskActive, ev.At, ""
	case workforce.EvVerify:
		t.Status = plan.TaskDone
	case workforce.EvCounter:
		t.LastSignal = ev.At
	case workforce.EvDecline, workforce.EvExpire, workforce.EvRelease, workforce.EvFail, workforce.EvCancel:
		t.Status, t.Assignee, t.Assignment, t.Blocked = plan.TaskTodo, "", "", ""
	case workforce.EvRedate:
		t.Due = ev.Due
	}
}

// reportText is what the brain is told in the requester's conversation.
func (s *Service) reportText(a workforce.Assignment, t plan.Task, ev workforce.Event) string {
	title := firstNonEmpty(t.Title, oneLine(a.Brief.Goal, 60))
	head := fmt.Sprintf("委托 %s（任务 %s「%s」，执行者 %s）", a.ID, orDash(a.TaskID), title, a.Worker)
	switch ev.Type {
	case workforce.EvAccept:
		if strings.HasPrefix(ev.Note, "queued as ") { // agents accept by queueing; no news
			return ""
		}
		return head + " 已接下。" + noteText(ev.Note)
	case workforce.EvDecline:
		return head + " 被拒绝了：" + firstNonEmpty(ev.Note, "（没说原因）") + "\n任务回到待分派。请决定：换人、调整要求，或告诉需求方。"
	case workforce.EvCounter:
		return fmt.Sprintf("%s 对方还价：%s（提议截止 %s）。\n请决定是否接受（assignment_update action=accept_counter），或调整 / 换人。", head, firstNonEmpty(ev.Note, "-"), dueText(ev.Due, time.Local))
	case workforce.EvAsk:
		return head + " 执行者有问题：\n" + ev.Note + "\n能答就用 assignment_update action=answer 回复；需要需求方决定就去问。"
	case workforce.EvDeliver:
		out := head + " 已交付，等待验收。"
		if ev.Result != "" {
			out += "\n结果：\n" + clip(ev.Result, 3000)
		}
		if len(ev.Evidence) > 0 {
			out += "\n证据：" + strings.Join(ev.Evidence, "；")
		}
		if t.Done != "" {
			out += "\n完成标准：" + t.Done
		}
		return out + "\n请按完成标准验收（assignment_update action=verify / revise），并把结果告诉需求方。"
	case workforce.EvFail:
		return head + " 失败：" + clip(firstNonEmpty(ev.Note, "（无错误信息）"), 800) + "\n任务回到待分派。请判断：重试、换人，还是告诉需求方。"
	case workforce.EvRelease:
		return head + " 被执行者退回：" + firstNonEmpty(ev.Note, "-") + "\n任务回到待分派。"
	case workforce.EvExpire:
		return head + " 一直没有回应，已过期。任务回到待分派，请换人或联系需求方。"
	case workforce.EvProgress:
		if a.Worker == OwnerID || ev.Note == "" {
			return ""
		}
		return head + " 进展：" + ev.Note + etaText(ev.ETA)
	}
	return ""
}

func noteText(n string) string {
	if n == "" {
		return ""
	}
	return "备注：" + n
}

func etaText(t *time.Time) string {
	if t == nil {
		return ""
	}
	return "（预计 " + t.Format("01-02 15:04") + "）"
}

func (s *Service) record(a workforce.Assignment, ev workforce.Event) {
	if s.Audit == nil {
		return
	}
	u := ev.By
	if u.ID == "" {
		u = a.Requester
	}
	s.Audit.Record(audit.Event{Type: "assignment", Bot: a.Bot, Conv: a.ConvKey, User: &u, Worker: a.Worker, TaskID: a.ID,
		Status: string(ev.Type), Text: audit.Clip(firstNonEmpty(ev.Note, ev.Result), 1000), Extra: map[string]any{"task": a.TaskID, "status": string(a.Status)}})
}

// ---- tasks & goals ----

// TaskSpec is what the brain fills in.
type TaskSpec struct {
	Title, Done, GoalID, ParentID string
	Due                           *time.Time
	Estimate                      time.Duration
	Priority                      int
	DependsOn                     []string
}

func (s *Service) CreateTask(bot, conv string, by identity.User, sp TaskSpec) (plan.Task, error) {
	if strings.TrimSpace(sp.Title) == "" {
		return plan.Task{}, fmt.Errorf("title is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sp.GoalID != "" && s.d.Goals[sp.GoalID] == nil {
		return plan.Task{}, fmt.Errorf("no goal %q", sp.GoalID)
	}
	for _, d := range sp.DependsOn {
		if s.d.Tasks[d] == nil {
			return plan.Task{}, fmt.Errorf("no task %q to depend on", d)
		}
	}
	now := s.now()
	t := &plan.Task{ID: s.nextID("T"), Bot: bot, GoalID: sp.GoalID, ParentID: sp.ParentID, Title: sp.Title, Done: sp.Done,
		Due: sp.Due, Estimate: sp.Estimate, Priority: sp.Priority, DependsOn: sp.DependsOn, Status: plan.TaskTodo,
		Owner: by, ConvKey: conv, CreatedAt: now, UpdatedAt: now}
	s.d.Tasks[t.ID] = t
	s.saveLocked()
	return *t, nil
}

// UpdateTask changes fields that are set (nil / zero = unchanged).
func (s *Service) UpdateTask(bot, id string, title, done *string, due *time.Time, estimate *time.Duration, priority *int, status string) (plan.Task, error) {
	s.mu.Lock()
	t := s.d.Tasks[id]
	if t == nil || t.Bot != bot {
		s.mu.Unlock()
		return plan.Task{}, fmt.Errorf("no task %q", id)
	}
	if title != nil {
		t.Title = *title
	}
	if done != nil {
		t.Done = *done
	}
	if estimate != nil {
		t.Estimate = *estimate
	}
	if priority != nil {
		t.Priority = *priority
	}
	switch status {
	case "":
	case string(plan.TaskDone), string(plan.TaskDropped):
		t.Status = plan.TaskStatus(status)
	default:
		s.mu.Unlock()
		return plan.Task{}, fmt.Errorf("status can only be set to done or dropped (the rest follows the assignment)")
	}
	aid := t.Assignment
	redate := due != nil && (t.Due == nil || !t.Due.Equal(*due))
	if due != nil {
		t.Due = due
	}
	t.UpdatedAt = s.now()
	s.saveLocked()
	cp := *t
	s.mu.Unlock()
	if redate && aid != "" {
		_ = s.UpdateAssignment(bot, aid, workforce.Event{Type: workforce.EvRedate, Due: due}, identity.User{})
	}
	if (status == string(plan.TaskDropped)) && aid != "" {
		_ = s.UpdateAssignment(bot, aid, workforce.Event{Type: workforce.EvCancel, Note: "任务已取消"}, identity.User{})
	}
	return cp, nil
}

// Tasks lists a bot's tasks (open first, by due date).
func (s *Service) Tasks(bot string, includeClosed bool) []plan.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []plan.Task
	for _, t := range s.d.Tasks {
		if t.Bot == bot && (includeClosed || t.Open()) {
			out = append(out, *t)
		}
	}
	sortTasks(out)
	return out
}

func sortTasks(ts []plan.Task) {
	sort.Slice(ts, func(i, j int) bool {
		a, b := ts[i], ts[j]
		if a.Open() != b.Open() {
			return a.Open()
		}
		if (a.Due == nil) != (b.Due == nil) {
			return a.Due != nil
		}
		if a.Due != nil && !a.Due.Equal(*b.Due) {
			return a.Due.Before(*b.Due)
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
}

func (s *Service) Task(bot, id string) (plan.Task, []workforce.Assignment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.d.Tasks[id]
	if t == nil || t.Bot != bot {
		return plan.Task{}, nil, false
	}
	var as []workforce.Assignment
	for _, a := range s.d.Assignments {
		if a.TaskID == id {
			as = append(as, *a)
		}
	}
	sort.Slice(as, func(i, j int) bool { return as[i].CreatedAt.Before(as[j].CreatedAt) })
	return *t, as, true
}

func (s *Service) CreateGoal(bot, conv string, by identity.User, g plan.Goal) (plan.Goal, error) {
	if strings.TrimSpace(g.Title) == "" {
		return plan.Goal{}, fmt.Errorf("title is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	g.ID, g.Bot, g.Owner, g.ConvKey, g.Status, g.CreatedAt, g.UpdatedAt = s.nextID("G"), bot, by, conv, plan.GoalActive, now, now
	s.d.Goals[g.ID] = &g
	s.saveLocked()
	return g, nil
}

func (s *Service) UpdateGoal(bot, id string, f func(*plan.Goal) error) (plan.Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.d.Goals[id]
	if g == nil || g.Bot != bot {
		return plan.Goal{}, fmt.Errorf("no goal %q", id)
	}
	if err := f(g); err != nil {
		return plan.Goal{}, err
	}
	g.UpdatedAt = s.now()
	s.saveLocked()
	return *g, nil
}

func (s *Service) Goals(bot string) []plan.Goal {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []plan.Goal
	for _, g := range s.d.Goals {
		if g.Bot == bot {
			out = append(out, *g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// ---- assignments ----

// Assign hands a task to a worker. Checks: the worker exists for this bot,
// is reachable, and (people) agreed to take work from this requester.
func (s *Service) Assign(ctx context.Context, bot, taskID, workerID string, by identity.User, scope worker.Scope, brief workforce.Brief) (workforce.Assignment, error) {
	if workerID == "me" {
		workerID = OwnerID
	}
	w, err := s.workerFor(bot, workerID)
	if err != nil {
		return workforce.Assignment{}, err
	}
	prof := w.Profile()
	switch prof.Kind {
	case workforce.KindHuman:
		p := w.(*person)
		if p.cfg.ID != OwnerID && !p.cfg.Consent {
			return workforce.Assignment{}, fmt.Errorf("permission denied: %s has not agreed to take work from the bot (consent = false); reach them to ask instead", p.cfg.Name)
		}
		if !acceptsFrom(prof.Norms.AcceptFrom, by) {
			return workforce.Assignment{}, fmt.Errorf("permission denied: %s only takes work from %s", p.cfg.Name, strings.Join(prof.Norms.AcceptFrom, ", "))
		}
	case workforce.KindAgentSession:
		if !s.agents.Access(workerID, scope).Delegate {
			return workforce.Assignment{}, fmt.Errorf("no worker named %q you can hand work to", workerID)
		}
	}
	var st workforce.State
	for _, f := range w.Observe(ctx) {
		st.Put(f)
	}
	if u, why := st.Unreachable(s.now()); u {
		return workforce.Assignment{}, fmt.Errorf("%s can't take work now: %s", workerID, why)
	}

	s.mu.Lock()
	t := s.d.Tasks[taskID]
	if t == nil || t.Bot != bot {
		s.mu.Unlock()
		return workforce.Assignment{}, fmt.Errorf("no task %q", taskID)
	}
	if !t.Open() {
		s.mu.Unlock()
		return workforce.Assignment{}, fmt.Errorf("task %s is %s", taskID, t.Status)
	}
	if t.Assignment != "" {
		if prev := s.d.Assignments[t.Assignment]; prev != nil && !prev.Status.Terminal() {
			s.mu.Unlock()
			return workforce.Assignment{}, fmt.Errorf("task %s is already with %s (%s, %s); cancel that first", taskID, prev.Worker, prev.ID, prev.Status)
		}
	}
	if brief.Goal == "" {
		brief.Goal = t.Title
	}
	if brief.Done == "" {
		brief.Done = t.Done
	}
	if brief.Due == nil {
		brief.Due = t.Due
	}
	if brief.Estimate == 0 {
		brief.Estimate = t.Estimate
	}
	if brief.Priority == 0 {
		brief.Priority = t.Priority
	}
	a := &workforce.Assignment{ID: s.nextID("A"), TaskID: taskID, Worker: workerID, Bot: bot, ConvKey: t.ConvKey, Requester: by, Brief: brief}
	_ = a.Apply(workforce.Event{Type: workforce.EvOffer, At: s.now(), By: by})
	s.d.Assignments[a.ID] = a
	t.Assignee, t.Assignment, t.UpdatedAt = workerID, a.ID, s.now()
	s.saveLocked()
	cp := *a
	s.mu.Unlock()
	s.record(cp, workforce.Event{Type: workforce.EvOffer, By: by, Note: brief.Goal})

	if err := w.Offer(ctx, cp); err != nil {
		_ = s.Emit(cp.ID, workforce.Event{Type: workforce.EvFail, Note: "could not deliver the offer: " + err.Error()})
		return cp, fmt.Errorf("could not hand it to %s: %w", workerID, err)
	}
	a2, _ := s.Assignment(bot, cp.ID)
	return a2, nil
}

func acceptsFrom(list []string, u identity.User) bool {
	for _, x := range list {
		if x == string(u.Role) || x == u.ID || (x == "admin" && u.Role == identity.RoleOwner) {
			return true
		}
	}
	return false
}

func (s *Service) Assignment(bot, id string) (workforce.Assignment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.d.Assignments[id]
	if a == nil || a.Bot != bot {
		return workforce.Assignment{}, false
	}
	return *a, true
}

// UpdateAssignment is the requester side: verify, revise, cancel, answer,
// redate, accept_counter. The worker is told.
func (s *Service) UpdateAssignment(bot, id string, ev workforce.Event, by identity.User) error {
	a, ok := s.Assignment(bot, id)
	if !ok {
		return fmt.Errorf("no assignment %q", id)
	}
	ev.By = by
	if ev.Type == workforce.EvAccept && a.Status != workforce.Countered {
		return fmt.Errorf("assignment %s has no counter-proposal to accept", id)
	}
	if err := s.Emit(id, ev); err != nil {
		return err
	}
	if ev.Type == workforce.EvAccept { // accepted the counter: the worker goes ahead
		_ = s.Emit(id, workforce.Event{Type: workforce.EvStart})
		s.mu.Lock()
		if t := s.d.Tasks[a.TaskID]; t != nil && a.Brief.Due != nil {
			due := *a.Brief.Due
			if cur := s.d.Assignments[id]; cur != nil && cur.Brief.Due != nil {
				due = *cur.Brief.Due
			}
			t.Due = &due
			s.saveLocked()
		}
		s.mu.Unlock()
	}
	w, err := s.workerFor(bot, a.Worker)
	if err != nil {
		return nil
	}
	cur, _ := s.Assignment(bot, id)
	if err := w.Notify(context.Background(), cur, ev); err != nil {
		slog.Info("pm: worker not notified", "assignment", id, "event", ev.Type, "err", err)
	}
	return nil
}

// Respond is the worker side, for people answering in chat: accept, decline,
// counter, progress, ask, deliver, release. Only the assignee may.
func (s *Service) Respond(bot, id string, ev workforce.Event, caller identity.User) error {
	a, ok := s.Assignment(bot, id)
	if !ok {
		return fmt.Errorf("no assignment %q", id)
	}
	p := s.personFor(bot, a.Worker)
	if p == nil || !p.matches(caller) {
		return fmt.Errorf("permission denied: %s is not assigned to you", id)
	}
	ev.By = caller
	if ev.Type == workforce.EvDeliver && a.Status == workforce.Accepted {
		_ = s.Emit(id, workforce.Event{Type: workforce.EvStart, By: caller})
	}
	return s.Emit(id, ev)
}

// AssignmentsOf lists open assignments of a worker (for a person's context).
func (s *Service) AssignmentsOf(bot, workerID string) []workforce.Assignment {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []workforce.Assignment
	for _, a := range s.d.Assignments {
		if a.Bot == bot && a.Worker == workerID && !a.Status.Terminal() {
			out = append(out, *a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *Service) loadOf(bot, workerID string, since time.Time) (open, declined int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.d.Assignments {
		if a.Bot != bot || a.Worker != workerID {
			continue
		}
		if !a.Status.Terminal() {
			open++
		}
		if a.Status == workforce.Declined && a.UpdatedAt.After(since) {
			declined++
		}
	}
	return
}

// ---- roles ----

func (s *Service) SetRole(bot string, r plan.Role) (plan.Role, error) {
	if r.Worker == "" || r.Title == "" {
		return plan.Role{}, fmt.Errorf("worker and title are required")
	}
	if r.Worker == "me" {
		r.Worker = OwnerID
	}
	if _, err := s.workerFor(bot, r.Worker); err != nil {
		return plan.Role{}, err
	}
	if r.Scope == "" {
		r.Scope = "org"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Scope != "org" {
		if g := s.d.Goals[r.Scope]; g == nil || g.Bot != bot {
			return plan.Role{}, fmt.Errorf("scope must be \"org\" or a goal id; no goal %q", r.Scope)
		}
	}
	if r.ID == "" {
		r.ID = s.nextID("RO")
	}
	r.Bot = bot
	s.d.Roles[r.ID] = &r
	s.saveLocked()
	return r, nil
}

func (s *Service) EndRole(bot, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.d.Roles[id]
	if r == nil || r.Bot != bot {
		return fmt.Errorf("no role %q", id)
	}
	now := s.now()
	r.Until = &now
	s.saveLocked()
	return nil
}

// Roles lists roles active at now (all = include ended / future ones).
func (s *Service) Roles(bot string, all bool) []plan.Role {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var out []plan.Role
	for _, r := range s.d.Roles {
		if r.Bot == bot && (all || r.Active(now)) {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ---- risks ----

func (s *Service) RaiseRisk(bot string, r plan.Risk) (plan.Risk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Tasks[r.Subject] == nil && s.d.Goals[r.Subject] == nil {
		return plan.Risk{}, fmt.Errorf("no task or goal %q", r.Subject)
	}
	r.ID, r.Bot, r.Status, r.At = s.nextID("R"), bot, plan.RiskOpen, s.now()
	s.d.Risks[r.ID] = &r
	s.saveLocked()
	return r, nil
}

func (s *Service) UpdateRisk(bot, id string, status plan.RiskStatus, note string) (plan.Risk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.d.Risks[id]
	if r == nil || r.Bot != bot {
		return plan.Risk{}, fmt.Errorf("no risk %q", id)
	}
	switch status {
	case plan.RiskOpen, plan.RiskAccepted, plan.RiskMitigated, plan.RiskClosed:
	default:
		return plan.Risk{}, fmt.Errorf("status must be open, accepted, mitigated or closed")
	}
	r.Status = status
	if note != "" {
		r.Summary += "（" + string(status) + "：" + note + "）"
	}
	s.saveLocked()
	return *r, nil
}

func (s *Service) Risks(bot string, all bool) []plan.Risk {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []plan.Risk
	for _, r := range s.d.Risks {
		if r.Bot == bot && (all || r.Status == plan.RiskOpen) {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level > out[j].Level
		}
		return out[i].At.Before(out[j].At)
	})
	return out
}

// Note records a fact about a worker (declared by them, or the brain's
// inference — which expires within a day).
func (s *Service) Note(bot, workerID string, f workforce.Fact) error {
	if _, err := s.workerFor(bot, workerID); err != nil {
		return err
	}
	if f.Source != workforce.Declared {
		f.Source = workforce.Inferred
	}
	f.At = s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.d.Facts[workerID]
	if st == nil {
		st = &workforce.State{}
		s.d.Facts[workerID] = st
	}
	st.Prune(f.At)
	st.Put(f)
	s.saveLocked()
	return nil
}

// State merges a worker's observed facts with stored declared / inferred ones.
func (s *Service) State(ctx context.Context, bot, workerID string) (workforce.Profile, workforce.State, error) {
	w, err := s.workerFor(bot, workerID)
	if err != nil {
		return workforce.Profile{}, workforce.State{}, err
	}
	var st workforce.State
	for _, f := range w.Observe(ctx) {
		st.Put(f)
	}
	s.mu.Lock()
	if saved := s.d.Facts[workerID]; saved != nil {
		for _, f := range saved.Facts {
			st.Put(f)
		}
	}
	s.mu.Unlock()
	st.Prune(s.now())
	return w.Profile(), st, nil
}

func firstNonEmpty(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}

func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func clip(x string, n int) string {
	r := []rune(x)
	if len(r) <= n {
		return x
	}
	return string(r[:n]) + "…"
}
