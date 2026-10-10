// Package jsonstore keeps the application state in one JSON file (or only in
// memory) with an append-only event log next to it. Transactions are
// serialized by a mutex and work on a deep copy, so a failed command leaves
// nothing behind.
package jsonstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/shared"
)

type Store struct {
	mu    sync.Mutex
	path  string // "" = memory only
	state *app.State
}

var _ app.Store = (*Store)(nil)

// Memory returns a store that keeps nothing on disk.
func Memory() *Store { return &Store{state: app.NewState()} }

// Open loads (or starts) the state at path (e.g. <data_dir>/state.json);
// events go to <path without .json>.events.jsonl.
func Open(path string) (*Store, error) {
	s := &Store{path: path, state: app.NewState()}
	b, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, s.state); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		s.state.Fill()
	}
	return s, nil
}

func clone(st *app.State) (*app.State, error) {
	b, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	out := app.NewState()
	if err := json.Unmarshal(b, out); err != nil {
		return nil, err
	}
	out.Fill()
	return out, nil
}

func (s *Store) Tx(ctx context.Context, fn func(*app.State) ([]shared.Event, error)) ([]shared.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	work, err := clone(s.state)
	if err != nil {
		return nil, err
	}
	evs, err := fn(work)
	if err != nil {
		return nil, err
	}
	if s.path != "" {
		if err := s.persist(work, evs); err != nil {
			return nil, err
		}
	}
	s.state = work
	return evs, nil
}

func (s *Store) Read(fn func(*app.State)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.state)
}

func (s *Store) persist(st *app.State, evs []shared.Event) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	if len(evs) > 0 {
		f, err := os.OpenFile(s.eventsPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(f)
		for _, e := range evs {
			if err := enc.Encode(e); err != nil {
				f.Close()
				return err
			}
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) eventsPath() string {
	p := s.path
	if filepath.Ext(p) == ".json" {
		p = p[:len(p)-len(".json")]
	}
	return p + ".events.jsonl"
}
