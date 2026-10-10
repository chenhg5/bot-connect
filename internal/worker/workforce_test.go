package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/workforce"
)

type collect struct {
	mu sync.Mutex
	a  map[string]*workforce.Assignment
}

func (c *collect) Emit(id string, ev workforce.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.a[id].Apply(ev)
}

func TestAgentWorkerSpeaksProtocol(t *testing.T) {
	m, err := NewManager([]config.Worker{{Name: "w", Agent: "claudecode", WorkDir: t.TempDir(), Description: "demo"}}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.workers["w"].runner = &sleepRunner{}
	aw := &AgentWorker{M: m, Name: "w", Bot: "b"}
	m.OnFinish = aw.Finished
	a := &workforce.Assignment{ID: "a1", Worker: "w", Brief: workforce.Brief{Goal: "do it", Done: "it is done"}}
	_ = a.Apply(workforce.Event{Type: workforce.EvOffer})
	ev := &collect{a: map[string]*workforce.Assignment{"a1": a}}
	aw.Events = ev
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	if p := aw.Profile(); p.Kind != workforce.KindAgentSession || p.Trust != workforce.Controlled || p.Description != "demo" {
		t.Fatalf("profile %+v", p)
	}
	if err := aw.Offer(ctx, *a); err != nil {
		t.Fatal(err)
	}
	var st workforce.State
	for _, f := range aw.Observe(ctx) {
		st.Put(f)
	}
	if f, _ := st.Get(workforce.Presence, time.Now()); f.Value != "busy" {
		t.Fatalf("presence %v", f)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		ev.mu.Lock()
		s := a.Status
		ev.mu.Unlock()
		if s == workforce.Delivered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status %s, history %+v", s, a.History)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if a.Result != "done" {
		t.Fatalf("result %q", a.Result)
	}
}
