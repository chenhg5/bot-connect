package app

import (
	"context"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/attention"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
)

// requirePrivileged: owner, admin, or the scaffold itself.
func requirePrivileged(by Actor, what string) error {
	if by.Privileged() || by.Role == RoleSystem {
		return nil
	}
	return Forbidden("only the owner or an admin can %s", what)
}

// ---- workers ----

// UpsertWorker registers or replaces a worker profile (config sync, setup).
func (a *App) UpsertWorker(ctx context.Context, by Actor, w workforce.Worker) error {
	if err := requirePrivileged(by, "manage workers"); err != nil {
		return err
	}
	if err := w.Validate(); err != nil {
		return err
	}
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		if old, ok := s.Workers[w.ID]; ok {
			w.Version = old.Version + 1
		}
		s.Workers[w.ID] = w
		return []Event{NewEvent("worker.upserted", "worker:"+string(w.ID), a.now(), by, "kind", string(w.Kind))}, nil
	})
	return err
}

// WorkerFor finds the worker a platform identity belongs to.
func (a *App) WorkerFor(ids ...string) (WorkerID, bool) {
	var found WorkerID
	a.Store.Read(func(s *State) {
		for id, w := range s.Workers {
			if w.Is(ids...) {
				found = id
				return
			}
		}
	})
	return found, found != ""
}

// ResolveWorker finds a worker by id or by name (exact, then unique partial).
func (a *App) ResolveWorker(s string) (WorkerID, error) {
	s = strings.TrimSpace(s)
	if s == "me" || s == "主人" || s == "我" {
		return Owner, nil
	}
	var exact, partial []WorkerID
	a.Store.Read(func(st *State) {
		if _, ok := st.Workers[WorkerID(s)]; ok {
			exact = []WorkerID{WorkerID(s)}
			return
		}
		for id, w := range st.Workers {
			switch {
			case strings.EqualFold(w.Name, s) || strings.EqualFold(string(id), s):
				exact = append(exact, id)
			case s != "" && (strings.Contains(w.Name, s) || strings.Contains(s, w.Name) && w.Name != ""):
				partial = append(partial, id)
			}
		}
	})
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) == 0 && len(partial) == 1:
		return partial[0], nil
	case len(exact)+len(partial) > 1:
		return "", Invalid("%q matches several workers; use the id", s)
	}
	return "", NotFound("no worker %q (see the roster in the brief)", s)
}

// NoteWorker records a fact about a worker: declared when the worker says it
// about themselves, otherwise inferred (expires within a day).
func (a *App) NoteWorker(ctx context.Context, by Actor, id WorkerID, f workforce.Fact) error {
	if by.Worker != id {
		if err := requirePrivileged(by, "note facts about workers"); err != nil {
			return err
		}
		f.Source = workforce.Inferred
	} else {
		f.Source = workforce.Declared
	}
	f.At = a.now()
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		if _, ok := s.Workers[id]; !ok {
			return nil, NotFound("no worker %q", id)
		}
		st := s.States[id]
		st.Worker = id
		st.Prune(f.At)
		evs := st.Record(f)
		s.States[id] = st
		return evs, nil
	})
	return err
}

// ---- projects ----

type ProjectSpec struct {
	ID        ProjectID
	Parent    ProjectID
	Title     string
	Objective portfolio.Objective
	Priority  Priority
	Timebox   Period
	Home      string
}

// EnsureOrg creates the root project if it doesn't exist.
func (a *App) EnsureOrg(ctx context.Context, title string) error {
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		if _, ok := s.Projects[OrgProject]; ok {
			return nil, nil
		}
		p, evs, err := portfolio.New(OrgProject, "", title, P2, Period{}, a.now(), System("setup"))
		if err != nil {
			return nil, err
		}
		s.Projects[OrgProject] = p
		return evs, nil
	})
	return err
}

func (a *App) CreateProject(ctx context.Context, by Actor, sp ProjectSpec) (portfolio.Project, error) {
	if err := requirePrivileged(by, "create projects"); err != nil {
		return portfolio.Project{}, err
	}
	var out portfolio.Project
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		id := sp.ID
		if id == "" {
			id = ProjectID(s.NextID("P"))
		}
		if _, ok := s.Projects[id]; ok {
			return nil, Conflict("project %s already exists", id)
		}
		parent := sp.Parent
		if parent == "" && id != OrgProject {
			parent = OrgProject
		}
		if parent != "" {
			if _, ok := s.Projects[parent]; !ok {
				return nil, NotFound("no parent project %s", parent)
			}
		}
		p, evs, err := portfolio.New(id, parent, sp.Title, sp.Priority, sp.Timebox, a.now(), by)
		if err != nil {
			return nil, err
		}
		p.Objective, p.Home = sp.Objective, sp.Home
		s.Projects[id] = p
		out = p
		return evs, nil
	})
	return out, err
}

// ChangeProject applies a command method to a project (members, policies,
// priority, card, cadence, status).
func (a *App) ChangeProject(ctx context.Context, by Actor, id ProjectID, f func(p *portfolio.Project, now time.Time) ([]Event, error)) (portfolio.Project, error) {
	if err := requirePrivileged(by, "change projects"); err != nil {
		return portfolio.Project{}, err
	}
	var out portfolio.Project
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		p, ok := s.Projects[id]
		if !ok {
			return nil, NotFound("no project %s", id)
		}
		evs, err := f(&p, a.now())
		if err != nil {
			return nil, err
		}
		for _, m := range p.Members {
			if _, ok := s.Workers[m.Worker]; !ok {
				return nil, NotFound("no worker %s", m.Worker)
			}
		}
		p.Version++
		s.Projects[id] = p
		out = p
		return evs, nil
	})
	return out, err
}

// ---- items ----

// PlanItem creates an item.
func (a *App) PlanItem(ctx context.Context, by Actor, sp planning.Spec) (planning.Item, error) {
	if err := requirePrivileged(by, "plan work"); err != nil {
		return planning.Item{}, err
	}
	var out planning.Item
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		p, ok := s.Projects[sp.Project]
		if !ok {
			return nil, NotFound("no project %s", sp.Project)
		}
		if !p.Open() {
			return nil, Conflict("project %s is %s", p.ID, p.Status)
		}
		id := ItemID(s.NextID("I"))
		if err := (planning.Graph{Items: s.Items}).Validate(id, sp.Project, sp.Parent, sp.DependsOn); err != nil {
			return nil, err
		}
		it, evs, err := planning.New(id, sp, a.now(), by)
		if err != nil {
			return nil, err
		}
		s.Items[id] = it
		out = it
		return evs, nil
	})
	return out, err
}

// RescheduleItem moves a due date; an open assignment follows and its worker is told.
func (a *App) RescheduleItem(ctx context.Context, by Actor, id ItemID, due *time.Time) error {
	if err := requirePrivileged(by, "reschedule work"); err != nil {
		return err
	}
	var notify *delegation.Assignment
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		it, ok := s.Items[id]
		if !ok {
			return nil, NotFound("no item %s", id)
		}
		evs, err := it.Reschedule(due, a.now(), by)
		if err != nil {
			return nil, err
		}
		s.Items[id] = it
		if as, ok := s.Assignments[it.Assignment]; ok && as.Open() {
			e2, err := as.Redate(due, a.now(), by)
			if err != nil {
				return nil, err
			}
			s.Assignments[as.ID] = as
			evs = append(evs, e2...)
			notify = &as
		}
		return evs, nil
	})
	if err == nil && notify != nil {
		text := "截止时间改为 " + fmtDue(due)
		a.notify(ctx, *notify, Notice{Kind: "redated", Text: text})
	}
	return err
}

// DropItem drops an item; an open assignment is cancelled.
func (a *App) DropItem(ctx context.Context, by Actor, id ItemID, reason string) error {
	if err := requirePrivileged(by, "drop work"); err != nil {
		return err
	}
	var cancelled *delegation.Assignment
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		it, ok := s.Items[id]
		if !ok {
			return nil, NotFound("no item %s", id)
		}
		var evs []Event
		if as, ok := s.Assignments[it.Assignment]; ok && as.Open() {
			e, err := as.Cancel(reason, a.now(), by)
			if err != nil {
				return nil, err
			}
			s.Assignments[as.ID] = as
			evs = append(evs, e...)
			cancelled = &as
		}
		e, err := it.Drop(reason, a.now(), by)
		if err != nil {
			return nil, err
		}
		s.Items[id] = it
		return append(evs, e...), nil
	})
	if err == nil && cancelled != nil {
		a.notify(ctx, *cancelled, Notice{Kind: "cancelled", Text: reason})
	}
	return err
}

// CompleteItem: the owner decides an item is done without a delivery.
func (a *App) CompleteItem(ctx context.Context, by Actor, id ItemID) error {
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		it, ok := s.Items[id]
		if !ok {
			return nil, NotFound("no item %s", id)
		}
		evs, err := it.Complete(false, a.now(), by)
		if err != nil {
			return nil, err
		}
		s.Items[id] = it
		return evs, nil
	})
	return err
}

// ---- attention ----

// Resolve records how a signal was handled.
func (a *App) Resolve(ctx context.Context, by Actor, key string, o attention.Outcome, until *time.Time, note string) error {
	if err := requirePrivileged(by, "handle the agenda"); err != nil {
		return err
	}
	c := attention.NewContext(a.World(ctx))
	var sig *attention.Signal
	for _, x := range a.Attention.Signals(c) {
		if x.Key == key {
			x := x
			sig = &x
		}
	}
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		target := attention.Signal{Key: key, Level: attention.Low}
		if sig != nil {
			target = *sig
		} else if r, ok := s.Raised[key]; ok {
			target.Level = r.Level
		} else if !strings.Contains(key, ":") {
			return nil, NotFound("no signal %q", key)
		}
		r, evs, err := attention.Resolve(target, o, until, note, a.now(), by)
		if err != nil {
			return nil, err
		}
		s.Resolutions[key] = r
		return evs, nil
	})
	return err
}

func fmtDue(t *time.Time) string {
	if t == nil {
		return "未定"
	}
	return t.Format("01-02 15:04")
}
