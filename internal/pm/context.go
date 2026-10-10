package pm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/plan"
	"github.com/chenhg5/bot-connect/internal/worker"
	"github.com/chenhg5/bot-connect/internal/workforce"
)

// Context is the project block for the brain's context: for owner/admin
// turns the open goals, tasks, risks and roles; for a person who has work
// from the bot, their own assignments (and nothing else).
func (s *Service) Context(bot string, caller identity.User) string {
	var b strings.Builder
	if pid := s.PersonOf(bot, caller); pid != "" && pid != OwnerID {
		if as := s.AssignmentsOf(bot, pid); len(as) > 0 {
			fmt.Fprintf(&b, "\n%s 是 worker（%s），他名下来自你的委托（他只能看到这些）：\n", caller.Display(), pid)
			for _, a := range as {
				fmt.Fprintf(&b, "- %s [%s] %s；截止 %s\n", a.ID, a.Status, oneLine(a.Brief.Goal, 100), dueText(a.Brief.Due, time.Local))
				if a.Question != "" {
					fmt.Fprintf(&b, "  他问过：%s\n", oneLine(a.Question, 100))
				}
			}
			b.WriteString("他的回复（接下 / 不接 / 改时间 / 进展 / 提问 / 交付）用 assignment_respond 登记，然后简短确认。\n")
		}
	}
	if !caller.Privileged() {
		return b.String()
	}
	now := s.now()
	goals := s.Goals(bot)
	tasks := s.Tasks(bot, false)
	risks := s.Risks(bot, false)
	roles := s.Roles(bot, false)
	if len(goals)+len(tasks)+len(risks)+len(roles) == 0 {
		return b.String()
	}
	b.WriteString("\nprojects:\n")
	for _, g := range goals {
		if g.Status != plan.GoalActive {
			continue
		}
		fmt.Fprintf(&b, "- 目标 %s「%s」截止 %s", g.ID, g.Title, dueText(g.Due, time.Local))
		for _, m := range g.Metrics {
			fmt.Fprintf(&b, "；%s %g/%g%s", m.Name, m.Current, m.Target, m.Unit)
		}
		b.WriteString("\n")
	}
	risky := map[string]plan.Level{}
	for _, r := range risks {
		if l, ok := risky[r.Subject]; !ok || r.Level > l {
			risky[r.Subject] = r.Level
		}
	}
	shown := 0
	for _, t := range tasks {
		if shown >= 15 {
			fmt.Fprintf(&b, "- …还有 %d 个任务（task_list）\n", len(tasks)-shown)
			break
		}
		shown++
		line := fmt.Sprintf("- 任务 %s「%s」%s", t.ID, t.Title, t.Status)
		if t.Assignee != "" {
			a, _ := s.Assignment(bot, t.Assignment)
			line += fmt.Sprintf(" → %s(%s %s)", t.Assignee, a.ID, a.Status)
		}
		if t.Due != nil {
			line += " 截止 " + t.Due.Format("01-02 15:04")
			if t.Open() && now.After(*t.Due) {
				line += "【已逾期】"
			}
		}
		if t.GoalID != "" {
			line += " 属于 " + t.GoalID
		}
		if l, ok := risky[t.ID]; ok {
			line += " ⚠" + l.String()
		}
		if t.Blocked != "" {
			line += " 卡在：" + oneLine(t.Blocked, 60)
		}
		b.WriteString(line + "\n")
	}
	if len(risks) > 0 {
		b.WriteString("open risks:\n")
		for _, r := range risks {
			fmt.Fprintf(&b, "- %s [%s %s] %s：%s\n", r.ID, r.Kind, r.Level, r.Subject, oneLine(r.Summary, 140))
		}
	}
	if len(roles) > 0 {
		b.WriteString("roles (who to go to for what, now):\n")
		b.WriteString(renderRoles(roles, goals))
	}
	return b.String()
}

func renderRoles(roles []plan.Role, goals []plan.Goal) string {
	titles := map[string]string{}
	for _, g := range goals {
		titles[g.ID] = g.Title
	}
	var b strings.Builder
	for _, r := range roles {
		scope := "公司层面"
		if r.Scope != "org" {
			scope = "项目 " + r.Scope
			if t := titles[r.Scope]; t != "" {
				scope += "「" + t + "」"
			}
		}
		fmt.Fprintf(&b, "- %s：%s 是 %s", scope, r.Worker, r.Title)
		if len(r.Duties) > 0 {
			fmt.Fprintf(&b, "，负责 %s", strings.Join(r.Duties, "、"))
		}
		if r.Until != nil {
			fmt.Fprintf(&b, "（到 %s）", r.Until.Format("01-02"))
		}
		fmt.Fprintf(&b, " [%s]\n", r.ID)
	}
	return b.String()
}

// Workforce renders every worker the caller may hand work to: profile, live
// state and roles, for choosing who should do something.
func (s *Service) Workforce(ctx context.Context, bot string, scope worker.Scope) string {
	type row struct {
		id, line string
	}
	var rows []row
	roles := s.Roles(bot, false)
	roleOf := func(id string) string {
		var parts []string
		for _, r := range roles {
			if r.Worker == id {
				p := r.Title
				if r.Scope != "org" {
					p += "@" + r.Scope
				}
				parts = append(parts, p)
			}
		}
		if len(parts) == 0 {
			return ""
		}
		return "；角色：" + strings.Join(parts, "，")
	}
	add := func(id string) {
		p, st, err := s.State(ctx, bot, id)
		if err != nil {
			return
		}
		kind := string(p.Kind)
		if p.Kind == workforce.KindHuman {
			kind = "人"
		}
		line := fmt.Sprintf("- %s（%s，%s）— %s\n  状态：%s%s", id, p.Name, kind, firstNonEmpty(p.Description, strings.Join(p.Skills, "、"), "-"), st.Summary(s.now()), roleOf(id))
		if p.Kind == workforce.KindHuman && id != OwnerID {
			ch := make([]string, 0, len(p.Contact))
			for _, c := range p.Contact {
				ch = append(ch, string(c.Channel))
			}
			line += "；联系方式：" + strings.Join(ch, " → ")
		}
		rows = append(rows, row{id, line})
	}
	add(OwnerID)
	for _, p := range s.people {
		if allowedBot(p.Bots, bot) {
			add(p.ID)
		}
	}
	if s.agents != nil {
		for _, wi := range s.agents.Snapshot() {
			if wi.Parent == "" && wi.User == "" && s.agents.Access(wi.Name, scope).Delegate {
				add(wi.Name)
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].id == OwnerID && rows[j].id != OwnerID })
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.line + "\n")
	}
	if b.Len() == 0 {
		return "(no workers)"
	}
	return b.String()
}
