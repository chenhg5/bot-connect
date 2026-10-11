package sim

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// Long runs: time passes hour by hour; scripted people and agents act on
// what they were asked; the bot gets its ticks and reacts. This is how the
// benchmark sees keeping goals and projects on track over weeks.

func (h *Harness) run(ctx context.Context, span string) (string, error) {
	d, err := parseSpan(span)
	if err != nil {
		return "", fmt.Errorf("run: %v", err)
	}
	end := h.Clock.Now().Add(d)
	acts := 0
	for h.Clock.Now().Before(end) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		h.Clock.Advance(time.Hour)
		acts += h.behave(ctx) + h.principalReplies(ctx)
		h.App.Tick(ctx)
		h.settle(3 * time.Minute)
	}
	return fmt.Sprintf("时间流过 %s → %s（人和 agent 自动行动 %d 次）", span, h.Clock.Now().Format("01-02 15:04 Mon"), acts), nil
}

// settle waits until the bot is idle; quick when nothing happened.
func (h *Harness) settle(max time.Duration) {
	time.Sleep(150 * time.Millisecond)
	if h.Hub.Idle() && !h.busySince() {
		return
	}
	h.waitIdle(max)
}

func (h *Harness) busySince() bool {
	n := len(h.plat.snapshot()) + len(h.rec.snapshot())
	defer func() { h.lastSeen = n }()
	return n != h.lastSeen
}

// behave lets scripted workers act on their open assignments.
func (h *Harness) behave(ctx context.Context) int {
	now := h.Clock.Now()
	people, agents := map[WorkerID]PersonSpec{}, map[WorkerID]AgentSpec{}
	for _, p := range h.Case.People {
		people[WorkerID(p.ID)] = p
	}
	for _, a := range h.Case.Agents {
		agents[WorkerID(a.ID)] = a
	}
	var open []delegation.Assignment
	h.App.Store.Read(func(s *app.State) {
		for _, a := range s.Assignments {
			if a.Open() {
				open = append(open, a)
			}
		}
	})
	sort.Slice(open, func(i, j int) bool { return open[i].ID < open[j].ID })
	acts := 0
	for _, a := range open {
		if ag, ok := agents[a.Worker]; ok {
			work, _ := parseSpan(ag.Work)
			if work > 0 && (a.Status == delegation.Accepted || a.Status == delegation.Working || a.Status == delegation.Revising) &&
				now.Sub(started(a)) >= work {
				res := deliverable(a)
				if ag.LLM {
					if out, err := llmDeliverable(ctx, ag, a); err == nil {
						res = out
					}
				}
				if h.App.Report(ctx, System("driver:agent"), a.ID, app.Report{Action: "deliver", Result: res, Evidence: []string{"内容见交付说明正文"}}) == nil {
					acts++
				}
			}
			continue
		}
		p, ok := people[a.Worker]
		if !ok || p.Reply == "" {
			continue
		}
		reply, _ := parseSpan(p.Reply)
		by := Actor{Worker: a.Worker, UserID: userOf(p.ID), Role: RoleMember, Via: "chat"}
		switch {
		case a.Status == delegation.Offered && now.Sub(a.CreatedAt) >= reply:
			var r app.Report
			policy := firstNonEmpty(p.Policy, "accept")
			switch {
			case a.Kind.Answered():
				r = app.Report{Action: "answer", Choice: firstNonEmpty(p.Answer, "approve")}
			case policy == "ignore":
				continue
			case policy == "decline":
				r = app.Report{Action: "decline", Note: "排不过来"}
			case strings.HasPrefix(policy, "counter:"):
				extra, _ := parseSpan(strings.TrimPrefix(strings.TrimPrefix(policy, "counter:"), "+"))
				due := now.Add(extra)
				if a.Brief.Due != nil {
					due = a.Brief.Due.Add(extra)
				}
				r = app.Report{Action: "counter", Due: &due, Note: "手上事情多，晚一点可以吗"}
			default:
				r = app.Report{Action: "accept"}
			}
			if r.Action == "answer" && len(a.Options) > 0 && !contains(a.Options, r.Choice) {
				r.Choice = a.Options[0]
			}
			if h.App.Report(ctx, by, a.ID, r) == nil {
				acts++
			}
		case (a.Status == delegation.Accepted || a.Status == delegation.Working || a.Status == delegation.Revising) && p.Work != "":
			work, _ := parseSpan(p.Work)
			if now.Sub(started(a)) >= work {
				res := "做完了：" + a.Brief.Goal
				if p.LLM {
					if out, err := llmDeliverable(ctx, AgentSpec{Description: p.Name + "，" + p.Description}, a); err == nil {
						res = out
					}
				}
				if h.App.Report(ctx, by, a.ID, app.Report{Action: "deliver", Result: res}) == nil {
					acts++
				}
			}
		}
	}
	return acts
}

func started(a delegation.Assignment) time.Time {
	if t := a.Started(); !t.IsZero() {
		return t
	}
	return a.UpdatedAt
}

// parseSpan reads "4h", "2d", "1w".
func parseSpan(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if n := len(s); n > 1 && (s[n-1] == 'd' || s[n-1] == 'w') {
		var k int
		if _, err := fmt.Sscanf(s[:n-1], "%d", &k); err != nil {
			return 0, err
		}
		day := 24 * time.Hour
		if s[n-1] == 'w' {
			day *= 7
		}
		return time.Duration(k) * day, nil
	}
	return time.ParseDuration(s)
}

func contains(v []string, x string) bool {
	for _, y := range v {
		if y == x {
			return true
		}
	}
	return false
}

// checkOutcome scores a project after a long run.
func (h *Harness) checkOutcome(ck Check, msgs []Msg) string {
	pid := h.App.ResolveProject(ck.Project)
	var items []planning.Item
	h.App.Store.Read(func(s *app.State) {
		for _, it := range s.Items {
			if it.Project == pid && !it.Ask && it.Kind != planning.Milestone && it.Status != planning.Dropped {
				items = append(items, it)
			}
		}
	})
	if len(items) == 0 {
		return fmt.Sprintf("outcome: project %q has no items", ck.Project)
	}
	done := 0
	for _, it := range items {
		if it.Status == planning.Done {
			done++
		}
	}
	var fails []string
	if ratio := float64(done) / float64(len(items)); ratio < ck.DoneAtLeast {
		fails = append(fails, fmt.Sprintf("done %d/%d (%.0f%%) < %.0f%%", done, len(items), ratio*100, ck.DoneAtLeast*100))
	}
	var toPrincipal []Msg
	for _, m := range msgs {
		if m.Chat == principalChat {
			toPrincipal = append(toPrincipal, m)
		}
	}
	if ck.MaxMessages > 0 && len(toPrincipal) > ck.MaxMessages {
		fails = append(fails, fmt.Sprintf("%d messages to the principal > %d", len(toPrincipal), ck.MaxMessages))
	}
	if ck.NoSilentMiss {
		now := h.Clock.Now()
		projectTitle := ""
		h.App.Store.Read(func(s *app.State) { projectTitle = s.Projects[pid].Title })
		for _, it := range items {
			if !it.Open() || it.Due == nil || now.Before(*it.Due) {
				continue
			}
			told := false
			for _, m := range toPrincipal {
				told = told || (m.VAt.Before(it.Due.Add(time.Hour)) && mentions(m.Text, it, projectTitle))
			}
			if !told {
				fails = append(fails, fmt.Sprintf("「%s」过了截止 %s，之前没告诉委托人", it.Title, it.Due.Format("01-02 15:04")))
			}
		}
	}
	if len(fails) > 0 {
		return "outcome: " + strings.Join(fails, "; ")
	}
	return ""
}

// deliverable is what a scripted agent hands in: the work itself, written
// out, covering every acceptance criterion (and the last revision note).
func deliverable(a delegation.Assignment) string {
	var b strings.Builder
	b.WriteString("完成：" + a.Brief.Goal + "\n交付内容（全文）：\n")
	if len(a.Brief.Done) == 0 {
		b.WriteString("- " + a.Brief.Goal + "：已完成，内容如下——（示例正文：结构、要点、具体内容逐条列出）\n")
	}
	for i, d := range a.Brief.Done {
		fmt.Fprintf(&b, "%d. %s：已完成。具体内容：（示例正文，第 %d 部分，按要求逐条写出）\n", i+1, d, i+1)
	}
	for i := len(a.History) - 1; i >= 0; i-- {
		if a.History[i].Type == "revise" && a.History[i].Note != "" {
			b.WriteString("按返工意见修改：" + a.History[i].Note + " —— 已逐条处理，内容已直接写在上面。\n")
			break
		}
	}
	return b.String()
}

// principalReplies plays the principal in long runs: a question to them is
// answered after a while with the scripted answer.
func (h *Harness) principalReplies(ctx context.Context) int {
	pr := h.Case.Principal
	if pr.Reply == "" || pr.Say == "" {
		return 0
	}
	wait, _ := parseSpan(pr.Reply)
	now := h.Clock.Now()
	msgs := h.plat.snapshot()
	var last *Msg
	for i := h.principalSeen; i < len(msgs); i++ {
		m := msgs[i]
		if m.Chat == principalChat && asksSomething(m.Text) {
			last = &msgs[i]
		}
	}
	if last == nil || now.Sub(last.VAt) < wait {
		return 0
	}
	h.principalSeen = len(msgs)
	h.plat.mu.Lock()
	h.plat.seq++
	id := fmt.Sprintf("m%d", h.plat.seq)
	h.plat.mu.Unlock()
	h.plat.on(hubInbound(principalChat, id, principalUser, "chicken", pr.Say, false))
	return 1
}

func asksSomething(text string) bool {
	for _, w := range []string{"？", "?", "吗", "拍板", "确认", "决定", "选哪", "同意"} {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// mentions: a message to the principal is about this item — its id, the
// start of its title, or the project together with words of risk.
func mentions(text string, it planning.Item, project string) bool {
	if strings.Contains(text, string(it.ID)) {
		return true
	}
	key := []rune(it.Title)
	if len(key) > 4 {
		key = key[:4]
	}
	if strings.Contains(text, string(key)) {
		return true
	}
	pk := []rune(project)
	if len(pk) > 4 {
		pk = pk[:4]
	}
	if project != "" && strings.Contains(text, string(pk)) {
		for _, w := range []string{"延期", "赶不上", "来不及", "风险", "红", "推迟", "不可能", "晚"} {
			if strings.Contains(text, w) {
				return true
			}
		}
	}
	return false
}

// llmDeliverable has a small model actually do the work described in the
// brief (as text), so the bot reviews a real deliverable.
func llmDeliverable(ctx context.Context, ag AgentSpec, a delegation.Assignment) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "你是一个 agent：%s。直接完成下面的任务，把交付内容的全文写出来（中文，不超过 1200 字）。"+
		"缺信息就做合理假设并注明，不要说做不到，不要问问题，不要写占位符。\n\n任务：%s\n", ag.Description, a.Brief.Goal)
	if a.Brief.Context != "" {
		b.WriteString("背景：" + a.Brief.Context + "\n")
	}
	if len(a.Brief.Done) > 0 {
		b.WriteString("完成标准（逐条满足）：\n- " + strings.Join(a.Brief.Done, "\n- ") + "\n")
	}
	for i := len(a.History) - 1; i >= 0; i-- {
		if a.History[i].Type == "revise" && a.History[i].Note != "" {
			b.WriteString("上一版被退回，返工意见：" + a.History[i].Note + "\n")
			break
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, "claude", "-p", b.String(), "--model", "haiku", "--output-format", "text", "--tools", "", "--setting-sources", "")
	cmd.Env = cleanEnv(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", fmt.Errorf("empty deliverable")
	}
	return text, nil
}

// cleanEnv drops provider overrides so the CLI uses the local login.
func cleanEnv(env []string) []string {
	var out []string
	for _, e := range env {
		if strings.HasPrefix(e, "ANTHROPIC_") || strings.HasPrefix(e, "CLAUDECODE") {
			continue
		}
		out = append(out, e)
	}
	return out
}
