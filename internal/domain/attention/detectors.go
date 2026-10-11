package attention

import (
	"fmt"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/insight"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
)

// DefaultDetectors is the first rule set.
func DefaultDetectors() []Detector {
	return []Detector{Overdue{}, WillMiss{}, ThinSlack{}, Unassigned{}, Silent{}, Stuck{}, LateDependency{},
		WaitingOnUs{}, OfferUnanswered{}, Overload{}, AssigneeUnavailable{}, Routine{}, Exploration{}}
}

func fmtT(t time.Time) string { return t.Format("01-02 15:04") }

func itemSignal(kind string, lv Level, it planning.Item, summary string, suggest []string, kv ...any) Signal {
	s := Signal{Key: kind + ":" + string(it.ID), Kind: kind, Level: lv, Subject: "item:" + string(it.ID), Project: it.Project,
		Summary: summary, Suggest: suggest, Facts: map[string]any{}}
	if it.Assignee != "" {
		s.People = []WorkerID{it.Assignee}
	}
	if it.Due != nil {
		s.Facts["due"] = *it.Due
	}
	for i := 0; i+1 < len(kv); i += 2 {
		s.Facts[fmt.Sprint(kv[i])] = kv[i+1]
	}
	return s
}

// Overdue: past due and not done.
type Overdue struct{}

func (Overdue) Name() string { return "overdue" }
func (Overdue) Detect(c *Context) []Signal {
	var out []Signal
	for _, it := range c.Items {
		if it.Open() && it.Due != nil && c.Now.After(*it.Due) {
			out = append(out, itemSignal("overdue", High, it,
				fmt.Sprintf("「%s」已过截止 %s（%s）", it.Title, fmtT(*it.Due), c.Now.Sub(*it.Due).Round(time.Minute)),
				[]string{"confirm_eta", "cut_scope", "reassign", "tell_requester"}))
		}
	}
	return out
}

// WillMiss: forecast (pace-adjusted, through dependencies) lands after due.
type WillMiss struct{}

func (WillMiss) Name() string { return "will_miss" }
func (WillMiss) Detect(c *Context) []Signal {
	var out []Signal
	for id, it := range c.Items {
		f, ok := c.Forecast[id]
		if !it.Open() || it.Due == nil || c.Now.After(*it.Due) || !ok || !f.After(*it.Due) {
			continue
		}
		own := c.Now.Add(insight.Remaining(c.World, it, c.Pace))
		if !own.After(*it.Due) {
			continue // late only because of a dependency: LateDependency reports it
		}
		out = append(out, itemSignal("will_miss", High, it,
			fmt.Sprintf("「%s」预计 %s 完成，晚于截止 %s", it.Title, fmtT(f), fmtT(*it.Due)),
			[]string{"confirm_eta", "cut_scope", "add_help", "tell_requester"}, "forecast", f, "pace", c.Pace.Factor(it.Assignee)))
	}
	return out
}

// ThinSlack: on time, but with less than a quarter of the estimate to spare.
type ThinSlack struct{}

func (ThinSlack) Name() string { return "thin_slack" }
func (ThinSlack) Detect(c *Context) []Signal {
	var out []Signal
	for id, it := range c.Items {
		f, ok := c.Forecast[id]
		if !it.Open() || it.Due == nil || it.Estimate <= 0 || !ok || f.After(*it.Due) || c.Now.After(*it.Due) {
			continue
		}
		if slack := it.Due.Sub(f); slack < it.Estimate/4 {
			out = append(out, itemSignal("thin_slack", Medium, it,
				fmt.Sprintf("「%s」预计 %s 完成，余量只有 %s", it.Title, fmtT(f), slack.Round(time.Minute)),
				[]string{"watch", "prepare_fallback"}, "forecast", f))
		}
	}
	return out
}

// Unassigned: nobody has it, it's ready, and the due date is getting close.
type Unassigned struct{}

func (Unassigned) Name() string { return "unassigned" }
func (Unassigned) Detect(c *Context) []Signal {
	var out []Signal
	g := c.Graph()
	for _, it := range c.Items {
		if !it.Open() || it.Assignment != "" || it.Due == nil || it.Kind == planning.Milestone || !g.Ready(it.ID) || len(g.Children(it.ID)) > 0 {
			continue
		}
		est := it.Estimate
		if est <= 0 {
			est = time.Hour
		}
		left := it.Due.Sub(c.Now)
		switch {
		case left < est:
			out = append(out, itemSignal("unassigned", High, it, fmt.Sprintf("「%s」没人负责，按预估 %s 已经来不及", it.Title, est),
				[]string{"assign_now", "cut_scope", "tell_requester"}))
		case left < est*3/2:
			out = append(out, itemSignal("unassigned", Medium, it, fmt.Sprintf("「%s」没人负责，离截止只剩 %s", it.Title, left.Round(time.Minute)),
				[]string{"assign_now"}))
		}
	}
	return out
}

// Silent: work is going on but no sign of life for too long (agents:
// max(estimate/4, 2h); people: a working day).
type Silent struct{}

func (Silent) Name() string { return "silent" }
func (Silent) Detect(c *Context) []Signal {
	var out []Signal
	for _, a := range c.Assignments {
		if a.Status != delegation.Working && a.Status != delegation.Accepted && a.Status != delegation.Revising {
			continue
		}
		it, ok := c.Items[a.Item]
		if !ok || !it.Open() {
			continue
		}
		last := a.LastLife
		if last.IsZero() {
			last = a.CreatedAt
		}
		quiet := 24 * time.Hour
		if c.Workers[a.Worker].Kind.Agentic() {
			quiet = max(it.Estimate/4, 2*time.Hour)
		}
		if c.Now.Sub(last) > quiet {
			s := itemSignal("silent", Medium, it, fmt.Sprintf("「%s」%s 已经 %s 没有进展", it.Title, a.Worker, c.Now.Sub(last).Round(time.Minute)),
				[]string{"check_in"}, "assignment", string(a.ID))
			s.People = []WorkerID{a.Worker}
			out = append(out, s)
		}
	}
	return out
}

// Stuck: blocked for more than 4 hours.
type Stuck struct{}

func (Stuck) Name() string { return "stuck" }
func (Stuck) Detect(c *Context) []Signal {
	var out []Signal
	for _, it := range c.Items {
		if it.Status == planning.Blocked && it.Blocker != nil && c.Now.Sub(it.Blocker.Since) > 4*time.Hour {
			out = append(out, itemSignal("stuck", Medium, it,
				fmt.Sprintf("「%s」卡在「%s」已 %s", it.Title, it.Blocker.Reason, c.Now.Sub(it.Blocker.Since).Round(time.Minute)),
				[]string{"unblock", "find_people", "work_around"}))
		}
	}
	return out
}

// LateDependency: the item itself would fit, but something it waits for
// finishes too late.
type LateDependency struct{}

func (LateDependency) Name() string { return "late_dep" }
func (LateDependency) Detect(c *Context) []Signal {
	var out []Signal
	for id, it := range c.Items {
		f, ok := c.Forecast[id]
		if !it.Open() || it.Due == nil || !ok || !f.After(*it.Due) || c.Now.After(*it.Due) {
			continue
		}
		if c.Now.Add(insight.Remaining(c.World, it, c.Pace)).After(*it.Due) {
			continue // late by itself: WillMiss
		}
		var late []string
		for _, d := range it.DependsOn {
			if dep, ok := c.Items[d]; ok && dep.Open() {
				late = append(late, string(d))
			}
		}
		out = append(out, itemSignal("late_dep", High, it,
			fmt.Sprintf("「%s」依赖的 %v 完成得太晚，预计 %s，截止 %s", it.Title, late, fmtT(f), fmtT(*it.Due)),
			[]string{"speed_up_dependency", "parallelize", "move_due", "cut_scope"}, "blocked_by", late))
	}
	return out
}

// WaitingOnUs: a worker waits for an answer, a verdict, or a decision on
// their counter-proposal.
type WaitingOnUs struct{}

func (WaitingOnUs) Name() string { return "waiting_on_us" }
func (WaitingOnUs) Detect(c *Context) []Signal {
	var out []Signal
	for _, a := range c.Assignments {
		what, ok := a.WaitingOnRequester()
		if !ok {
			continue
		}
		lv := Medium
		if it, ok := c.Items[a.Item]; ok && it.Due != nil && it.Due.Sub(c.Now) < 24*time.Hour {
			lv = High
		}
		text := map[string]string{"question": "在等你回答问题：" + a.Question, "verify": "交付了，等验收", "counter": "提了新的时间，等你决定"}[what]
		out = append(out, Signal{Key: "waiting_on_us:" + string(a.ID) + ":" + what, Kind: "waiting_on_us", Level: lv,
			Subject: "assignment:" + string(a.ID), Project: a.Project, Summary: fmt.Sprintf("%s（%s）%s", a.Worker, a.ID, text),
			Suggest: []string{map[string]string{"question": "answer", "verify": "review", "counter": "decide_counter"}[what]},
			People:  []WorkerID{a.Worker}, Since: a.UpdatedAt, Facts: map[string]any{"assignment": string(a.ID), "waiting": what}})
	}
	return out
}

// OfferUnanswered: a person hasn't answered an offer for a day. (Reminders
// are sent by the follow-up policy; this tells the planner.)
type OfferUnanswered struct{}

func (OfferUnanswered) Name() string { return "offer_unanswered" }
func (OfferUnanswered) Detect(c *Context) []Signal {
	var out []Signal
	for _, a := range c.Assignments {
		if a.Status != delegation.Offered || c.Workers[a.Worker].Kind != workforce.Human || c.Now.Sub(a.CreatedAt) < 24*time.Hour {
			continue
		}
		out = append(out, Signal{Key: "offer_unanswered:" + string(a.ID), Kind: "offer_unanswered", Level: Medium,
			Subject: "assignment:" + string(a.ID), Project: a.Project,
			Summary: fmt.Sprintf("%s 一天没回复是否接下 %s（已提醒 %d 次）", a.Worker, a.ID, len(a.Nudges())),
			Suggest: []string{"reach_differently", "reassign", "ask_principal"}, People: []WorkerID{a.Worker}, Since: a.CreatedAt})
	}
	return out
}

// Overload: a person carries too much across projects.
type Overload struct{ Max int }

func (Overload) Name() string { return "overload" }
func (o Overload) Detect(c *Context) []Signal {
	limit := o.Max
	if limit <= 0 {
		limit = 5
	}
	var out []Signal
	for id, wk := range c.Workers {
		if wk.Kind != workforce.Human {
			continue
		}
		open := c.OpenAssignments(id)
		if len(open) < limit {
			continue
		}
		projects := map[ProjectID]bool{}
		for _, a := range open {
			projects[a.Project] = true
		}
		out = append(out, Signal{Key: "overload:" + string(id), Kind: "overload", Level: Low, Subject: "worker:" + string(id),
			Summary: fmt.Sprintf("%s 手上有 %d 件事（%d 个项目）", id, len(open), len(projects)),
			Suggest: []string{"rebalance"}, People: []WorkerID{id}, Facts: map[string]any{"open": len(open), "projects": len(projects)}})
	}
	return out
}

// AssigneeUnavailable: whoever has an item with a near deadline can't be reached.
type AssigneeUnavailable struct{}

func (AssigneeUnavailable) Name() string { return "assignee_unavailable" }
func (AssigneeUnavailable) Detect(c *Context) []Signal {
	var out []Signal
	for _, it := range c.Items {
		if !it.Open() || it.Assignee == "" || it.Due == nil || it.Due.Sub(c.Now) > 72*time.Hour {
			continue
		}
		if ok, why := c.State(it.Assignee).Available(c.Now); !ok {
			lv := Medium
			if it.Due.Sub(c.Now) < 24*time.Hour {
				lv = High
			}
			out = append(out, itemSignal("assignee_unavailable", lv, it, fmt.Sprintf("「%s」的执行者 %s 不可用（%s）", it.Title, it.Assignee, why),
				[]string{"reassign", "move_due"}))
		}
	}
	return out
}

// Routine: the same kind of work keeps coming back and nothing has been
// built to make it cheaper — a tool, a skill, a dedicated worker.
type Routine struct{ Min int }

func (Routine) Name() string { return "routine" }
func (r Routine) Detect(c *Context) []Signal {
	limit := r.Min
	if limit <= 0 {
		limit = 3
	}
	done, invested := map[string]int{}, map[string]bool{}
	for _, it := range c.Items {
		rt := it.Node.Routine
		if rt == "" || it.Status == planning.Dropped {
			continue
		}
		if it.Node.Compounding {
			invested[rt] = true
		} else if it.Status == planning.Done {
			done[rt]++
		}
	}
	var out []Signal
	for rt, n := range done {
		if n < limit || invested[rt] {
			continue
		}
		out = append(out, Signal{Key: "routine:" + rt, Kind: "routine", Level: Medium, Subject: "routine:" + rt,
			Summary: fmt.Sprintf("「%s」已经做了 %d 次，还没有沉淀成工具或专门的 worker", rt, n),
			Suggest: []string{"build_tool", "train_worker"}, Facts: map[string]any{"routine": rt, "times": n}})
	}
	return out
}

// Exploration watches what goes wrong when exploring — not missed dates, but
// losing touch with the principal, running past the timebox, and not
// noticing that the open questions are answered.
type Exploration struct{ ReviewEvery time.Duration }

func (Exploration) Name() string { return "exploration" }
func (e Exploration) Detect(c *Context) []Signal {
	every := e.ReviewEvery
	if every <= 0 {
		every = 72 * time.Hour
	}
	var out []Signal
	for id, p := range c.Projects {
		pl := p.Plan
		if pl.Kind != portfolio.KindExplore || pl.Stage != portfolio.PlanActive || !p.Open() {
			continue
		}
		mk := func(kind string, lv Level, summary string, suggest ...string) Signal {
			return Signal{Key: kind + ":" + string(id), Kind: kind, Level: lv, Subject: "project:" + string(id), Project: id,
				Summary: summary, Suggest: suggest, Facts: map[string]any{"round": pl.Round, "open_questions": len(pl.Questions)}}
		}
		last := pl.LastReview
		if last.IsZero() {
			last = p.CreatedAt
		}
		if gap := c.Now.Sub(last); gap > every {
			out = append(out, mk("explore_unreviewed", Medium,
				fmt.Sprintf("「%s」第 %d 轮已经 %s 没和委托人对过了：拿出具体的东西给他看，别在可能偏了的方向上继续做", p.Title, pl.Round, gap.Round(time.Hour)),
				"show_and_ask", "record_learning"))
		}
		if until := p.Timebox.Until; until != nil {
			total := until.Sub(p.CreatedAt)
			if c.Now.After(*until) {
				out = append(out, mk("explore_timebox", High, fmt.Sprintf("「%s」的探索时间盒已过（%s），还有 %d 个问题没答案：收敛、延长，还是停", p.Title, fmtT(*until), len(pl.Questions)),
					"converge", "extend", "stop"))
			} else if total > 0 && c.Now.Sub(p.CreatedAt) > total*4/5 && len(pl.Questions) > 0 {
				out = append(out, mk("explore_timebox", Medium, fmt.Sprintf("「%s」探索时间盒用了八成，还有 %d 个问题没答案", p.Title, len(pl.Questions)),
					"narrow", "converge"))
			}
		}
		if len(pl.Questions) == 0 && len(pl.Learned) > 0 {
			out = append(out, mk("explore_converged", Medium, fmt.Sprintf("「%s」列出的问题都有答案了：考虑收敛成执行计划", p.Title), "converge"))
		}
	}
	return out
}
