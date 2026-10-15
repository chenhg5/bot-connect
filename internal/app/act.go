package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/inbox"
	"github.com/chenhg5/bot-connect/internal/domain/observe"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// Hands carry out actions outside the bot (a command, a file write, a
// message to an existing agent session).
type Hands interface {
	Do(ctx context.Context, a observe.Action) (string, error)
}

// NeedsApproval: the action is allowed neither once nor by a standing permission.
type NeedsApproval struct{ Action observe.Action }

func (e NeedsApproval) Error() string {
	return "needs the principal's approval: " + e.Action.Describe() +
		" — tell them exactly this, and after they agree, call again with confirmed=true in that turn (or, for something routine, propose a standing permission with act_allow)"
}

// AllowAct grants a standing permission ("write ~/code/projects/*/handoffs/**").
func (a *App) AllowAct(ctx context.Context, by Actor, pattern string) error {
	if by.Role != RolePrincipal {
		return Forbidden("only the principal grants permissions to act")
	}
	pattern = strings.Join(strings.Fields(pattern), " ")
	if err := observe.ValidPermission(pattern); err != nil {
		return err
	}
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		for _, p := range s.Granted {
			if p == pattern {
				return nil, nil
			}
		}
		s.Granted = append(s.Granted, pattern)
		return []Event{NewEvent("act.granted", "act", a.now(), by, "pattern", pattern)}, nil
	})
	return err
}

// RevokeAct removes a standing permission.
func (a *App) RevokeAct(ctx context.Context, by Actor, pattern string) error {
	if by.Role != RolePrincipal {
		return Forbidden("only the principal revokes permissions")
	}
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		var keep []string
		for _, p := range s.Granted {
			if p != strings.Join(strings.Fields(pattern), " ") {
				keep = append(keep, p)
			}
		}
		s.Granted = keep
		return []Event{NewEvent("act.revoked", "act", a.now(), by, "pattern", pattern)}, nil
	})
	return err
}

// Grants are the standing permissions.
func (a *App) Grants() []string {
	var out []string
	a.Store.Read(func(s *State) { out = append(out, s.Granted...) })
	return out
}

// MaxDone bounds the audit trail kept in state (events keep everything).
const MaxDone = 300

// Act carries out an action if the principal allowed it: once (confirmed,
// in the turn where they agreed — the tool checks that) or by a standing
// permission. Handing a message to a session runs in the background; its
// answer comes back as a signal.
//
// A confirmation without a proposal covers one action per turn (what the
// principal asked for in that message). Several actions are proposed first
// (ProposeActs) and carried out exactly as proposed (ActProposal).
func (a *App) Act(ctx context.Context, by Actor, pid ProjectID, act observe.Action, confirmed bool, turn string) (string, error) {
	if err := requirePrivileged(by, "act outside the bot"); err != nil {
		return "", err
	}
	if a.Hands == nil {
		return "", Forbidden("acting is not set up ([observe] roots)")
	}
	if err := act.Validate(); err != nil {
		return "", err
	}
	home, _ := os.UserHomeDir()
	grant := ""
	for _, p := range a.Grants() {
		if observe.Permits(p, act, home) {
			grant = p
			break
		}
	}
	if grant == "" {
		if !confirmed {
			return "", NeedsApproval{Action: act}
		}
		if turn == "" {
			return "", Forbidden("a confirmation belongs to the principal's message that gave it")
		}
		if !a.useOnce(turn) {
			return "", Conflict("this turn's confirmation was already used for one action; to do more, propose them all (act kind=propose), show the principal the list, and carry it out after they agree")
		}
		grant = "once"
	}
	return a.do(ctx, by, pid, act, grant)
}

func (a *App) do(ctx context.Context, by Actor, pid ProjectID, act observe.Action, grant string) (string, error) {
	act, err := a.resolveAction(ctx, act)
	if err != nil {
		return "", err
	}
	if act.Kind == observe.ActSession {
		go func() {
			out, err := a.Hands.Do(context.Background(), act)
			a.recordDone(context.Background(), by, pid, act, grant, out, err)
			body := fmt.Sprintf("%s 的结果：\n%s", act.Describe(), clipRunes(out, 4000))
			if err != nil {
				body = fmt.Sprintf("%s 失败：%v\n%s", act.Describe(), err, clipRunes(out, 2000))
			}
			_, _, _ = a.Ingest(context.Background(), SignalSpec{Dedupe: fmt.Sprintf("act:%s:%s:%d", act.Harness, act.Session, a.now().UnixNano()),
				Source: inbox.WorkerReply, Reason: "act_result", Actor: System("act"), ActorName: act.Harness + " " + act.Session,
				Summary: clipRunes(act.Describe(), 80), Body: body, Refs: inbox.Refs{Project: pid}})
		}()
		return fmt.Sprintf("已递给 %s 会话 %s（%s）；它的回复会作为信号回来。", act.Harness, act.Session, grant), nil
	}
	out, err := a.Hands.Do(ctx, act)
	a.recordDone(ctx, by, pid, act, grant, out, err)
	return out, err
}

func (a *App) recordDone(ctx context.Context, by Actor, pid ProjectID, act observe.Action, grant, out string, err error) {
	d := observe.Done{At: a.now(), Project: pid, Action: act, By: by, Grant: grant, Result: clipRunes(out, 500)}
	if len(d.Action.Content) > 500 {
		d.Action.Content = clipRunes(d.Action.Content, 500)
	}
	if err != nil {
		d.Err = err.Error()
	}
	_, _ = a.commit(ctx, func(s *State) ([]Event, error) {
		s.Done = append(s.Done, d)
		if len(s.Done) > MaxDone {
			s.Done = s.Done[len(s.Done)-MaxDone:]
		}
		return []Event{NewEvent("act.done", "act", d.At, by, "project", string(pid), "action", act.Describe(), "grant", grant, "err", d.Err)}, nil
	})
}

// RecentActs lists the latest actions (of a project, or all).
func (a *App) RecentActs(pid ProjectID, n int) []observe.Done {
	var out []observe.Done
	a.Store.Read(func(s *State) {
		for i := len(s.Done) - 1; i >= 0 && len(out) < n; i-- {
			if pid == "" || s.Done[i].Project == pid {
				out = append(out, s.Done[i])
			}
		}
	})
	return out
}

// useOnce spends a turn's single confirmation.
func (a *App) useOnce(turn string) bool {
	a.onceMu.Lock()
	defer a.onceMu.Unlock()
	if a.onceUsed == nil {
		a.onceUsed = map[string]bool{}
	}
	if a.onceUsed[turn] {
		return false
	}
	a.onceUsed[turn] = true
	return true
}

// ActProposal is a set of actions put to the principal, carried out exactly
// as written once they agree.
type ActProposal struct {
	ID        string           `json:"id"`
	Project   ProjectID        `json:"project,omitempty"`
	Actions   []observe.Action `json:"actions"`
	By        Actor            `json:"by"`
	CreatedAt time.Time        `json:"created_at"`
	Status    string           `json:"status"` // pending | done | dropped
}

// Describe lists the proposal as the principal approves it.
func (p ActProposal) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s（待委托人确认）：\n", p.ID)
	for i, x := range p.Actions {
		fmt.Fprintf(&b, "%d. %s\n", i+1, x.Describe())
		if x.Kind == observe.ActWrite {
			b.WriteString("   内容：" + clipRunes(oneLine(x.Content, 300), 300) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ProposeActs records actions to put to the principal.
func (a *App) ProposeActs(ctx context.Context, by Actor, pid ProjectID, acts []observe.Action) (ActProposal, error) {
	if err := requirePrivileged(by, "propose actions"); err != nil {
		return ActProposal{}, err
	}
	if len(acts) == 0 || len(acts) > 20 {
		return ActProposal{}, Invalid("propose 1–20 actions")
	}
	for i, x := range acts {
		if err := x.Validate(); err != nil {
			return ActProposal{}, fmt.Errorf("action %d: %w", i+1, err)
		}
	}
	var out ActProposal
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		out = ActProposal{ID: s.NextID("AP"), Project: pid, Actions: acts, By: by, CreatedAt: a.now(), Status: "pending"}
		s.Proposed[out.ID] = out
		return []Event{NewEvent("act.proposed", "act", a.now(), by, "proposal", out.ID, "actions", len(acts))}, nil
	})
	return out, err
}

// ActProposal carries out a pending proposal, exactly as proposed, after
// the principal agreed in this turn.
func (a *App) CarryOut(ctx context.Context, by Actor, id string) ([]string, error) {
	if err := requirePrivileged(by, "act outside the bot"); err != nil {
		return nil, err
	}
	var p ActProposal
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		x, ok := s.Proposed[id]
		if !ok {
			return nil, NotFound("no proposal %s", id)
		}
		if x.Status != "pending" {
			return nil, Conflict("proposal %s is %s", id, x.Status)
		}
		x.Status = "done"
		s.Proposed[id] = x
		p = x
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	var out []string
	for i, x := range p.Actions {
		res, err := a.do(ctx, by, p.Project, x, "proposal "+p.ID)
		if err != nil {
			out = append(out, fmt.Sprintf("%d. %s ✗ %v", i+1, x.Describe(), err))
			continue
		}
		out = append(out, fmt.Sprintf("%d. %s ✓ %s", i+1, x.Describe(), clipRunes(res, 200)))
	}
	return out, nil
}
