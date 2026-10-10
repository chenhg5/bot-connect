// Package world is a read-only snapshot of the domain at one instant,
// assembled by the application layer from the repositories. Rules,
// forecasts and staffing read it; none of them do I/O.
package world

import (
	"sort"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
)

type World struct {
	Now         time.Time
	Projects    map[ProjectID]portfolio.Project
	Items       map[ItemID]planning.Item
	Assignments map[AssignmentID]delegation.Assignment
	Workers     map[WorkerID]workforce.Worker
	States      map[WorkerID]workforce.WorkerState // declared + inferred + freshly observed
}

// New returns an empty world at now.
func New(now time.Time) *World {
	return &World{Now: now, Projects: map[ProjectID]portfolio.Project{}, Items: map[ItemID]planning.Item{},
		Assignments: map[AssignmentID]delegation.Assignment{}, Workers: map[WorkerID]workforce.Worker{},
		States: map[WorkerID]workforce.WorkerState{}}
}

func (w *World) Chain(p ProjectID) portfolio.Chain {
	return portfolio.ChainOf(p, func(id ProjectID) (portfolio.Project, bool) {
		x, ok := w.Projects[id]
		return x, ok
	})
}

func (w *World) Graph() planning.Graph { return planning.Graph{Items: w.Items} }

// ProjectPriority of an item (its own priority, else its project's).
func (w *World) Priority(it planning.Item) Priority {
	p, ok := w.Projects[it.Project]
	if !ok {
		return P2
	}
	return it.EffectivePriority(p.Priority)
}

// OpenAssignments of a worker, across all projects.
func (w *World) OpenAssignments(worker WorkerID) []delegation.Assignment {
	var out []delegation.Assignment
	for _, a := range w.Assignments {
		if a.Worker == worker && a.Open() {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// State of a worker (zero value if unknown).
func (w *World) State(id WorkerID) workforce.WorkerState {
	if s, ok := w.States[id]; ok {
		return s
	}
	return workforce.WorkerState{Worker: id}
}

// ItemsOf a project, open ones only unless all.
func (w *World) ItemsOf(p ProjectID, all bool) []planning.Item {
	var out []planning.Item
	for _, it := range w.Items {
		if it.Project == p && (all || it.Open()) {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (w *World) IsWorker(id WorkerID) bool { _, ok := w.Workers[id]; return ok }
