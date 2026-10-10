// Package agentsession is the worker driver for coding-agent sessions run by
// the worker manager (Claude Code / Codex, configured or from templates).
// An offer becomes a queued task; the task's end becomes deliver / fail; a
// revision or an answer to a question becomes another task in the same
// session.
package agentsession

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/worker"
)

type Driver struct {
	M   *worker.Manager
	App *app.App
	Bot string
}

var _ app.WorkerDriver = (*Driver)(nil)

func actor() Actor { return System("driver:agent_session") }

func requester(a delegation.Assignment) identity.User {
	return identity.User{ID: firstNonEmpty(a.Requester.UserID, "bot-connect"), Role: identity.RoleOwner, Name: "bot-connect"}
}

func (d *Driver) Observe(ctx context.Context, w workforce.Worker, open []delegation.Assignment) []workforce.Fact {
	now := time.Now()
	for _, wi := range d.M.Snapshot() {
		if wi.Name != string(w.ID) {
			continue
		}
		load := wi.Queued
		presence := "available"
		if wi.Running != "" {
			load++
		}
		if load > 0 {
			presence = "busy"
		}
		facts := []workforce.Fact{
			{Dim: workforce.Presence, Value: presence, At: now},
			{Dim: workforce.Load, Value: fmt.Sprintf("%d queued", load), Num: float64(load), At: now},
		}
		if wi.Session != "" {
			facts = append(facts, workforce.Fact{Dim: workforce.Context, Value: "session " + short(wi.Session), At: now})
		}
		return facts
	}
	return []workforce.Fact{{Dim: workforce.Presence, Value: "offline", Detail: "no such agent session", At: now}}
}

func (d *Driver) Offer(ctx context.Context, w workforce.Worker, a delegation.Assignment) error {
	t, _, err := d.M.Delegate(d.Bot, string(w.ID), Instruction(a), a.Conv, requester(a), a.Brief.Priority == P0)
	if err != nil {
		return err
	}
	return d.App.Report(ctx, actor(), a.ID, app.Report{Action: "accept", Note: "queued as " + t.ID, Ref: t.ID})
}

func (d *Driver) Notify(ctx context.Context, w workforce.Worker, a delegation.Assignment, n app.Notice) error {
	switch n.Kind {
	case "cancelled":
		if a.Ref != "" {
			_, err := d.M.Cancel(a.Ref)
			return err
		}
	case "revise", "answer":
		lead := map[string]string{"revise": "验收没通过，请继续修改：", "answer": "你之前的问题有回复了："}[n.Kind]
		t, _, err := d.M.Delegate(d.Bot, string(w.ID), fmt.Sprintf("[bot-connect 委托 %s 续]\n%s\n%s\n\n原完成标准：\n%s", a.ID, lead, n.Text,
			strings.Join(a.Brief.Done, "\n")), a.Conv, requester(a), false)
		if err != nil {
			return err
		}
		return d.App.Report(ctx, actor(), a.ID, app.Report{Action: "progress", Note: "continuing as " + t.ID, Ref: t.ID})
	}
	return nil
}

// Finished routes a finished agent task to its assignment; it reports
// whether the task belonged to one (then no plain task report is needed).
func (d *Driver) Finished(ctx context.Context, t worker.Task) bool {
	var id AssignmentID
	d.App.Store.Read(func(s *app.State) {
		for aid, as := range s.Assignments {
			if as.Ref == t.ID && as.Open() {
				id = aid
			}
		}
	})
	if id == "" {
		return false
	}
	var err error
	switch t.Status {
	case worker.StatusSucceeded:
		err = d.App.Report(ctx, actor(), id, app.Report{Action: "deliver", Result: t.Result})
	case worker.StatusCancelled:
		return true
	default:
		err = d.App.Report(ctx, actor(), id, app.Report{Action: "fail", Note: firstNonEmpty(t.Error, string(t.Status))})
	}
	if err != nil && d.App.Log != nil {
		d.App.Log.Warn("agent task report failed", "task", t.ID, "assignment", id, "err", err)
	}
	return true
}

// Instruction renders a brief for an agent: self-contained, with the
// definition of done and what evidence to return.
func Instruction(a delegation.Assignment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[bot-connect 委托 %s]\n\n%s\n", a.ID, a.Brief.Goal)
	if a.Brief.Context != "" {
		b.WriteString("\n背景：" + a.Brief.Context + "\n")
	}
	if len(a.Brief.Done) > 0 {
		b.WriteString("\n完成标准：\n")
		for _, d := range a.Brief.Done {
			b.WriteString("- " + d + "\n")
		}
	}
	if a.Brief.Evidence != "" {
		b.WriteString("\n交付时附上：" + a.Brief.Evidence + "\n")
	}
	if a.Brief.Due != nil {
		b.WriteString("\n截止：" + a.Brief.Due.Format("2006-01-02 15:04") + "\n")
	}
	b.WriteString("\n完成后，最后一条消息用简洁的中文总结：做了什么、结果、证据（链接 / 提交 / 数据），以及有没有需要人来决定或批准的事。" +
		"如果缺权限、缺信息或需要人做决定，直接说明缺什么，不要猜。")
	return b.String()
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func firstNonEmpty(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}
