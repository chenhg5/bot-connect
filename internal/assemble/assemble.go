// Package assemble wires the project-management core to the conversation
// hub — the same way for the running bot and for the evaluation harness.
package assemble

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/adapters/probe"
	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/audit"
	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/domain/inbox"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/hub"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/judge"
)

// WirePM connects a bot's PM core and hub: wake-ups go to the main session
// (one audience per turn), messages come in as signals, the bot's wake
// policy applies, and main-session turns report back.
// WirePM wires the PM scaffold into a hub. The returned claim log must be
// added to the tool server's audit sink (it checks replies against the
// turn's tool calls).
func WirePM(a *app.App, h *hub.Hub, bc config.BotConfig, jc config.Judge) *ClaimLog {
	claims := NewClaimLog()
	h.SetTurnStart(claims.Reset)
	a.Style = app.Style{Risk: bc.Style.Risk, AskBefore: bc.Style.AskBefore, BudgetMonth: bc.Style.BudgetMonth, Parallel: bc.Style.Parallel}
	wireJudge(a, h, bc, jc, claims)
	a.Waker = app.WakerFunc(func(ctx context.Context, w app.Wake) {
		// One audience per wake-up: the principal side together (and the
		// scaffold's own signals), each other person on their own, so a turn
		// runs with the rights of whoever it is for.
		type group struct {
			from identity.User
			sigs []inbox.Signal
		}
		var groups []*group
		byKey := map[string]*group{}
		for _, x := range w.Signals {
			from, key := hub.MainUser, "principal"
			if r := identity.Role(x.Actor.Role); x.Actor.UserID != "" && r != identity.RolePrincipal && r != identity.RoleAdmin && x.Source != inbox.Relay {
				from = identity.User{ID: x.Actor.UserID, Name: x.ActorName, Role: r}
				key = x.Actor.UserID
			}
			g := byKey[key]
			if g == nil {
				g = &group{from: from}
				byKey[key] = g
				groups = append(groups, g)
			}
			g.sigs = append(g.sigs, x)
		}
		for _, g := range groups {
			ids := make([]string, len(g.sigs))
			reply := w.ReplyConv
			for i, x := range g.sigs {
				ids[i] = x.ID
				if x.ReplyTo.Conv != "" {
					reply = x.ReplyTo.Conv
				}
			}
			h.WakeMain(app.FocusText(g.sigs), ids, reply, g.from)
		}
	})
	for k, v := range bc.Wake.Modes() {
		a.Policy.Modes[k] = v
	}
	if bc.Wake.BatchEvery.Duration > 0 {
		a.Policy.BatchEvery = bc.Wake.BatchEvery.Duration
	}
	// One continuous session: everyone's messages become signals for the
	// main session (unless visitors are isolated, then only the principal side's).
	h.SetIntake(func(in hub.Inbound, u identity.User, conv string) bool {
		if bc.IsolateVisitors && !u.Privileged() {
			return false
		}
		by := Actor{UserID: u.ID, Role: Role(u.Role), Via: "chat"}
		if u.Role == identity.RolePrincipal {
			by.Worker = Principal
		} else if id, ok := a.WorkerFor(u.ID, u.UnionID, u.Email); ok {
			by.Worker = id
		}
		src, reason := inbox.DirectMessage, "dm"
		if in.IsGroup {
			src, reason = inbox.Mention, "mention"
		}
		_, _, err := a.Ingest(context.Background(), app.SignalSpec{Dedupe: "msg:" + in.Platform + ":" + firstNonEmpty(in.MessageID, fmt.Sprint(time.Now().UnixNano())),
			Source: src, Reason: reason, Actor: by, ActorName: u.Display(), Summary: oneLine(in.Text, 120), Body: in.Text,
			Refs: inbox.Refs{Worker: by.Worker}, ReplyTo: inbox.ReplyTo{Conv: conv, MessageID: in.MessageID}})
		if err != nil {
			slog.Error("pm: could not take the message in", "err", err)
			return false
		}
		return true
	}, func(ids []string, answered bool) { a.TurnDone(context.Background(), ids, answered) })
	return claims
}

func firstNonEmpty(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// NewJudge builds the configured L1 judge (judge.None without a key).
func NewJudge(jc config.Judge) judge.Judge {
	if jc.Provider == "claude" {
		return judge.Claude{Model: jc.Model}
	}
	if jc.Provider == "none" || jc.Key() == "" {
		return judge.None{}
	}
	return &judge.Jev{Key: jc.Key(), Model: jc.Model, URL: jc.URL}
}

// wireJudge plugs the L1 judgment layer in: triage of incoming messages
// (and landing what needs no decision), the outbound leak guard, and
// semantic staffing. Without a judge the guard still catches exact project
// titles.
func wireJudge(a *app.App, h *hub.Hub, bc config.BotConfig, jc config.Judge, claims *ClaimLog) {
	j := NewJudge(jc)
	_, live := j.(*judge.Jev)
	a.Say = func(ctx context.Context, conv, text string) error { return h.SendTo(ctx, conv, text) }
	if live {
		a.Sorter = judge.Sorter{J: j}
	}
	if live && config.On(jc.Triage) {
		a.Triage = &judge.Triage{J: j, App: a}
	}
	if live && config.On(jc.PlanReview) {
		a.Reviewer = &judge.PlanReview{J: j}
		a.Learnings = judge.LearningCheck{J: j}
	}
	if live && config.On(jc.Staffing) {
		a.Staffing.Matchers = append(a.Staffing.Matchers, judge.Matcher{J: j})
	}
	honest := func(ctx context.Context, c *hub.Conversation, text string) string {
		if !live || claims == nil || !mayClaim(text) {
			return text
		}
		var ok, failed []string
		for _, e := range claims.Turn(h.MainKey(), c.Key) {
			line := e.Tool + " " + audit.Clip(fmt.Sprint(e.Args), 160)
			if e.Error != "" {
				failed = append(failed, line+" ✗ "+audit.Clip(e.Error, 120))
			} else {
				ok = append(ok, line+" → "+audit.Clip(e.Text, 240))
			}
		}
		p, err := judge.ClaimCheck(ctx, j, firstNonEmpty(bc.PrincipalName, "委托人"), text, ok, failed)
		if err != nil || p < 0.7 {
			return text
		}
		slog.Warn("reply claims something this turn didn't do", "conv", c.Key, "p", p)
		_, _, _ = a.Ingest(ctx, app.SignalSpec{Dedupe: fmt.Sprintf("claim:%s:%d", c.Key, time.Now().UnixNano()), Source: inbox.Internal,
			Reason: "claim_unsupported", Actor: System("honesty"), Summary: "刚才的回复说做了某件事，但这一轮没有对应的成功操作",
			Body: fmt.Sprintf("发往 %s 的回复：\n%s\n\n这一轮成功的操作：%v\n失败的操作：%v\n要么现在真的去做，要么告诉对方实际情况。", c.Key, text, ok, failed)})
		note := "（核对：上面说已经做了的事，这一轮没有对应的成功操作"
		if len(failed) > 0 {
			note += "，有 " + fmt.Sprint(len(failed)) + " 个操作失败了"
		}
		return text + "\n\n" + note + "，以实际状态为准，我会跟进纠正。）"
	}
	if !config.On(jc.Guard) {
		h.SetEgress(func(ctx context.Context, c *hub.Conversation, text string) (string, bool) {
			return honest(ctx, c, text), true
		})
		return
	}
	var guardJudge judge.Judge
	if live {
		guardJudge = j
	}
	g := &judge.Guard{J: guardJudge, App: a, Threshold: jc.LeakThreshold}
	principal := firstNonEmpty(bc.PrincipalName, "委托人")
	h.SetEgress(func(ctx context.Context, c *hub.Conversation, text string) (string, bool) {
		text = honest(ctx, c, text)
		if c.Key == h.MainKey() || h.IsPrincipalConv(c) {
			return text, true
		}
		gctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		v := g.Check(gctx, c.Peer, c.LastAsked, text)
		cancel()
		if !v.Block {
			return text, true
		}
		slog.Warn("outbound guard held a message back", "conv", c.Key, "reason", v.Reason)
		_, _, _ = a.Ingest(ctx, app.SignalSpec{Dedupe: fmt.Sprintf("guard:%s:%d", c.Key, time.Now().UnixNano()), Source: inbox.Internal,
			Reason: "guard_blocked", Actor: System("guard"), Summary: "拦下了一条发给同事的消息：" + v.Reason,
			Body: fmt.Sprintf("发往 %s 的消息被拦下（%s）。原文：\n%s\n对方问的是：%s\n需要的话，改写后用 reply / contact 重新发。", c.Key, v.Reason, text, c.LastAsked)})
		return fmt.Sprintf("这个我需要先跟 %s 确认一下，确认后再回复你。", principal), true
	})
}

// WireObserve gives the app its eyes: a local prober limited to the
// configured roots, read-only commands and hosts, plus the command patterns
// the principal allows at run time.
func WireObserve(a *app.App, oc config.Observe, toolDir string) {
	if len(oc.Roots) == 0 {
		return
	}
	roots := make([]string, len(oc.Roots))
	for i, r := range oc.Roots {
		roots[i] = config.ExpandHome(r)
	}
	l := &probe.Local{Roots: roots, Commands: append(append([]string(nil), probe.DefaultCommands...), oc.Commands...),
		Hosts: oc.Hosts, Extra: a.AllowedCommands, Sessions: oc.Sessions, ToolDir: toolDir}
	a.Prober, a.Hands = l, l
}
