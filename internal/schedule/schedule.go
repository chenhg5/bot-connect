// Package schedule runs recurring and one-shot jobs. A job doesn't execute
// anything itself: when due it is posted into the conversation that created
// it, as a message the brain handles like any other (answer, or delegate).
//
// Schedules are a persistence channel (an instruction that fires later,
// unattended), so: only owners/admins create them, the creator's role is
// re-checked on every run, and everything is recorded in the audit log.
package schedule

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/chenhg5/bot-connect/internal/identity"
)

// MinInterval is the shortest allowed gap between runs (cost guard).
const MinInterval = 5 * time.Minute

type Job struct {
	ID          string        `json:"id"`
	Bot         string        `json:"bot,omitempty"`
	ConvKey     string        `json:"conv"`
	Spec        string        `json:"spec"` // cron, @every, @daily…, TZ=… prefix, or @once <time|duration>
	Prompt      string        `json:"prompt"`
	Description string        `json:"description,omitempty"`
	CreatedBy   identity.User `json:"created_by"`
	CreatedAt   time.Time     `json:"created_at"`
	Enabled     bool          `json:"enabled"`
	NextRun     time.Time     `json:"next_run,omitempty"`
	LastRun     time.Time     `json:"last_run,omitempty"`
	LastStatus  string        `json:"last_status,omitempty"` // fired | skipped_running | done | disabled: …
	Runs        int           `json:"runs"`
	Once        bool          `json:"once,omitempty"`
	Running     bool          `json:"running,omitempty"` // fired, turn not finished yet
}

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Next validates spec and returns the first run after now.
// "@once 2h" / "@once 2026-10-10T15:00" / "@once 15:00" are one-shot.
func Next(spec string, now time.Time) (next time.Time, once bool, err error) {
	spec = strings.TrimSpace(spec)
	if rest, ok := strings.CutPrefix(spec, "@once "); ok {
		t, err := parseWhen(strings.TrimSpace(rest), now)
		if err != nil {
			return time.Time{}, true, err
		}
		if !t.After(now) {
			return time.Time{}, true, fmt.Errorf("@once time %s is in the past", t.Format(time.RFC3339))
		}
		return t, true, nil
	}
	sch, err := parser.Parse(spec)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("invalid schedule %q: %v (examples: \"0 9 * * 1-5\", \"@every 30m\", \"@daily\", \"TZ=Asia/Shanghai 0 9 * * *\", \"@once 2h\")", spec, err)
	}
	first := sch.Next(now)
	if gap := sch.Next(first).Sub(first); gap < MinInterval {
		return time.Time{}, false, fmt.Errorf("runs every %s; the minimum interval is %s", gap, MinInterval)
	}
	return first, false, nil
}

func parseWhen(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t, nil
		}
	}
	if t, err := time.ParseInLocation("15:04", s, now.Location()); err == nil {
		at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		if !at.After(now) {
			at = at.Add(24 * time.Hour)
		}
		return at, nil
	}
	return time.Time{}, fmt.Errorf("cannot read time %q (use a duration like 2h, HH:MM, or 2006-01-02T15:04)", s)
}

// Fire posts a due job; it returns false if the job couldn't be delivered.
type Fire func(Job) bool

// Authorize reports whether the job's creator may still run it.
type Authorize func(Job) bool

type Store struct {
	mu    sync.Mutex
	path  string
	jobs  map[string]*Job
	mtime time.Time
	now   func() time.Time

	Fire      Fire
	Authorize Authorize
	OnEvent   func(Job, string) // audit hook: fired / skipped / disabled / done
}

func Open(dataDir string) (*Store, error) {
	s := &Store{path: filepath.Join(dataDir, "schedules.json"), jobs: map[string]*Job{}, now: time.Now}
	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var list []*Job
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("%s: %w", s.path, err)
	}
	running := map[string]bool{}
	for id, j := range s.jobs {
		running[id] = j.Running
	}
	s.jobs = map[string]*Job{}
	for _, j := range list {
		j.Running = running[j.ID] // in-memory only
		s.jobs[j.ID] = j
	}
	if st, err := os.Stat(s.path); err == nil {
		s.mtime = st.ModTime()
	}
	return nil
}

// reloadIfChanged picks up edits made by the CLI while the bot runs.
func (s *Store) reloadIfChanged() {
	st, err := os.Stat(s.path)
	if err != nil || !st.ModTime().After(s.mtime) {
		return
	}
	if err := s.load(); err != nil {
		slog.Warn("schedule: reload failed", "err", err)
	}
}

func (s *Store) saveLocked() error {
	list := s.listLocked("", "")
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if st, err := os.Stat(s.path); err == nil {
		s.mtime = st.ModTime()
	}
	return nil
}

func newID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return "s" + hex.EncodeToString(b)
}

// Create adds a job (the caller has checked the creator may create it).
func (s *Store) Create(j Job) (Job, error) {
	if strings.TrimSpace(j.Prompt) == "" {
		return Job{}, fmt.Errorf("prompt is empty")
	}
	if !j.CreatedBy.Privileged() {
		return Job{}, fmt.Errorf("permission denied: only the owner or an admin can create schedules")
	}
	now := s.now()
	next, once, err := Next(j.Spec, now)
	if err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadIfChanged()
	j.ID, j.CreatedAt, j.Enabled, j.NextRun, j.Once = newID(), now, true, next, once
	s.jobs[j.ID] = &j
	return j, s.saveLocked()
}

func (s *Store) listLocked(bot, conv string) []Job {
	var out []Job
	for _, j := range s.jobs {
		if (bot == "" || j.Bot == bot) && (conv == "" || j.ConvKey == conv) {
			out = append(out, *j)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.Before(out[b].CreatedAt) })
	return out
}

// List returns jobs, optionally of one bot / conversation.
func (s *Store) List(bot, conv string) []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadIfChanged()
	return s.listLocked(bot, conv)
}

func (s *Store) Get(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadIfChanged()
	j, ok := s.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

func (s *Store) Delete(id string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadIfChanged()
	j, ok := s.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("no schedule %q", id)
	}
	delete(s.jobs, id)
	return *j, s.saveLocked()
}

// SetEnabled pauses or resumes a job (resuming recomputes the next run).
func (s *Store) SetEnabled(id string, on bool) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadIfChanged()
	j, ok := s.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("no schedule %q", id)
	}
	if on && !j.Enabled {
		next, _, err := Next(j.Spec, s.now())
		if err != nil {
			return Job{}, err
		}
		j.NextRun = next
	}
	j.Enabled = on
	return *j, s.saveLocked()
}

// Done marks a fired job's turn as finished (it may fire again).
func (s *Store) Done(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok && j.Running {
		j.Running = false
		if s.OnEvent != nil {
			s.OnEvent(*j, "done")
		}
	}
}

// Tick fires every due job once. Call it periodically.
func (s *Store) Tick() {
	s.mu.Lock()
	s.reloadIfChanged()
	now := s.now()
	var due []*Job
	for _, j := range s.jobs {
		if j.Enabled && !j.NextRun.IsZero() && !j.NextRun.After(now) {
			due = append(due, j)
		}
	}
	type fire struct {
		j     Job
		event string
	}
	var events []fire
	var toFire []*Job
	for _, j := range due {
		switch {
		case s.Authorize != nil && !s.Authorize(*j):
			j.Enabled, j.LastStatus = false, "disabled: creator is no longer owner/admin"
			events = append(events, fire{*j, "disabled"})
		case j.Running:
			j.LastStatus = "skipped_running"
			events = append(events, fire{*j, "skipped"})
		default:
			toFire = append(toFire, j)
		}
		if j.Enabled {
			if j.Once && j.LastStatus != "skipped_running" {
				j.NextRun = time.Time{}
			} else if next, _, err := Next(j.Spec, now); err == nil {
				j.NextRun = next
			}
		}
	}
	for _, j := range toFire {
		j.Running, j.LastRun, j.LastStatus = true, now, "fired"
		j.Runs++
		if j.Once {
			j.Enabled = false
		}
	}
	snapshot := make([]Job, len(toFire))
	for i, j := range toFire {
		snapshot[i] = *j
	}
	if len(due) > 0 {
		_ = s.saveLocked()
	}
	s.mu.Unlock()

	for _, e := range events {
		if s.OnEvent != nil {
			s.OnEvent(e.j, e.event)
		}
	}
	for _, j := range snapshot {
		ok := s.Fire != nil && s.Fire(j)
		if s.OnEvent != nil {
			s.OnEvent(j, map[bool]string{true: "fired", false: "undeliverable"}[ok])
		}
		if !ok {
			s.Done(j.ID)
		}
	}
}

// Run ticks every interval until stop is closed.
func (s *Store) Run(stop <-chan struct{}, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.Tick()
		}
	}
}
