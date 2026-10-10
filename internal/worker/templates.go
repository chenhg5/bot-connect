package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/audit"
	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/identity"
)

// CreateOptions is what the brain chooses when creating a worker from a
// template; everything else (agent, access, tools…) comes from the template.
type CreateOptions struct {
	Name    string // default: <template>-<n>
	Purpose string // what this worker is for (shown to the brain when routing)
	Dir     string // workspace = existing: the directory (under the template's roots)
	Repo    string // workspace = worktree: the repository (default: the template's repo)
	Bot     string
	By      identity.User
}

// TemplateInfo is a template plus how many workers use it.
type TemplateInfo struct {
	config.Template
	InUse int `json:"in_use"`
}

// Templates lists the templates the scope may use, with capacity.
func (m *Manager) Templates(s Scope) []TemplateInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []TemplateInfo
	for n, t := range m.templates {
		if s.Template != nil && !s.Template[n] {
			continue
		}
		out = append(out, TemplateInfo{Template: t, InUse: m.inUseLocked(n)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Manager) inUseLocked(tpl string) int {
	n := 0
	for _, w := range m.workers {
		if w.Template == tpl && w.Parent == "" {
			n++
		}
	}
	return n
}

// CreateFromTemplate creates (and starts) a worker from a template. Only
// owners/admins may, and only from templates the scope allows; the template
// fixes what the worker may do.
func (m *Manager) CreateFromTemplate(tpl string, o CreateOptions, s Scope) (WorkerInfo, error) {
	if !s.Caller.Role.Privileged() {
		return WorkerInfo{}, fmt.Errorf("permission denied: only the owner or an admin can create workers")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.templates[tpl]
	if !ok || (s.Template != nil && !s.Template[tpl]) {
		return WorkerInfo{}, fmt.Errorf("no template %q (available: %s)", tpl, strings.Join(m.templateNamesLocked(s), ", "))
	}
	if n := m.inUseLocked(tpl); n >= t.MaxInstances {
		return WorkerInfo{}, fmt.Errorf("template %s is at capacity (%d/%d workers): reuse one of %s, or retire one first",
			tpl, n, t.MaxInstances, strings.Join(m.ofTemplateLocked(tpl), ", "))
	}
	name := o.Name
	if name == "" {
		for i := 1; ; i++ {
			if name = fmt.Sprintf("%s-%d", tpl, i); m.workers[name] == nil {
				break
			}
		}
	}
	if !validName(name) {
		return WorkerInfo{}, fmt.Errorf("invalid worker name %q: use letters, digits, - and _ (max 40)", name)
	}
	if m.workers[name] != nil {
		return WorkerInfo{}, fmt.Errorf("worker %q already exists", name)
	}

	spec := config.Worker{Name: name, Agent: t.Agent, Model: t.Model, Access: t.Access, ReadDirs: t.ReadDirs,
		Tools: t.Tools, TaskTimeout: t.TaskTimeout, DenyRead: t.DenyRead, Isolate: t.Isolate, Env: t.Env,
		Description: firstNonEmpty(o.Purpose, t.Description)}
	var branch, repo string
	switch t.Workspace {
	case "dir":
		spec.WorkDir = filepath.Join(m.dataDir, "workspaces", tpl, name)
		if err := os.MkdirAll(spec.WorkDir, 0o700); err != nil {
			return WorkerInfo{}, err
		}
	case "existing":
		dir, err := underRoots(o.Dir, t.Roots)
		if err != nil {
			return WorkerInfo{}, err
		}
		spec.WorkDir = dir
	case "worktree":
		repo = t.Repo
		if o.Repo != "" {
			r, err := underRoots(o.Repo, append([]string{t.Repo}, t.Roots...))
			if err != nil {
				return WorkerInfo{}, err
			}
			repo = r
		}
		if repo == "" {
			return WorkerInfo{}, fmt.Errorf("template %s needs repo=<a git repository under %s>", tpl, strings.Join(t.Roots, ", "))
		}
		dir := filepath.Join(m.dataDir, "worktrees", tpl, name)
		branch = "bot-connect/" + name
		if err := addWorktree(repo, dir, branch); err != nil {
			return WorkerInfo{}, err
		}
		spec.WorkDir = dir
		// Commits write to the repository's git dir, outside the worktree.
		if out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output(); err == nil {
			spec.WriteDirs = []string{strings.TrimSpace(string(out))}
		}
	}
	if err := m.addLocked(spec, true); err != nil {
		return WorkerInfo{}, err
	}
	w := m.workers[name]
	by := o.By
	w.Template, w.Bot, w.CreatedBy, w.CreatedAt, w.Branch, w.RepoDir = tpl, o.Bot, &by, time.Now(), branch, repo
	if m.ctx != nil {
		go m.loop(m.ctx, w)
	}
	m.saveLocked()
	m.event(w, "worker_created", by, "")
	return m.infoLocked(name, w), nil
}

// UpdatePurpose changes what a template worker is for (nothing else about
// it can change: that would be a different template).
func (m *Manager) UpdatePurpose(name, purpose string, s Scope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.workers[name]
	if w == nil || w.Template == "" || !s.access(w).Delegate {
		return fmt.Errorf("no worker named %q created from a template", name)
	}
	w.Spec.Description = purpose
	m.saveLocked()
	return nil
}

// Retire removes a worker created from a template. A busy worker is only
// retired with force (its tasks are cancelled). Its directory is kept,
// except a worktree checkout with nothing uncommitted (the branch stays).
func (m *Manager) Retire(name string, force bool, s Scope) (string, error) {
	m.mu.Lock()
	w := m.workers[name]
	if w == nil || w.Parent != "" || w.Template == "" || !s.access(w).Delegate {
		m.mu.Unlock()
		return "", fmt.Errorf("no worker named %q created from a template (configured workers are retired by editing the config)", name)
	}
	m.mu.Unlock()
	return m.retire(name, force, s.Caller, "")
}

func (m *Manager) retire(name string, force bool, by identity.User, why string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.workers[name]
	if w == nil {
		return "", fmt.Errorf("no worker named %q", name)
	}
	family := []*workerState{w}
	for _, c := range m.workers {
		if c.Parent == name {
			family = append(family, c)
		}
	}
	for _, f := range family {
		if (f.current != nil || len(f.queue) > 0) && !force {
			return "", fmt.Errorf("worker %s is busy (%d queued); wait, or retire with force=true to cancel its tasks", f.Spec.Name, len(f.queue)+boolInt(f.current != nil))
		}
	}
	note := "directory kept: " + w.Spec.WorkDir
	if w.Branch != "" {
		dirty, err := gitDirty(w.Spec.WorkDir)
		switch {
		case err != nil:
			note = fmt.Sprintf("worktree %s kept (%v)", w.Spec.WorkDir, err)
		case dirty && !force:
			return "", fmt.Errorf("worktree %s has uncommitted changes; ask the worker to commit them, or retire with force=true to keep the directory as is", w.Spec.WorkDir)
		case dirty:
			note = fmt.Sprintf("worktree %s kept (uncommitted changes); branch %s", w.Spec.WorkDir, w.Branch)
		default:
			if out, err := exec.Command("git", "-C", w.RepoDir, "worktree", "remove", w.Spec.WorkDir).CombinedOutput(); err != nil {
				note = fmt.Sprintf("worktree %s kept (%s)", w.Spec.WorkDir, strings.TrimSpace(string(out)))
			} else {
				note = fmt.Sprintf("worktree removed; branch %s kept in %s", w.Branch, w.RepoDir)
			}
		}
	}
	for _, f := range family {
		for _, q := range f.queue {
			q.Status, q.EndedAt, q.Error = StatusCancelled, time.Now(), "worker retired"
		}
		f.queue = nil
		if f.current != nil {
			f.current.Status = StatusCancelled
			if f.cancel != nil {
				f.cancel()
			}
		}
		close(f.stop)
		delete(m.workers, f.Spec.Name)
	}
	m.saveLocked()
	m.event(w, "worker_retired", by, strings.TrimSpace(why+" "+note))
	return fmt.Sprintf("Retired %s; %s", name, note), nil
}

// Reap retires template workers idle for longer than their template's
// idle_ttl. Workers with uncommitted work are skipped (and logged).
func (m *Manager) Reap(now time.Time) []string {
	m.mu.Lock()
	var due []string
	for n, w := range m.workers {
		t, ok := m.templates[w.Template]
		if w.Template == "" || w.Parent != "" || !ok || t.TTL() <= 0 {
			continue
		}
		last := w.LastRun
		if last.IsZero() {
			last = w.CreatedAt
		}
		busy := w.current != nil || len(w.queue) > 0
		for _, c := range m.workers {
			if c.Parent == n {
				busy = busy || c.current != nil || len(c.queue) > 0
				if c.LastRun.After(last) {
					last = c.LastRun
				}
			}
		}
		if !busy && now.Sub(last) > t.TTL() {
			due = append(due, n)
		}
	}
	m.mu.Unlock()
	var done []string
	for _, n := range due {
		if _, err := m.retire(n, false, identity.User{ID: "bot-connect", Name: "idle reaper"}, "idle"); err != nil {
			slog.Info("reaper: keep worker", "worker", n, "why", err)
			continue
		}
		done = append(done, n)
	}
	return done
}

func (m *Manager) reapLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.Reap(now)
		}
	}
}

func (m *Manager) event(w *workerState, what string, by identity.User, note string) {
	if m.Audit == nil {
		return
	}
	e := audit.Event{Type: audit.Task, Bot: w.Bot, User: &by, Worker: w.Spec.Name, Status: what,
		Extra: map[string]any{"template": w.Template, "dir": w.Spec.WorkDir}}
	if note != "" {
		e.Extra["note"] = note
	}
	m.Audit.Record(e)
}

func (m *Manager) templateNamesLocked(s Scope) []string {
	var out []string
	for n := range m.templates {
		if s.Template == nil || s.Template[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return []string{"(none configured)"}
	}
	return out
}

func (m *Manager) ofTemplateLocked(tpl string) []string {
	var out []string
	for n, w := range m.workers {
		if w.Template == tpl && w.Parent == "" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// templateCatalogLocked renders the templates for the brain's prompt.
func (m *Manager) templateCatalogLocked(s Scope) string {
	if !s.Caller.Role.Privileged() {
		return ""
	}
	var names []string
	for n := range m.templates {
		if s.Template == nil || s.Template[n] {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("\ntemplates (create a worker with worker_create, or delegate with template=…):\n")
	for _, n := range names {
		t := m.templates[n]
		where := map[string]string{"dir": "fresh empty dir", "existing": "a dir under " + strings.Join(t.Roots, ", "),
			"worktree": "new git worktree of " + firstNonEmpty(t.Repo, "a repo under "+strings.Join(t.Roots, ", "))}[t.Workspace]
		fmt.Fprintf(&b, "- %s (%s, %s; %s) — %s; in use %d/%d\n", n, t.Agent, t.Access, where, t.Description, m.inUseLocked(n), t.MaxInstances)
	}
	return b.String()
}

func (m *Manager) infoLocked(n string, w *workerState) WorkerInfo {
	wi := WorkerInfo{Name: n, Agent: w.Spec.Agent, Access: w.Spec.Access, Dir: w.Spec.WorkDir, Description: w.Spec.Description,
		Base: w.Base, User: w.UserID, Parent: w.Parent, Session: w.SessionID, Owned: w.Owned, PerUser: w.Spec.PerUser,
		Queued: len(w.queue), LastRun: w.LastRun, Template: w.Template, Branch: w.Branch, CreatedAt: w.CreatedAt}
	if w.current != nil {
		wi.Running = w.current.ID
	}
	return wi
}

// underRoots resolves dir and checks it is (under) one of roots.
func underRoots(dir string, roots []string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("a directory is required (under %s)", strings.Join(nonEmpty(roots), ", "))
	}
	abs, err := filepath.Abs(config.ExpandHome(dir))
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		r, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			r = rr
		}
		if rel, err := filepath.Rel(r, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return abs, nil
		}
	}
	return "", fmt.Errorf("permission denied: %s is outside this template's roots (%s)", abs, strings.Join(nonEmpty(roots), ", "))
}

func addWorktree(repo, dir, branch string) error {
	if out, err := exec.Command("git", "-C", repo, "rev-parse", "--is-inside-work-tree").CombinedOutput(); err != nil {
		return fmt.Errorf("%s is not a git repository: %s", repo, strings.TrimSpace(string(out)))
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	args := []string{"-C", repo, "worktree", "add", "-b", branch, dir}
	if exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil {
		args = []string{"-C", repo, "worktree", "add", dir, branch} // reuse a branch a retired worker left
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitDirty(dir string) (bool, error) {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return false, fmt.Errorf("git status: %v", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func validName(n string) bool {
	if n == "" || len(n) > 40 {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func nonEmpty(v []string) []string {
	var out []string
	for _, x := range v {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
