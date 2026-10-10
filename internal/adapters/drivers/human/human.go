// Package human is the worker driver for people: offers, answers and
// reminders go out as chat messages along the person's contact routes,
// within their hours; their replies come back through chat (the brain
// records them with the worker-side tools).
package human

import (
	"context"
	"fmt"
	"strings"

	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
)

// Reacher delivers a text along one kind of route.
type Reacher interface {
	Send(ctx context.Context, address, text string, u workforce.Urgency) (messageID string, err error)
}

// ReacherFunc adapts a function.
type ReacherFunc func(ctx context.Context, address, text string, u workforce.Urgency) (string, error)

func (f ReacherFunc) Send(ctx context.Context, address, text string, u workforce.Urgency) (string, error) {
	return f(ctx, address, text, u)
}

// Route kinds this package knows by name; others can be registered.
const (
	FeishuDM  workforce.RouteKind = "feishu_dm"
	OwnerChat workforce.RouteKind = "owner_chat"
)

type Driver struct {
	Clock    Clock
	Reachers map[workforce.RouteKind]Reacher
	// Approved reports whether the owner approved using an approval-gated
	// route (phone calls…) for this message; nil = never.
	Approved func(w workforce.Worker, r workforce.Route, text string) bool
}

var _ app.WorkerDriver = (*Driver)(nil)

func (d *Driver) Observe(ctx context.Context, w workforce.Worker, open []delegation.Assignment) []workforce.Fact {
	now := d.Clock.Now()
	presence := "available"
	if err := w.Norms.Available(workforce.Normal, now); err != nil {
		presence = "off_hours"
		if strings.Contains(err.Error(), "quiet") {
			presence = "dnd"
		}
	}
	return []workforce.Fact{
		{Dim: workforce.Presence, Value: presence, At: now},
		{Dim: workforce.Load, Value: fmt.Sprintf("%d open", len(open)), Num: float64(len(open)), At: now},
	}
}

func (d *Driver) Offer(ctx context.Context, w workforce.Worker, a delegation.Assignment) error {
	u := workforce.Normal
	if a.Brief.Priority == P0 {
		u = workforce.Urgent
	}
	return d.send(ctx, w, OfferText(w, a), u)
}

func (d *Driver) Notify(ctx context.Context, w workforce.Worker, a delegation.Assignment, n app.Notice) error {
	var text string
	switch n.Kind {
	case "answer":
		text = fmt.Sprintf("关于 %s，你的问题有回复了：\n%s", a.ID, n.Text)
	case "verified":
		text = fmt.Sprintf("✅ %s 已验收，谢谢！%s", a.ID, n.Text)
	case "revise":
		text = fmt.Sprintf("关于 %s，需要再改一下：\n%s", a.ID, n.Text)
	case "cancelled":
		text = fmt.Sprintf("%s 不用做了，已取消。%s", a.ID, n.Text)
	case "redated", "counter_accepted":
		text = fmt.Sprintf("%s：%s", a.ID, n.Text)
	case "message":
		text = n.Text
	case "nudge":
		text = fmt.Sprintf("⏰ 提醒 %s：%s\n%s", a.ID, n.Text, oneLine(a.Brief.Goal, 120))
	default:
		return nil
	}
	return d.send(ctx, w, text, n.Urgency)
}

// send goes up the person's routes for this urgency (most escalated usable
// route first), falling back down on failure.
func (d *Driver) send(ctx context.Context, w workforce.Worker, text string, u workforce.Urgency) error {
	routes, err := w.Plan(u, d.Clock.Now())
	if err != nil {
		return err
	}
	var errs []string
	for i := len(routes) - 1; i >= 0; i-- {
		r := routes[i]
		if r.Approval && (d.Approved == nil || !d.Approved(w, r, text)) {
			errs = append(errs, string(r.Kind)+": needs the owner's approval")
			continue
		}
		rc := d.Reachers[r.Kind]
		if rc == nil {
			errs = append(errs, string(r.Kind)+": not available")
			continue
		}
		if _, err := rc.Send(ctx, r.Address, text, u); err != nil {
			errs = append(errs, string(r.Kind)+": "+err.Error())
			continue
		}
		return nil
	}
	return fmt.Errorf("could not reach %s: %s", w.ID, strings.Join(errs, "; "))
}

// OfferText is the request a person receives, shaped by what is asked.
func OfferText(w workforce.Worker, a delegation.Assignment) string {
	var b strings.Builder
	switch a.Kind {
	case delegation.Approval:
		fmt.Fprintf(&b, "🔏 **需要你批准 %s**：%s\n", a.ID, a.Brief.Goal)
	case delegation.Decision:
		fmt.Fprintf(&b, "🤔 **需要你决定 %s**：%s\n", a.ID, a.Brief.Goal)
	case delegation.Clarification:
		fmt.Fprintf(&b, "❓ **想跟你确认一下 %s**：%s\n", a.ID, a.Brief.Goal)
	case delegation.Review:
		fmt.Fprintf(&b, "✅ **请帮忙确认 %s**：%s\n", a.ID, a.Brief.Goal)
	default:
		if w.ID == Owner {
			fmt.Fprintf(&b, "📋 **给你的待办 %s**：%s\n", a.ID, a.Brief.Goal)
		} else {
			fmt.Fprintf(&b, "📋 **请求 %s**：%s\n", a.ID, a.Brief.Goal)
		}
	}
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
		b.WriteString("\n交付时请附上：" + a.Brief.Evidence + "\n")
	}
	if a.Brief.Due != nil {
		b.WriteString("\n截止：" + a.Brief.Due.Format("01-02 15:04") + "\n")
	}
	switch {
	case len(a.Options) > 0:
		b.WriteString("\n直接回复你的选择：")
		for i, o := range a.Options {
			fmt.Fprintf(&b, "%s%d. %s", map[bool]string{true: "", false: "  "}[i == 0], i+1, optionLabel(o))
		}
		b.WriteString("\n")
	case a.Kind.Answered():
		b.WriteString("\n直接回复即可。\n")
	case w.ID == Owner:
		b.WriteString("\n做完了告诉我（附上结果）；做不了或要改时间也直接说。\n")
	default:
		b.WriteString("\n直接回复我：**接下** / **不接**（说下原因）/ **改时间**（给个时间）。有问题也可以直接问。\n")
	}
	return strings.TrimSpace(b.String())
}

func optionLabel(o string) string {
	return map[string]string{"approve": "批准", "reject": "拒绝"}[o] + map[bool]string{true: o, false: ""}[o != "approve" && o != "reject"]
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
