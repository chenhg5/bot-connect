// Package tools defines what a brain can do: inspect and command workers,
// talk to the conversation, reach the owner. The same registry is exposed over
// MCP and over the `bot-connect tool` CLI, and privilege is enforced here —
// not in any brain's prompt — so every brain implementation gets the same rules.
package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/session"
	"github.com/chenhg5/bot-connect/internal/worker"
)

// TurnContext is what a tool call is bound to: the conversation the brain is
// answering and the (least-privileged) caller of that turn.
type TurnContext struct {
	ConvKey  string
	Platform string
	Caller   worker.Requester
	// Allow, if non-empty, limits the tools this brain may see and call
	// (brain.tools in config). Privilege checks still apply on top.
	Allow map[string]bool
}

func (tc TurnContext) allowed(t *Tool) bool {
	if len(tc.Allow) > 0 && !tc.Allow[t.Name] {
		return false
	}
	return !t.OwnerOnly || tc.Caller.Privileged()
}

// Messenger is the slice of the hub the tools need.
type Messenger interface {
	NotifyOwner(ctx context.Context, text string) error
	SendTo(ctx context.Context, convKey, text string) error
}

type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	OwnerOnly   bool
	Handler     func(ctx context.Context, tc TurnContext, args map[string]any) (string, error)
}

type Registry struct {
	byName map[string]*Tool
	names  []string
}

// Names lists every tool name.
func (r *Registry) Names() []string { return append([]string(nil), r.names...) }

// List returns the tools visible to the turn's caller.
func (r *Registry) List(tc TurnContext) []*Tool {
	var out []*Tool
	for _, n := range r.names {
		if t := r.byName[n]; tc.allowed(t) {
			out = append(out, t)
		}
	}
	return out
}

func (r *Registry) Call(ctx context.Context, tc TurnContext, name string, args map[string]any) (string, error) {
	t := r.byName[name]
	if t == nil {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	if len(tc.Allow) > 0 && !tc.Allow[name] {
		return "", fmt.Errorf("tool %s is not enabled for this brain", name)
	}
	if t.OwnerOnly && !tc.Caller.Privileged() {
		return "", fmt.Errorf("permission denied: %s needs owner/admin, and this turn involves %s (%s)", name, tc.Caller.Display(), tc.Caller.Role)
	}
	if args == nil {
		args = map[string]any{}
	}
	return t.Handler(ctx, tc, args)
}

func (r *Registry) add(t Tool) {
	if t.Schema == nil {
		t.Schema = obj(nil)
	}
	r.byName[t.Name] = &t
	r.names = append(r.names, t.Name)
}

// Env is what one bot's tools operate on.
type Env struct {
	Bot       string
	Workers   *worker.Manager
	Sessions  *session.Catalog
	Messenger Messenger
	// Scope is the privileged view of the worker pool for this bot (its
	// workers); the caller's role narrows it further per worker.
	Scope worker.Scope
}

func (e Env) scope(tc TurnContext) worker.Scope {
	sc := e.Scope
	sc.Caller = tc.Caller
	return sc
}

// sessionOwner returns the worker (in scope) whose policy allows session
// id, plus the session — or an error that reveals nothing about sessions
// outside the allowlist.
func (e Env) sessionOwner(tc TurnContext, id string) (string, session.Info, error) {
	if e.Sessions == nil || id == "" {
		return "", session.Info{}, fmt.Errorf("no session %q", id)
	}
	in, err := e.Sessions.Find(id)
	if err != nil {
		return "", session.Info{}, fmt.Errorf("no session %q", id)
	}
	for _, root := range e.Workers.Roots(e.scope(tc)) {
		if p, ok := e.Workers.Policy(root, e.scope(tc)); ok && p.Allows(in) {
			return root, in, nil
		}
	}
	return "", session.Info{}, fmt.Errorf("no session %q", id)
}

// allowedSessions lists sessions covered by the workers in scope.
func (e Env) allowedSessions(tc TurnContext, only string) []session.Info {
	var out []session.Info
	seen := map[string]bool{}
	for _, root := range e.Workers.Roots(e.scope(tc)) {
		if only != "" && root != only {
			continue
		}
		p, ok := e.Workers.Policy(root, e.scope(tc))
		if !ok {
			continue
		}
		for id := range p.IDs {
			if in, err := e.Sessions.Find(id); err == nil && !seen[in.ID] && p.Allows(in) {
				seen[in.ID] = true
				out = append(out, in)
			}
		}
		if p.DirAll {
			l, _ := e.Sessions.List(p.Agent, 30, func(d string) bool { return strings.TrimRight(d, "/") == strings.TrimRight(p.Dir, "/") })
			for _, in := range l {
				if !seen[in.ID] {
					seen[in.ID] = true
					out = append(out, in)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// target resolves {worker, session} to the worker that should run the task.
func (e Env) target(tc TurnContext, a map[string]any) (string, error) {
	name, id := s(a, "worker"), s(a, "session")
	if id == "" {
		return e.Workers.DelegateTarget(name, e.scope(tc))
	}
	root, in, err := e.sessionOwner(tc, id)
	if err != nil {
		return "", err
	}
	if name != "" && name != root && !strings.HasPrefix(name, root+"#") {
		return "", fmt.Errorf("session %s does not belong to worker %s", id, name)
	}
	return e.Workers.AttachSession(root, in)
}

func New(env Env) *Registry {
	r := &Registry{byName: map[string]*Tool{}}
	workers := env.Workers

	r.add(Tool{
		Name:        "list_workers",
		Description: "List the workers with live status: idle/busy, queue length, last task. A worker is one agent session (configured, or an existing session you delegated to).",
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			return workers.Overview(env.scope(tc)), nil
		},
	})
	r.add(Tool{
		Name: "list_sessions",
		Description: "List the agent sessions your workers cover (only those — nothing else on the machine), newest first: id, agent, directory, title, last message, last update. " +
			"Use it to find the session that holds the context for a question, then read_session, or delegate with session=<id>.",
		Schema: obj(props{
			"worker": str("only sessions of this worker"),
			"query":  str("only sessions whose title or last message contains this text"),
			"limit":  num("max results (default 15)"),
		}),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			if env.Sessions == nil {
				return "", fmt.Errorf("sessions are not available")
			}
			q, limit := strings.ToLower(s(a, "query")), n(a, "limit", 15)
			var b strings.Builder
			shown := 0
			for _, in := range env.allowedSessions(tc, s(a, "worker")) {
				if q != "" && !strings.Contains(strings.ToLower(in.Title+" "+in.Last), q) {
					continue
				}
				fmt.Fprintf(&b, "- %s  [%s] %s  updated %s\n  title: %s\n  last: %s\n", in.ID, in.Agent, in.Dir, ago(in.Updated), in.Title, clip(in.Last, 140))
				if w := workers.WorkerForSession(in.ID); w != "" {
					fmt.Fprintf(&b, "  worker: %s\n", w)
				}
				if shown++; shown >= limit {
					break
				}
			}
			if shown == 0 {
				return "(no sessions — workers only cover the sessions they own, list in `sessions`, or (dir_sessions) find in their directory)", nil
			}
			return b.String(), nil
		},
	})
	r.add(Tool{
		Name:        "read_session",
		Description: "Read the recent conversation of a session your workers cover (by session id or worker name), including work done outside bot-connect.",
		Schema:      obj(props{"session": str("session id"), "worker": str("or a worker name (its current session)"), "limit": num("how many recent messages (default 12)")}),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			if env.Sessions == nil {
				return "", fmt.Errorf("sessions are not available")
			}
			id := s(a, "session")
			if id == "" {
				name := s(a, "worker")
				if name == "" || !workers.Visible(name, env.scope(tc)) {
					return "", fmt.Errorf("no worker named %q", name)
				}
				if id = workers.SessionOf(name); id == "" {
					return "(this worker has no session yet)", nil
				}
			}
			_, in, err := env.sessionOwner(tc, id)
			if err != nil {
				return "", err
			}
			h, err := env.Sessions.History(in.Agent, in.ID, n(a, "limit", 12))
			if err != nil {
				return "", err
			}
			head := fmt.Sprintf("session %s [%s] %s\ntitle: %s\nupdated %s, %d messages\n\n", in.ID, in.Agent, in.Dir, in.Title, ago(in.Updated), in.Messages)
			return head + worker.RenderHistory(h, 1200), nil
		},
	})
	r.add(Tool{
		Name:        "read_worker",
		Description: "Show one worker in detail: running / queued tasks, results of recent bot-connect tasks, and the latest messages in its session.",
		Schema:      obj(props{"name": str("worker name"), "recent": num("how many recent tasks to show (default 3)")}, "name"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			return workers.Detail(s(a, "name"), n(a, "recent", 3), env.scope(tc))
		},
	})
	r.add(Tool{
		Name: "delegate",
		Description: "Hand work to a worker, asynchronously — by worker name, optionally in one of the sessions it covers (session=<id>). " +
			"Members are routed to their own private copy of per-user workers; read-only workers only answer questions. " +
			"Returns a task id and queue position immediately; the result comes back to this conversation as a task report. " +
			"The worker cannot see this chat: the instruction must be self-contained (goal, context, constraints, definition of done). " +
			"If the session is being used locally right now, the task waits until it is idle.",
		Schema: obj(props{
			"worker":      str("worker name"),
			"session":     str("optional: continue this session (from list_sessions) instead of the worker's current one"),
			"instruction": str("complete, self-contained instruction"),
			"urgent":      boolean("put at the front of the queue (never interrupts a running task); default false"),
		}, "instruction"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			if s(a, "instruction") == "" {
				return "", fmt.Errorf("instruction is empty")
			}
			name, err := env.target(tc, a)
			if err != nil {
				return "", err
			}
			urgent, _ := a["urgent"].(bool)
			t, ahead, err := workers.Delegate(env.Bot, name, s(a, "instruction"), tc.ConvKey, tc.Caller, urgent)
			if err != nil {
				return "", err
			}
			if ahead == 0 {
				return fmt.Sprintf("Task %s created on worker %s; it was idle and has started.", t.ID, t.Worker), nil
			}
			return fmt.Sprintf("Task %s created on worker %s; it is busy, %d task(s) ahead — queued.", t.ID, t.Worker, ahead), nil
		},
	})
	r.add(Tool{
		Name:        "task_status",
		Description: "Get a task's status and result.",
		Schema:      obj(props{"task_id": str("task id, e.g. t3")}, "task_id"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			t, ok := workers.Task(s(a, "task_id"))
			if !ok || !workers.TaskVisible(t, env.scope(tc)) {
				return "", fmt.Errorf("no such task")
			}
			out := fmt.Sprintf("%s @%s [%s]\ninstruction: %s", t.ID, t.Worker, t.Status, clip(t.Instruction, 300))
			if t.Note != "" {
				out += "\nnote: " + t.Note
			}
			if t.Error != "" {
				out += "\nerror: " + clip(t.Error, 800)
			}
			if t.Result != "" {
				out += "\nresult: " + clip(t.Result, 3000)
			}
			return out, nil
		},
	})
	r.add(Tool{
		Name:        "cancel_task",
		Description: "Cancel a queued or running task.",
		Schema:      obj(props{"task_id": str("task id")}, "task_id"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			if t, ok := workers.Task(s(a, "task_id")); !ok || !workers.TaskVisible(t, env.scope(tc)) {
				return "", fmt.Errorf("no such task")
			}
			t, err := workers.Cancel(s(a, "task_id"))
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Cancelled %s @%s", t.ID, t.Worker), nil
		},
	})
	r.add(Tool{
		Name:        "create_worker",
		Description: "Start a NEW agent session on a directory as a worker. Only when the owner asks for a fresh session; to continue existing work use delegate with session=<id>.",
		OwnerOnly:   true,
		Schema: obj(props{
			"name":        str("short name"),
			"agent":       map[string]any{"type": "string", "enum": []string{"claudecode", "codex"}},
			"work_dir":    str("absolute path"),
			"description": str("what this worker is responsible for"),
		}, "name", "agent", "work_dir", "description"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			dir := config.ExpandHome(s(a, "work_dir"))
			inside := false
			for _, root := range workers.Roots(env.scope(tc)) {
				if p, ok := workers.Policy(root, env.scope(tc)); ok && (sameOrUnder(dir, p.Dir)) {
					inside = true
				}
			}
			if !inside {
				return "", fmt.Errorf("%s is not inside any of this bot's worker directories", dir)
			}
			err := workers.AddWorker(config.Worker{Name: s(a, "name"), Agent: s(a, "agent"), WorkDir: dir, Description: s(a, "description")})
			if err != nil {
				return "", err
			}
			return "Created worker " + s(a, "name"), nil
		},
	})
	r.add(Tool{
		Name:        "send_message",
		Description: "Send an extra message to the current conversation right now (e.g. a quick acknowledgement before slow lookups). Your final answer is sent automatically; don't repeat it here.",
		Schema:      obj(props{"text": str("message text (Markdown ok)")}, "text"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			if err := env.Messenger.SendTo(ctx, tc.ConvKey, s(a, "text")); err != nil {
				return "", err
			}
			return "sent", nil
		},
	})
	r.add(Tool{
		Name:        "notify_owner",
		Description: "Forward a visitor's request or message to the owner's private chat.",
		Schema:      obj(props{"message": str("what to tell the owner: who, and what they want")}, "message"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			text := fmt.Sprintf("📨 来自「%s」（%s · %s）的留言：\n%s", tc.Caller.Display(), tc.Platform, tc.Caller.ID, s(a, "message"))
			if err := env.Messenger.NotifyOwner(ctx, text); err != nil {
				return "", err
			}
			return "forwarded to owner", nil
		},
	})
	sort.Strings(r.names)
	return r
}

func sameOrUnder(d, root string) bool {
	d, root = strings.TrimRight(d, "/"), strings.TrimRight(root, "/")
	return d == root || strings.HasPrefix(d, root+"/")
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return t.Format("2006-01-02")
}

// ---- schema helpers ----

type props = map[string]any

func obj(p props, required ...string) map[string]any {
	if p == nil {
		p = props{}
	}
	o := map[string]any{"type": "object", "properties": p}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}
func str(d string) map[string]any     { return map[string]any{"type": "string", "description": d} }
func num(d string) map[string]any     { return map[string]any{"type": "integer", "description": d} }
func boolean(d string) map[string]any { return map[string]any{"type": "boolean", "description": d} }

func s(a map[string]any, k string) string { v, _ := a[k].(string); return strings.TrimSpace(v) }

func n(a map[string]any, k string, def int) int {
	if f, ok := a[k].(float64); ok && f > 0 {
		return int(f)
	}
	return def
}

func clip(x string, max int) string {
	r := []rune(x)
	if len(r) <= max {
		return x
	}
	return string(r[:max]) + "…"
}
