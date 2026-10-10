package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/attention"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/insight"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/world"
)

// BriefingOptions shape the page the brain reads.
type BriefingOptions struct {
	Focus    ProjectID // render this project's card in full ("" = all active projects, compact)
	MaxItems int       // open items per project (default 8)
}

// Briefing renders what the brain needs to decide the next step: the
// agenda (now / today / watch) and the state of projects — all computed,
// nothing for the brain to look up. Empty when there is nothing to manage.
func (a *App) Briefing(ctx context.Context, o BriefingOptions) string {
	ag, c := a.Agenda(ctx)
	w := c.World
	if o.MaxItems <= 0 {
		o.MaxItems = 8
	}
	var b strings.Builder
	if len(ag.Now)+len(ag.Today)+len(ag.Watch) > 0 {
		b.WriteString("agenda（框架按规则算出，按分数排序）：\n")
		if len(ag.Now) > 0 {
			b.WriteString("Now（这次必须处理，用工具给出结果：处理掉 / 已行动并等对方 / 推迟到某时 / 交给主人；再用 resolve 记下）：\n")
			for i, s := range ag.Now {
				fmt.Fprintf(&b, "%d. [%s·%s] %s  key=%s 分=%.1f\n", i+1, s.Kind, s.Level, s.Summary, s.Key, s.Score)
				if len(s.Suggest) > 0 {
					fmt.Fprintf(&b, "   建议：%s\n", strings.Join(s.Suggest, " / "))
				}
			}
		}
		if len(ag.Today) > 0 {
			b.WriteString("Today：")
			var parts []string
			for _, s := range ag.Today {
				parts = append(parts, s.Summary+"（"+s.Key+"）")
			}
			b.WriteString(strings.Join(parts, "；") + "\n")
		}
		if len(ag.Watch) > 0 {
			var parts []string
			for _, s := range ag.Watch {
				parts = append(parts, s.Summary)
			}
			b.WriteString("Watch：" + strings.Join(parts, "；") + "\n")
		}
	}
	sigs := a.Attention.Signals(c)
	var ids []ProjectID
	for id, p := range w.Projects {
		if id != OrgProject && p.Open() && (o.Focus == "" || o.Focus == id) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		pi, pj := w.Projects[ids[i]], w.Projects[ids[j]]
		if pi.Priority != pj.Priority {
			return pi.Priority < pj.Priority
		}
		return ids[i] < ids[j]
	})
	for _, id := range ids {
		b.WriteString("\n" + ProjectCard(w, w.Projects[id], insight.ProjectProgress(w, id, c.Forecast, attention.RiskCount(sigs, id)), o.Focus == id, o.MaxItems))
	}
	if org, ok := w.Projects[OrgProject]; ok {
		if roles := org.ActiveMembers(w.Now); len(roles) > 0 {
			b.WriteString("\n公司层面的角色：")
			var parts []string
			for _, m := range roles {
				parts = append(parts, roleText(m))
			}
			b.WriteString(strings.Join(parts, "；") + "\n")
		}
		if len(org.Policies) > 0 {
			b.WriteString("审批规则：")
			var parts []string
			for _, r := range org.Policies {
				p := r.Action
				if r.Scope != "" {
					p += "@" + r.Scope
				}
				parts = append(parts, p+" → "+strings.Join(r.Approvers, "/"))
			}
			b.WriteString(strings.Join(parts, "；") + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func roleText(m portfolio.Membership) string {
	t := string(m.Worker) + "=" + m.Role
	if len(m.Duties) > 0 {
		t += "（" + strings.Join(m.Duties, "、") + "）"
	}
	if m.Period.Until != nil {
		t += "至 " + m.Period.Until.Format("01-02")
	}
	return t
}

// ProjectCard renders one project.
func ProjectCard(w *world.World, p portfolio.Project, pr insight.Progress, full bool, maxItems int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "项目 %s「%s」%s", p.ID, p.Title, p.Priority)
	if p.Timebox.Until != nil {
		fmt.Fprintf(&b, "，截止 %s", p.Timebox.Until.Format("01-02"))
	}
	fmt.Fprintf(&b, "，健康度 %s", healthZh(pr.Health))
	if len(pr.Reasons) > 0 {
		fmt.Fprintf(&b, "（%s）", strings.Join(pr.Reasons, "；"))
	}
	b.WriteString("\n")
	if p.Objective.Text != "" {
		fmt.Fprintf(&b, "  目标：%s", p.Objective.Text)
		for _, m := range p.Objective.Metrics {
			fmt.Fprintf(&b, "；%s %g/%g%s", m.Name, m.Current, m.Target, m.Unit)
		}
		b.WriteString("\n")
	}
	if pr.Total > 0 {
		fmt.Fprintf(&b, "  进度：%.0f%%（%d/%d 件未完成）", pr.Done*100, pr.Open, pr.Total)
		if pr.Planned >= 0 {
			fmt.Fprintf(&b, "，计划应到 %.0f%%", pr.Planned*100)
		}
		if pr.Forecast != nil {
			fmt.Fprintf(&b, "，预计 %s 全部完成", pr.Forecast.Format("01-02 15:04"))
		}
		b.WriteString("\n")
	}
	if pr.NextMilestone != nil {
		fmt.Fprintf(&b, "  下一个里程碑：%s「%s」%s\n", pr.NextMilestone.ID, pr.NextMilestone.Title, pr.NextMilestone.Due.Format("01-02"))
	}
	if ms := p.ActiveMembers(w.Now); len(ms) > 0 {
		var parts []string
		for _, m := range ms {
			parts = append(parts, roleText(m))
		}
		b.WriteString("  成员：" + strings.Join(parts, "；") + "\n")
	}
	if full {
		if p.Card.Scope != "" {
			b.WriteString("  范围：" + p.Card.Scope + "\n")
		}
		for _, d := range p.Card.Decisions {
			fmt.Fprintf(&b, "  决策（%s）：%s\n", d.At.Format("01-02"), d.Text)
		}
		for _, c := range p.Card.Conventions {
			b.WriteString("  约定：" + c + "\n")
		}
		for _, l := range p.Card.Links {
			b.WriteString("  链接：" + l + "\n")
		}
	}
	items := w.ItemsOf(p.ID, false)
	sort.Slice(items, func(i, j int) bool {
		if (items[i].Due == nil) != (items[j].Due == nil) {
			return items[i].Due != nil
		}
		if items[i].Due != nil && !items[i].Due.Equal(*items[j].Due) {
			return items[i].Due.Before(*items[j].Due)
		}
		return items[i].ID < items[j].ID
	})
	for i, it := range items {
		if i >= maxItems {
			fmt.Fprintf(&b, "  …还有 %d 件（project_status 查看）\n", len(items)-maxItems)
			break
		}
		b.WriteString("  - " + ItemLine(w, it) + "\n")
	}
	return b.String()
}

// ItemLine is one item with who has it and how it's going.
func ItemLine(w *world.World, it planning.Item) string {
	line := fmt.Sprintf("%s「%s」%s", it.ID, it.Title, statusZh(string(it.Status)))
	if as, ok := w.Assignments[it.Assignment]; ok {
		line += fmt.Sprintf(" → %s（%s %s）", as.Worker, as.ID, assignmentZh(as))
	}
	if it.Due != nil {
		line += " 截止 " + it.Due.Format("01-02 15:04")
		if w.Now.After(*it.Due) {
			line += "【已逾期】"
		}
	}
	if it.Blocker != nil {
		line += " 卡在：" + it.Blocker.Reason
	}
	if len(it.DependsOn) > 0 {
		var deps []string
		for _, d := range it.DependsOn {
			deps = append(deps, string(d))
		}
		line += " 依赖 " + strings.Join(deps, ",")
	}
	return line
}

func assignmentZh(a delegation.Assignment) string {
	s := map[delegation.Status]string{delegation.Offered: "已发出待回复", delegation.Countered: "对方还价", delegation.Accepted: "已接下",
		delegation.Working: "进行中", delegation.Blocked: "卡住", delegation.Paused: "暂停", delegation.Delivered: "已交付待验收",
		delegation.Revising: "返工中"}[a.Status]
	if s == "" {
		s = string(a.Status)
	}
	if a.Kind != delegation.Work {
		s = string(a.Kind) + "·" + s
	}
	return s
}

func statusZh(s string) string {
	return map[string]string{"todo": "待分派", "active": "进行中", "blocked": "阻塞", "done": "已完成", "dropped": "已取消"}[s]
}

func healthZh(h insight.Health) string {
	return map[insight.Health]string{insight.Green: "绿", insight.Yellow: "黄", insight.Red: "红"}[h]
}

// AssigneeView is what a person who has work from the bot sees: their own
// open assignments and nothing else.
func (a *App) AssigneeView(worker WorkerID) string {
	var as []delegation.Assignment
	a.Store.Read(func(s *State) {
		for _, x := range s.Assignments {
			if x.Worker == worker && x.Open() {
				as = append(as, x)
			}
		}
	})
	if len(as) == 0 {
		return ""
	}
	sort.Slice(as, func(i, j int) bool { return as[i].CreatedAt.Before(as[j].CreatedAt) })
	var b strings.Builder
	for _, x := range as {
		fmt.Fprintf(&b, "- %s [%s] %s", x.ID, assignmentZh(x), oneLine(x.Brief.Goal, 100))
		if x.Brief.Due != nil {
			b.WriteString("；截止 " + x.Brief.Due.Format("01-02 15:04"))
		}
		if len(x.Options) > 0 {
			b.WriteString("；选项：" + strings.Join(x.Options, " / "))
		}
		if x.Question != "" {
			b.WriteString("；他问过：" + oneLine(x.Question, 80))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ParseWhen reads a time for tools: "2026-10-17 18:00", "2026-10-17",
// "10-17 18:00", "18:00" (next occurrence), or a duration from now
// ("2h", "30m", "3d").
func ParseWhen(s string, now time.Time) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if d, err := parseDuration(s, 24*time.Hour); err == nil {
		t := now.Add(d)
		return &t, nil
	}
	loc := now.Location()
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			if layout == "2006-01-02" {
				t = t.Add(18 * time.Hour) // a date means end of that working day
			}
			return &t, nil
		}
	}
	if t, err := time.ParseInLocation("01-02 15:04", s, loc); err == nil {
		t = time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		if t.Before(now.Add(-24 * time.Hour)) {
			t = t.AddDate(1, 0, 0)
		}
		return &t, nil
	}
	if t, err := time.ParseInLocation("15:04", s, loc); err == nil {
		at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		if !at.After(now) {
			at = at.Add(24 * time.Hour)
		}
		return &at, nil
	}
	return nil, Invalid("can't read time %q (use 2026-10-17 18:00, 10-17 18:00, 18:00, or 2h / 3d)", s)
}

// ParseEffort reads an estimate of effort: "2h", "30m", "1d" (= 8 working hours).
func ParseEffort(s string) (time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return parseDuration(strings.TrimSpace(s), 8*time.Hour)
}

func parseDuration(s string, day time.Duration) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		var f float64
		if _, err := fmt.Sscanf(n, "%g", &f); err != nil || f <= 0 {
			return 0, Invalid("bad duration %q", s)
		}
		return time.Duration(f * float64(day)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, Invalid("bad duration %q", s)
	}
	return d, nil
}
