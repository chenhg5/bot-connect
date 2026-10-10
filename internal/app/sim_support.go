package app

import (
	"context"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/inbox"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
)

// WorkerKind returns a worker's kind ("" if unknown).
func (a *App) WorkerKind(id WorkerID) workforce.Kind {
	var k workforce.Kind
	a.Store.Read(func(s *State) { k = s.Workers[id].Kind })
	return k
}

// Backdate moves the start of work on an item (and its open assignment)
// back in time. Simulations use it to set up work already in progress.
func (a *App) Backdate(id ItemID, at time.Time) {
	_, _ = a.Store.Tx(context.Background(), func(s *State) ([]Event, error) {
		it, ok := s.Items[id]
		if !ok {
			return nil, nil
		}
		it.StartedAt, it.LastSignal = at, at
		s.Items[id] = it
		if as, ok := s.Assignments[it.Assignment]; ok {
			as.LastLife, as.CreatedAt = at, at
			for i := range as.History {
				if as.History[i].At.After(at) {
					as.History[i].At = at
				}
			}
			s.Assignments[as.ID] = as
		}
		return nil, nil
	})
}

// ClearInbox closes every open signal (simulations: setup noise is not part of a case).
func (a *App) ClearInbox() {
	_, _ = a.Store.Tx(context.Background(), func(s *State) ([]Event, error) {
		for id, x := range s.Signals {
			if x.Status.Open() {
				x.Status, x.Note = inbox.Handled, "setup"
				s.Signals[id] = x
			}
		}
		return nil, nil
	})
}
