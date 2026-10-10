// Package worker owns the coding-agent workers: one agent session per worker,
// one task running per worker at a time, a FIFO queue for the rest.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/bot-connect/internal/audit"
	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/session"
)

const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"

	historyKeep = 10
)

// Requester is who asked for a task: a resolved user with a role.
type Requester = identity.User

type Task struct {
	ID          string    `json:"id"`
	Bot         string    `json:"bot,omitempty"` // bot that requested it
	Worker      string    `json:"worker"`
	Instruction string    `json:"instruction"`
	ConvKey     string    `json:"conv_key"` // conversation to report back to
	Requester   Requester `json:"requester"`
	Status      string    `json:"status"`
	Result      string    `json:"result,omitempty"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	EndedAt     time.Time `json:"ended_at,omitempty"`
	Note        string    `json:"note,omitempty"` // e.g. waiting for a locally used session
}

type workerState struct {
	Spec      config.Worker `json:"spec"`
	Dynamic   bool          `json:"dynamic"`
	Base      string        `json:"base,omitempty"`    // configured worker this derives from (instances, sub-workers)
	UserID    string        `json:"user_id,omitempty"` // owner of a per-user instance
	Parent    string        `json:"parent,omitempty"`  // sub-worker continuing another session of this worker
	Owned     []string      `json:"owned,omitempty"`   // sessions bot-connect created for this worker
	SessionID string        `json:"session_id"`
	History   []*Task       `json:"history"`
	LastRun   time.Time     `json:"last_run,omitempty"` // when we last finished writing to the session
	// Workers created from a template at run time:
	Template  string         `json:"template,omitempty"`
	Bot       string         `json:"bot,omitempty"` // bot that created it
	CreatedBy *identity.User `json:"created_by,omitempty"`
	CreatedAt time.Time      `json:"created_at,omitempty"`
	Branch    string         `json:"branch,omitempty"`   // worktree branch
	RepoDir   string         `json:"repo_dir,omitempty"` // repository the worktree belongs to

	runner  Runner
	queue   []*Task
	current *Task
	cancel  context.CancelFunc
	wake    chan struct{}
	stop    chan struct{} // closed when the worker is retired
}

type Manager struct {
	mu        sync.Mutex
	workers   map[string]*workerState
	templates map[string]config.Template
	tasks     map[string]*Task
	seq       int
	statePath string
	dataDir   string
	ctx       context.Context

	// OnFinish is called (outside the lock) when a task reaches a terminal state.
	OnFinish func(Task)
	// Audit receives task lifecycle events.
	Audit audit.Sink
	// Sessions reads agent transcripts (for read_worker and local-use checks).
	Sessions *session.Catalog
	// BusyWindow: a session written by someone else within this window is
	// considered in use locally, and tasks for it wait. 0 disables the check.
	BusyWindow time.Duration
}

func (m *Manager) record(t Task, extra string) {
	if m.Audit == nil {
		return
	}
	u := t.Requester
	e := audit.Event{Type: audit.Task, Bot: t.Bot, Conv: t.ConvKey, User: &u, TaskID: t.ID, Worker: t.Worker, Status: t.Status, Error: audit.Clip(t.Error, 500)}
	switch t.Status {
	case StatusQueued:
		e.Text = audit.Clip(t.Instruction, 2000)
	case StatusSucceeded, StatusFailed, StatusCancelled:
		if !t.StartedAt.IsZero() {
			e.Duration = t.EndedAt.Sub(t.StartedAt).Round(time.Second).String()
		}
		e.Text = audit.Clip(t.Result, 2000)
	}
	if extra != "" {
		e.Extra = map[string]any{"note": extra}
	}
	m.Audit.Record(e)
}

type persisted struct {
	Seq     int                     `json:"seq"`
	Workers map[string]*workerState `json:"workers"`
}

func NewManager(specs []config.Worker, templates []config.Template, dataDir string) (*Manager, error) {
	m := &Manager{workers: map[string]*workerState{}, tasks: map[string]*Task{}, templates: map[string]config.Template{},
		statePath: filepath.Join(dataDir, "workers.json"), dataDir: dataDir}
	for _, t := range templates {
		m.templates[t.Name] = t
	}
	var saved persisted
	if b, err := os.ReadFile(m.statePath); err == nil {
		_ = json.Unmarshal(b, &saved)
	}
	m.seq = saved.Seq
	for _, s := range specs {
		if err := m.addLocked(s, false); err != nil {
			return nil, err
		}
	}
	for name, ws := range saved.Workers {
		if ws.Dynamic && m.workers[name] == nil {
			// Env (provider credentials) is never persisted: take it from
			// where the worker came from.
			if t, ok := m.templates[ws.Template]; ok && ws.Template != "" {
				ws.Spec.Env = t.Env
			} else if b := m.workers[ws.Base]; b != nil && ws.Base != "" {
				ws.Spec.Env = b.Spec.Env
			} else if ws.Template != "" {
				slog.Warn("drop worker of removed template", "name", name, "template", ws.Template)
				continue
			}
			if err := m.addLocked(ws.Spec, true); err != nil {
				slog.Warn("drop saved worker", "name", name, "err", err)
				continue
			}
		}
		if w := m.workers[name]; w != nil {
			if w.SessionID == "" {
				w.SessionID = ws.SessionID
			}
			w.Parent, w.Base, w.UserID, w.Owned, w.LastRun = ws.Parent, ws.Base, ws.UserID, ws.Owned, ws.LastRun
			w.Template, w.Bot, w.CreatedBy, w.CreatedAt, w.Branch, w.RepoDir = ws.Template, ws.Bot, ws.CreatedBy, ws.CreatedAt, ws.Branch, ws.RepoDir
			w.History = ws.History
			for _, t := range ws.History {
				m.tasks[t.ID] = t
			}
		}
	}
	return m, nil
}

// runtimeDenyLocked extends a worker's deny_read with bot-connect's own
// state (audit log, conversations, isolated agent homes) and every other
// member's private workspace, so no worker can read those — while each
// keeps access to its own directory, wherever it lives.
func (m *Manager) runtimeDenyLocked(w *workerState) *[]string {
	if w.Spec.DenyRead == nil || w.Spec.Access == "full" {
		return w.Spec.DenyRead
	}
	deny := append([]string(nil), *w.Spec.DenyRead...)
	for _, p := range []string{"audit", "bots", "codex-home", "brain-codex-home", "brain", "workers.json", "conversations.json", "bot-connect.log"} {
		deny = append(deny, filepath.Join(m.dataDir, p))
	}
	for _, o := range m.workers {
		if o.UserID != "" && o.UserID != w.UserID {
			deny = append(deny, o.Spec.WorkDir)
		}
	}
	return &deny
}

// CodexHome is the isolated CODEX_HOME shared by isolated Codex workers;
// its sessions are read by the catalog alongside ~/.codex.
func (m *Manager) CodexHome() string { return filepath.Join(m.dataDir, "codex-home") }

func (m *Manager) addLocked(spec config.Worker, dynamic bool) error {
	config.ApplyWorkerDefaults(&spec)
	if spec.Agent == "codex" {
		spec.CodexHome = m.CodexHome()
	}
	r, err := RunnerFor(spec.Agent)
	if err != nil {
		return err
	}
	if st, err := os.Stat(spec.WorkDir); err != nil || !st.IsDir() {
		return fmt.Errorf("worker %s: work_dir %s is not a directory", spec.Name, spec.WorkDir)
	}
	m.workers[spec.Name] = &workerState{Spec: spec, Dynamic: dynamic, SessionID: spec.SessionID,
		runner: r, wake: make(chan struct{}, 1), stop: make(chan struct{})}
	return nil
}

func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	for _, w := range m.workers {
		go m.loop(ctx, w)
	}
	m.mu.Unlock()
	go m.reapLoop(ctx, 5*time.Minute)
}

// Delegate enqueues an instruction for a worker. ahead is the number of tasks
// (running or queued) in front of it; 0 means it starts right away. urgent
// jumps the queue but never preempts a running task.
func (m *Manager) Delegate(bot, worker, instruction, convKey string, req Requester, urgent bool) (t Task, ahead int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.workers[worker]
	if w == nil {
		return Task{}, 0, fmt.Errorf("no worker named %q", worker)
	}
	m.seq++
	task := &Task{ID: fmt.Sprintf("t%d", m.seq), Bot: bot, Worker: worker, Instruction: instruction, ConvKey: convKey,
		Requester: req, Status: StatusQueued, CreatedAt: time.Now()}
	m.tasks[task.ID] = task
	if urgent {
		w.queue = append([]*Task{task}, w.queue...)
		ahead = 0
	} else {
		w.queue = append(w.queue, task)
		ahead = len(w.queue) - 1
	}
	if w.current != nil {
		ahead++
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
	m.saveLocked()
	m.record(*task, fmt.Sprintf("ahead=%d urgent=%v", ahead, urgent))
	return *task, ahead, nil
}

func (m *Manager) Cancel(id string) (Task, error) {
	m.mu.Lock()
	t := m.tasks[id]
	if t == nil {
		m.mu.Unlock()
		return Task{}, fmt.Errorf("no task %q", id)
	}
	w := m.workers[t.Worker]
	switch t.Status {
	case StatusQueued:
		for i, q := range w.queue {
			if q == t {
				w.queue = append(w.queue[:i], w.queue[i+1:]...)
				break
			}
		}
		t.Status, t.EndedAt = StatusCancelled, time.Now()
		pushHistory(w, t)
		m.saveLocked()
		m.record(*t, "cancelled while queued")
	case StatusRunning:
		t.Status = StatusCancelled // loop keeps this status after the process dies
		if w.cancel != nil {
			w.cancel()
		}
	default:
		cp := *t
		m.mu.Unlock()
		return cp, fmt.Errorf("task %s already %s", id, t.Status)
	}
	cp := *t
	m.mu.Unlock()
	return cp, nil
}

func (m *Manager) Task(id string) (Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tasks[id]
	if t == nil {
		return Task{}, false
	}
	return *t, true
}

// ActiveTasksFor lists non-terminal tasks started from a conversation.
func (m *Manager) ActiveTasksFor(convKey string) []Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Task
	for _, t := range m.tasks {
		if t.ConvKey == convKey && (t.Status == StatusQueued || t.Status == StatusRunning) {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Overview renders a compact one-line-per-worker table for the brain's prompt,
// showing only what the caller may see.
func (m *Manager) Overview(scope Scope) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var names []string
	for n, w := range m.workers {
		if scope.access(w).See {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	catalog := m.templateCatalogLocked(scope)
	if len(names) == 0 {
		return "(no workers)\n" + catalog
	}
	var b strings.Builder
	for _, n := range names {
		w := m.workers[n]
		a := scope.access(w)
		status := "idle"
		if w.current != nil {
			status = "busy"
			if a.Read {
				status = fmt.Sprintf("busy[%s since %s: %s]", w.current.ID, w.current.StartedAt.Format("15:04"), oneLine(w.current.Instruction, 60))
				if w.current.Note != "" {
					status += " (" + w.current.Note + ")"
				}
			}
		}
		fmt.Fprintf(&b, "- %s (%s, %s) — %s\n  status: %s; queued: %d", n, w.Spec.Agent, w.Spec.Access, w.Spec.Description, status, len(w.queue))
		if w.Template != "" && w.Parent == "" {
			last := w.LastRun
			if last.IsZero() {
				last = w.CreatedAt
			}
			fmt.Fprintf(&b, "; from template %s, idle since %s", w.Template, last.Format("01-02 15:04"))
			if w.Branch != "" {
				fmt.Fprintf(&b, "; branch %s", w.Branch)
			}
		}
		switch {
		case !a.Read && w.Spec.PerUser != "" && a.Delegate:
			b.WriteString("; you get your own private copy of this worker")
		case !a.Read && a.Delegate:
			b.WriteString("; read-only: you may ask it questions")
		}
		if a.Read {
			fmt.Fprintf(&b, "; dir: %s", w.Spec.WorkDir)
			if w.SessionID != "" {
				fmt.Fprintf(&b, "; session: %s", w.SessionID)
			}
			if len(w.History) > 0 {
				last := w.History[len(w.History)-1]
				fmt.Fprintf(&b, "\n  last: %s %s %s → %s", last.ID, last.Status, oneLine(last.Instruction, 50), oneLine(firstNonEmpty(last.Result, last.Error), 80))
			}
		}
		b.WriteString("\n")
	}
	return b.String() + catalog
}

// ContextSummary is Overview plus, for workers the caller may read, the
// instruction and result of their latest tasks — enough for the brain to
// answer "what is X doing / what did X find" without another tool call.
func (m *Manager) ContextSummary(scope Scope, tasks int) string {
	out := m.Overview(scope)
	m.mu.Lock()
	defer m.mu.Unlock()
	var names []string
	for n, w := range m.workers {
		if scope.access(w).Read && len(w.History) > 0 {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return out
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("\nrecent results (latest first):\n")
	for _, n := range names {
		h := m.workers[n].History
		for i := len(h) - 1; i >= 0 && i >= len(h)-tasks; i-- {
			t := h[i]
			fmt.Fprintf(&b, "- %s %s [%s] %s, asked by %s: %s\n  → %s\n", n, t.ID, t.Status, t.EndedAt.Format("01-02 15:04"),
				t.Requester.Display(), oneLine(t.Instruction, 120), oneLine(firstNonEmpty(t.Result, t.Error), 600))
		}
	}
	return out + b.String()
}

// Detail renders a worker's queue and recent task results.
func (m *Manager) Detail(name string, n int, scope Scope) (string, error) {
	m.mu.Lock()
	w := m.workers[name]
	if w == nil || !scope.access(w).Read {
		m.mu.Unlock()
		return "", fmt.Errorf("no worker named %q you can read", name)
	}
	agent, sid := w.Spec.Agent, w.SessionID
	out, _ := m.detailLocked(name, w, n)
	m.mu.Unlock()
	// The session's own recent conversation, including work done outside bot-connect.
	if m.Sessions != nil && sid != "" {
		if h, err := m.Sessions.History(agent, sid, 6); err == nil && len(h) > 0 {
			out += "\n\n=== recent conversation in the session (latest last) ===\n" + RenderHistory(h, 600)
		}
	}
	return out, nil
}

// RenderHistory formats transcript entries for the brain.
func RenderHistory(h []session.Entry, each int) string {
	var b strings.Builder
	for _, e := range h {
		fmt.Fprintf(&b, "[%s %s] %s\n", e.Time.Format("01-02 15:04"), e.Role, clip(strings.TrimSpace(e.Text), each))
	}
	return b.String()
}

func (m *Manager) detailLocked(name string, w *workerState, n int) (string, error) {
	if n <= 0 {
		n = 3
	}
	var b strings.Builder
	fmt.Fprintf(&b, "worker %s (%s) dir=%s session=%s\n%s\n", name, w.Spec.Agent, w.Spec.WorkDir, orNone(w.SessionID), w.Spec.Description)
	if w.current != nil {
		fmt.Fprintf(&b, "\nRUNNING %s (since %s, by %s): %s\n", w.current.ID, w.current.StartedAt.Format("15:04:05"), w.current.Requester.Display(), w.current.Instruction)
	}
	for i, q := range w.queue {
		fmt.Fprintf(&b, "QUEUED #%d %s (by %s): %s\n", i+1, q.ID, q.Requester.Display(), oneLine(q.Instruction, 120))
	}
	start := len(w.History) - n
	if start < 0 {
		start = 0
	}
	for i := len(w.History) - 1; i >= start; i-- {
		t := w.History[i]
		fmt.Fprintf(&b, "\n--- %s [%s] %s (by %s)\ninstruction: %s\n", t.ID, t.Status, t.EndedAt.Format("01-02 15:04"), t.Requester.Display(), oneLine(t.Instruction, 300))
		if t.Error != "" {
			fmt.Fprintf(&b, "error: %s\n", clip(t.Error, 800))
		}
		if t.Result != "" {
			fmt.Fprintf(&b, "result: %s\n", clip(t.Result, 2500))
		}
	}
	if w.current == nil && len(w.queue) == 0 && len(w.History) == 0 {
		b.WriteString("\n(no bot-connect tasks yet)")
	}
	return b.String(), nil
}

func (m *Manager) loop(ctx context.Context, w *workerState) {
	for {
		m.mu.Lock()
		if len(w.queue) == 0 {
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-w.stop:
				return
			case <-w.wake:
				continue
			}
		}
		t := w.queue[0]
		w.queue = w.queue[1:]
		t.Status, t.StartedAt = StatusRunning, time.Now()
		w.current = t
		tctx, cancel := context.WithTimeout(ctx, w.Spec.TaskTimeout.Duration)
		w.cancel = cancel
		spec, sid, lastRun := w.Spec, w.SessionID, w.LastRun
		spec.DenyRead = m.runtimeDenyLocked(w)
		m.saveLocked()
		m.mu.Unlock()

		slog.Info("task start", "task", t.ID, "worker", spec.Name)
		m.record(*t, "")
		m.waitWhileUsedLocally(tctx, t, spec.Agent, sid, lastRun)
		var (
			result, newSID string
			err            error
		)
		if tctx.Err() == nil {
			result, newSID, err = w.runner.Run(tctx, spec, sid, workerPrompt(t))
		} else {
			err = tctx.Err()
		}
		timedOut := tctx.Err() == context.DeadlineExceeded
		cancel()

		m.mu.Lock()
		if newSID != "" {
			if newSID != sid {
				w.Owned = append(w.Owned, newSID) // a session bot-connect created
			}
			w.SessionID = newSID
		}
		t.Result, t.EndedAt = strings.TrimSpace(result), time.Now()
		switch {
		case t.Status == StatusCancelled:
		case timedOut:
			t.Status, t.Error = StatusFailed, "timed out after "+spec.TaskTimeout.String()
		case err != nil:
			t.Status, t.Error = StatusFailed, err.Error()
		default:
			t.Status = StatusSucceeded
		}
		w.current, w.cancel = nil, nil
		w.LastRun, t.Note = time.Now(), ""
		pushHistory(w, t)
		m.saveLocked()
		done := *t
		m.mu.Unlock()
		slog.Info("task end", "task", t.ID, "status", done.Status, "dur", done.EndedAt.Sub(done.StartedAt).Round(time.Second))
		m.record(done, "")
		if m.OnFinish != nil {
			m.OnFinish(done)
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// waitWhileUsedLocally holds a task while someone else (e.g. the owner in
// their terminal) has recently written to the same session, so we don't
// interleave turns into a conversation that is in use.
func (m *Manager) waitWhileUsedLocally(ctx context.Context, t *Task, agent, sid string, lastRun time.Time) {
	if m.Sessions == nil || m.BusyWindow <= 0 || sid == "" {
		return
	}
	noted := false
	for {
		in, err := m.Sessions.Find(sid)
		if err != nil || time.Since(in.Updated) > m.BusyWindow || !in.Updated.After(lastRun.Add(2*time.Second)) {
			if noted {
				m.mu.Lock()
				t.Note = ""
				m.mu.Unlock()
			}
			return
		}
		if !noted {
			noted = true
			m.mu.Lock()
			t.Note = "waiting: session in use locally"
			m.mu.Unlock()
			slog.Info("task waiting for locally used session", "task", t.ID, "session", sid)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func workerPrompt(t *Task) string {
	who := t.Requester.Display()
	return fmt.Sprintf("[bot-connect 委派的任务 %s，发起人：%s]\n\n%s\n\n"+
		"（完成后，最后一条消息请用简洁的中文总结：做了什么、结果如何、有没有需要人来决定的事。）",
		t.ID, who, t.Instruction)
}

func pushHistory(w *workerState, t *Task) {
	w.History = append(w.History, t)
	if len(w.History) > historyKeep {
		w.History = w.History[len(w.History)-historyKeep:]
	}
}

func (m *Manager) saveLocked() {
	p := persisted{Seq: m.seq, Workers: m.workers}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		slog.Error("marshal worker state", "err", err)
		return
	}
	tmp := m.statePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		_ = os.Rename(tmp, m.statePath)
	}
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	return clip(s, n)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func orNone(s string) string {
	if s == "" {
		return "(new)"
	}
	return s
}
