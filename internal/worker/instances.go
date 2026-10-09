package worker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/session"
)

// DelegateTarget resolves the worker a task should run on: the named worker
// itself, or — for a member using a per_user worker — that member's own
// instance (created on first use, with its own session and directory).
func (m *Manager) DelegateTarget(name string, s Scope) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.workers[name]
	if w == nil || !s.access(w).Delegate {
		return "", fmt.Errorf("no worker named %q you can hand work to", name)
	}
	if w.Spec.PerUser == "" || w.Base != "" || w.Parent != "" || s.Caller.Role.Privileged() {
		return name, nil
	}
	inst, err := m.instanceLocked(w, s.Caller)
	if err != nil {
		return "", err
	}
	return inst.Spec.Name, nil
}

func userKey(id string) string {
	k := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, id)
	if len(k) > 40 {
		k = k[len(k)-40:]
	}
	return k
}

func (m *Manager) instanceLocked(base *workerState, u identity.User) (*workerState, error) {
	name := base.Spec.Name + "@" + userKey(u.ID)
	if w := m.workers[name]; w != nil {
		return w, nil
	}
	dir, err := m.instanceDir(base, userKey(u.ID))
	if err != nil {
		return nil, fmt.Errorf("prepare workspace for %s: %w", u.Display(), err)
	}
	spec := base.Spec
	spec.Name, spec.WorkDir = name, dir
	spec.SessionID, spec.Sessions, spec.DirSessions, spec.PerUser, spec.Public = "", nil, false, "", false
	spec.Description = base.Spec.Description + " — private workspace of " + u.Display()
	isolate := true // a member's worker never sees the owner's Claude setup
	spec.Isolate = &isolate
	if err := m.addLocked(spec, true); err != nil {
		return nil, err
	}
	w := m.workers[name]
	w.Base, w.UserID = base.Spec.Name, u.ID
	if m.ctx != nil {
		go m.loop(m.ctx, w)
	}
	m.saveLocked()
	return w, nil
}

func (m *Manager) instanceDir(base *workerState, key string) (string, error) {
	switch base.Spec.PerUser {
	case "worktree":
		dir := filepath.Join(m.dataDir, "worktrees", base.Spec.Name, key)
		if _, err := os.Stat(dir); err == nil {
			return dir, nil
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return "", err
		}
		branch := "bot-connect/" + base.Spec.Name + "/" + key
		out, err := exec.Command("git", "-C", base.Spec.WorkDir, "worktree", "add", "-b", branch, dir).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git worktree add: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return dir, nil
	default: // "dir"
		root := base.Spec.PerUserRoot
		if root == "" {
			root = filepath.Join(m.dataDir, "workspaces", base.Spec.Name)
		}
		dir := filepath.Join(root, key)
		return dir, os.MkdirAll(dir, 0o700)
	}
}

// AttachSession returns the worker that runs tasks in session in, creating a
// sub-worker of parent when it isn't the parent's current session (one
// worker, hence one queue, per session). The caller must have checked that
// parent's policy allows the session.
func (m *Manager) AttachSession(parent string, in session.Info) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for n, w := range m.workers {
		if w.SessionID == in.ID {
			return n, nil
		}
	}
	p := m.workers[parent]
	if p == nil || p.Parent != "" {
		return "", fmt.Errorf("no worker named %q", parent)
	}
	id := in.ID
	if len(id) > 8 {
		id = id[:8]
	}
	spec := p.Spec // inherit agent, access, provider env…
	spec.Name = parent + "#" + id
	spec.SessionID, spec.Sessions, spec.DirSessions, spec.PerUser = in.ID, nil, false, ""
	spec.Description = "session of " + parent + ": " + in.Title
	if err := m.addLocked(spec, true); err != nil {
		return "", err
	}
	w := m.workers[spec.Name]
	w.Parent, w.Base, w.UserID = parent, p.base(), p.UserID
	if p.Base == "" {
		w.Base = parent
	}
	if m.ctx != nil {
		go m.loop(m.ctx, w)
	}
	m.saveLocked()
	return spec.Name, nil
}

// SessionPolicy is what a session-owning worker (configured or instance) covers.
type SessionPolicy struct {
	Agent, Dir string
	IDs        map[string]bool // explicit + owned + current sessions
	DirAll     bool            // every session of Agent in Dir
}

func (p SessionPolicy) Allows(in session.Info) bool {
	return in.Agent == p.Agent && (p.IDs[in.ID] || (p.DirAll && sameDir(in.Dir, p.Dir)))
}

func sameDir(a, b string) bool { return strings.TrimRight(a, "/") == strings.TrimRight(b, "/") }

// Roots lists the session-owning workers (no sub-workers) the caller may read.
func (m *Manager) Roots(s Scope) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for n, w := range m.workers {
		if w.Parent == "" && s.access(w).Read {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Policy returns the session policy of a root worker the caller may read.
func (m *Manager) Policy(name string, s Scope) (SessionPolicy, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.workers[name]
	if w == nil || w.Parent != "" || !s.access(w).Read {
		return SessionPolicy{}, false
	}
	p := SessionPolicy{Agent: w.Spec.Agent, Dir: w.Spec.WorkDir, IDs: map[string]bool{}, DirAll: w.Spec.DirSessions}
	add := func(id string) {
		if id != "" {
			p.IDs[id] = true
		}
	}
	add(w.SessionID)
	add(w.Spec.SessionID)
	for _, id := range w.Spec.Sessions {
		add(id)
	}
	for _, id := range w.Owned {
		add(id)
	}
	for _, c := range m.workers { // sessions of its sub-workers count as its own
		if c.Parent == name {
			add(c.SessionID)
			for _, id := range c.Owned {
				add(id)
			}
		}
	}
	return p, true
}

// SessionOf returns a worker's current session id.
func (m *Manager) SessionOf(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.workers[name]; w != nil {
		return w.SessionID
	}
	return ""
}

// WorkerForSession returns the worker bound to a session id, if any.
func (m *Manager) WorkerForSession(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for n, w := range m.workers {
		if w.SessionID == id {
			return n
		}
	}
	return ""
}

// DenyFor returns the deny_read list a worker would run with (for tests / diagnostics).
func (m *Manager) DenyFor(name string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.workers[name]; w != nil {
		if d := m.runtimeDenyLocked(w); d != nil {
			return *d
		}
	}
	return nil
}

// DirOf returns a worker's directory.
func (m *Manager) DirOf(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.workers[name]; w != nil {
		return w.Spec.WorkDir
	}
	return ""
}
