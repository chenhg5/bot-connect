package worker

import (
	"context"
	"github.com/chenhg5/bot-connect/internal/identity"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/config"
)

type sleepRunner struct {
	mu   sync.Mutex
	runs []string
}

func (r *sleepRunner) Run(ctx context.Context, spec config.Worker, sid, prompt string) (string, string, error) {
	r.mu.Lock()
	r.runs = append(r.runs, prompt)
	r.mu.Unlock()
	select {
	case <-time.After(150 * time.Millisecond):
	case <-ctx.Done():
		return "", "sess", ctx.Err()
	}
	return "done", "sess", nil
}

func TestBusyWorkerQueuesAndResumes(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager([]config.Worker{{Name: "w", Agent: "claudecode", WorkDir: dir}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &sleepRunner{}
	m.workers["w"].runner = r
	var mu sync.Mutex
	var finished []Task
	m.OnFinish = func(t Task) { mu.Lock(); finished = append(finished, t); mu.Unlock() }
	m.Start(context.Background())

	t1, a1, _ := m.Delegate("b", "w", "first", "c", Requester{ID: "me", Role: identity.RoleOwner}, false)
	time.Sleep(30 * time.Millisecond)
	t2, a2, _ := m.Delegate("b", "w", "second", "c", Requester{ID: "me", Role: identity.RoleOwner}, false)
	t3, a3, _ := m.Delegate("b", "w", "third", "c", Requester{ID: "me", Role: identity.RoleOwner}, true) // urgent: ahead of t2
	if a1 != 0 || a2 != 1 || a3 != 1 {
		t.Fatalf("ahead = %d %d %d", a1, a2, a3)
	}
	if _, err := m.Cancel(t2.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(finished) != 2 || finished[0].ID != t1.ID || finished[1].ID != t3.ID {
		t.Fatalf("finished = %+v", finished)
	}
	if m.workers["w"].SessionID != "sess" {
		t.Fatal("session id not kept for resume")
	}
	if got, _ := m.Task(t2.ID); got.Status != StatusCancelled {
		t.Fatalf("t2 status %s", got.Status)
	}
}
