package worker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chenhg5/bot-connect/internal/workforce"
)

// AgentWorker presents a worker of this manager (one agent session) through
// the workforce protocol. Agents we run never decline: an offer is accepted
// and queued at once, and the task's end becomes deliver / fail.
type AgentWorker struct {
	M      *Manager
	Name   string
	Bot    string
	Events workforce.Events

	mu      sync.Mutex
	byTask  map[string]string // task id → assignment id
	byAssig map[string]string // assignment id → task id
}

var _ workforce.Worker = (*AgentWorker)(nil)

func (w *AgentWorker) Profile() workforce.Profile {
	w.M.mu.Lock()
	defer w.M.mu.Unlock()
	p := workforce.Profile{ID: w.Name, Name: w.Name, Kind: workforce.KindAgentSession, Principal: "owner", Trust: workforce.Controlled,
		Interaction: workforce.Interaction{Sessions: true}, Cost: workforce.Cost{Unit: "tokens"}}
	if ws := w.M.workers[w.Name]; ws != nil {
		p.Description = ws.Spec.Description
		p.Skills = []string{ws.Spec.Agent, ws.Spec.Access}
	}
	return p
}

// Observe reports presence and load from the queue, health and track record
// from recent history, and the session it holds.
func (w *AgentWorker) Observe(ctx context.Context) []workforce.Fact {
	now := time.Now()
	w.M.mu.Lock()
	defer w.M.mu.Unlock()
	ws := w.M.workers[w.Name]
	if ws == nil {
		return []workforce.Fact{{Dim: workforce.Presence, Value: "offline", Detail: "retired", Source: workforce.Observed, At: now}}
	}
	open := len(ws.queue)
	if ws.current != nil {
		open++
	}
	presence := "available"
	if open > 0 {
		presence = "busy"
	}
	facts := []workforce.Fact{
		{Dim: workforce.Presence, Value: presence, Source: workforce.Observed, At: now},
		{Dim: workforce.Load, Value: fmt.Sprintf("%d open", open), Num: float64(open), Source: workforce.Observed, At: now},
	}
	var ok, failed int
	var took time.Duration
	for _, t := range ws.History {
		switch t.Status {
		case StatusSucceeded:
			ok++
			took += t.EndedAt.Sub(t.StartedAt)
		case StatusFailed:
			failed++
		}
	}
	if ok+failed > 0 {
		health := "ok"
		if failed*2 >= ok+failed {
			health = "degraded"
		}
		facts = append(facts, workforce.Fact{Dim: workforce.Health, Value: health, Detail: fmt.Sprintf("%d/%d recent tasks failed", failed, ok+failed), Source: workforce.Observed, At: now})
	}
	if ok > 0 {
		facts = append(facts, workforce.Fact{Dim: workforce.Record, Value: fmt.Sprintf("typical %s", (took / time.Duration(ok)).Round(time.Second)),
			Num: (took / time.Duration(ok)).Seconds(), Source: workforce.Observed, At: now})
	}
	if ws.SessionID != "" {
		facts = append(facts, workforce.Fact{Dim: workforce.Context, Value: "session " + ws.SessionID, Source: workforce.Observed, At: now})
	}
	return facts
}

// Offer queues the brief as a task and accepts at once.
func (w *AgentWorker) Offer(ctx context.Context, a workforce.Assignment) error {
	t, _, err := w.M.Delegate(w.Bot, w.Name, a.Brief.Instruction(), a.ConvKey, a.Requester, a.Brief.Priority >= 2)
	if err != nil {
		return err
	}
	w.mu.Lock()
	if w.byTask == nil {
		w.byTask, w.byAssig = map[string]string{}, map[string]string{}
	}
	w.byTask[t.ID], w.byAssig[a.ID] = a.ID, t.ID
	w.mu.Unlock()
	if w.Events != nil {
		now := time.Now()
		_ = w.Events.Emit(a.ID, workforce.Event{Type: workforce.EvAccept, At: now, Note: "queued as " + t.ID})
		_ = w.Events.Emit(a.ID, workforce.Event{Type: workforce.EvStart, At: now})
	}
	return nil
}

// Notify handles cancellation; agents can't be asked mid-task yet.
func (w *AgentWorker) Notify(ctx context.Context, a workforce.Assignment, ev workforce.Event) error {
	w.mu.Lock()
	tid := w.byAssig[a.ID]
	w.mu.Unlock()
	switch ev.Type {
	case workforce.EvCancel:
		if tid == "" {
			return fmt.Errorf("assignment %s has no task", a.ID)
		}
		_, err := w.M.Cancel(tid)
		return err
	case workforce.EvNudge:
		return nil // agents don't need reminders
	}
	return fmt.Errorf("%s workers don't support %s yet", workforce.KindAgentSession, ev.Type)
}

// Finished turns a finished task into deliver / fail events. Call it from
// the manager's OnFinish.
func (w *AgentWorker) Finished(t Task) {
	w.mu.Lock()
	aid := w.byTask[t.ID]
	delete(w.byTask, t.ID)
	delete(w.byAssig, aid)
	w.mu.Unlock()
	if aid == "" || w.Events == nil {
		return
	}
	ev := workforce.Event{At: t.EndedAt}
	switch t.Status {
	case StatusSucceeded:
		ev.Type, ev.Result = workforce.EvDeliver, t.Result
	case StatusCancelled:
		return // the requester cancelled; the assignment already says so
	default:
		ev.Type, ev.Note = workforce.EvFail, t.Error
	}
	_ = w.Events.Emit(aid, ev)
}
