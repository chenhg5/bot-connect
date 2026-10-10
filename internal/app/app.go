package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/attention"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/insight"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
	"github.com/chenhg5/bot-connect/internal/domain/world"
)

// App wires the domain to its ports.
type App struct {
	Store     Store
	Clock     Clock
	Drivers   map[string]WorkerDriver // by Worker.Driver, falling back to string(Worker.Kind)
	Waker     Waker
	Sink      EventSink
	Attention attention.Service
	Staffing  insight.Staffing
	Policy    WakePolicy
	Triage    Triage // optional L1 judgment on incoming signals
	Log       *slog.Logger
}

// New returns an app with the default rules.
func New(store Store, clock Clock) *App {
	if clock == nil {
		clock = RealClock{}
	}
	return &App{Store: store, Clock: clock, Drivers: map[string]WorkerDriver{}, Attention: attention.Default(),
		Staffing: insight.DefaultStaffing(), Policy: DefaultWakePolicy(), Log: slog.Default()}
}

// RegisterDriver makes a driver available under a key (a worker kind, or a
// Worker.Driver value).
func (a *App) RegisterDriver(key string, d WorkerDriver) { a.Drivers[key] = d }

func (a *App) driverFor(w workforce.Worker) WorkerDriver {
	if w.Driver != "" {
		if d := a.Drivers[w.Driver]; d != nil {
			return d
		}
	}
	return a.Drivers[string(w.Kind)]
}

func (a *App) now() time.Time { return a.Clock.Now() }

// World assembles a snapshot: stored state plus what drivers observe now.
func (a *App) World(ctx context.Context) *world.World {
	w := world.New(a.Clock.Now())
	a.Store.Read(func(s *State) {
		for k, v := range s.Projects {
			w.Projects[k] = v
		}
		for k, v := range s.Items {
			w.Items[k] = v
		}
		for k, v := range s.Assignments {
			w.Assignments[k] = v
		}
		for k, v := range s.Workers {
			w.Workers[k] = v
		}
		for k, v := range s.States {
			w.States[k] = workforce.WorkerState{Worker: v.Worker, Facts: append([]workforce.Fact(nil), v.Facts...)}
		}
	})
	for id, wk := range w.Workers {
		d := a.driverFor(wk)
		if d == nil {
			continue
		}
		var open []delegation.Assignment
		for _, as := range w.Assignments {
			if as.Worker == id && as.Open() {
				open = append(open, as)
			}
		}
		st := w.State(id).Merge(d.Observe(ctx, wk, open))
		st.Prune(w.Now)
		w.States[id] = st
	}
	return w
}

// Agenda is what the rules find right now, scored and tiered (the
// findings; what the brain must act on is in the inbox).
func (a *App) Agenda(ctx context.Context) (attention.Agenda, *attention.Context) {
	c := attention.NewContext(a.World(ctx))
	return a.Attention.Build(c, nil), c
}

// Progress of a project.
func (a *App) Progress(ctx context.Context, p ProjectID) insight.Progress {
	c := attention.NewContext(a.World(ctx))
	return insight.ProjectProgress(c.World, p, c.Forecast, attention.RiskCount(a.Attention.Signals(c), p))
}

// Candidates for a need.
func (a *App) Candidates(ctx context.Context, n insight.Need) []insight.Candidate {
	return a.Staffing.Candidates(n, a.World(ctx))
}

// commit runs a transaction and publishes its events.
func (a *App) commit(ctx context.Context, fn func(s *State) ([]Event, error)) ([]Event, error) {
	evs, err := a.Store.Tx(ctx, fn)
	if err != nil {
		return nil, err
	}
	if a.Sink != nil && len(evs) > 0 {
		a.Sink.Publish(ctx, evs)
	}
	return evs, nil
}
