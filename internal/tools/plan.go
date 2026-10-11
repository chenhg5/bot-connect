package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

func addPlan(r *Registry, env Env) {
	pm := env.App
	node := obj(props{
		"ref":         str("a short name other nodes use in depends_on (r1, d1…)"),
		"id":          str("an existing item id, to change that node"),
		"title":       str("what must come out of it"),
		"done":        arr("acceptance criteria"),
		"executor":    str("worker id or name meant to do it; omit when it depends on a decision not made yet"),
		"why":         enum("required when the executor is a person", "approval", "decision", "physical", "relationship", "judgment", "knowledge", "capability"),
		"output":      enum("what it produces", "findings", "draft", "decision", "approval", "change", "action"),
		"depends_on":  arr("refs or item ids it needs first — only real dependencies; everything else runs in parallel"),
		"estimate":    str("effort: 30m | 4h | 1d"),
		"due":         str("deadline in the person's words, if there is one"),
		"brief":       str("instructions for whoever does it, beyond the title"),
		"compounding": boolean("an investment for repeated work: a tool, a skill, a dedicated worker"),
		"routine":     str("the kind of repeated work it belongs to (release, weekly_report…)"),
		"milestone":   boolean("a milestone, not work"),
	}, "title")
	r.add(Tool{
		Name: "plan",
		Description: "Your plan for a project — how you get its objective done through workers. " +
			"draft: write a revision (nodes with who, why, output, real dependencies); bot-connect checks the structure, computes parallel batches and the critical path, " +
			"and starts exploratory agent work (findings / drafts) right away. propose: ask everyone with a node to confirm their part (an outside review is attached). " +
			"agree: record that the principal agreed (only in a turn where they said so). The plan runs once everyone agreed; ready nodes are handed out automatically. " +
			"status: the plan now. A changed plan is a new revision and needs agreeing again.",
		PrincipalOnly: true,
		Schema: obj(props{
			"action":           enum("", "draft", "propose", "agree", "status"),
			"project":          str("project id or title"),
			"kind":             enum("draft: goal = an outcome whose path is open (judge feasibility first); task = a known path", "goal", "task"),
			"feasibility":      enum("draft: your verdict, backed by findings", "unknown", "feasible", "conditional", "infeasible"),
			"feasibility_note": str("draft: why, citing the findings"),
			"assumptions":      arr("draft: what must hold"),
			"stop_if":          arr("draft: when to stop or rethink"),
			"replace":          boolean("draft: the nodes listed are the whole plan; open nodes not listed are dropped"),
			"nodes":            map[string]any{"type": "array", "items": node, "description": "draft: the nodes"},
		}, "action", "project"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			pid := projectArg(pm, a)
			if pid == OrgProject {
				return "", &ToolError{KindInvalid, "plans belong to a project: create one with project_setup first"}
			}
			by := actorOf(pm, tc)
			switch s(a, "action") {
			case "status":
				out := pm.DescribePlan(pid)
				if out == "" {
					return "", &ToolError{KindNotFound, "no project " + string(pid)}
				}
				return out, nil
			case "draft":
				if _, _, ok := pm.PlanStatus(pid); !ok {
					pr := P2
					if v := s(a, "priority"); v != "" {
						x, err := ParsePriority(v)
						if err != nil {
							return "", &ToolError{KindInvalid, err.Error()}
						}
						pr = x
					}
					p, err := pm.CreateProject(ctx, by, app.ProjectSpec{Title: s(a, "project"), Priority: pr,
						Objective: portfolio.Objective{Text: s(a, "objective")}, Home: tc.ConvKey})
					if err != nil {
						return "", domainErr(err)
					}
					pid = p.ID
				}
				d, err := planDraft(pm, tc, pid, a)
				if err != nil {
					return "", err
				}
				res, err := pm.DraftPlan(ctx, by, d)
				if err != nil {
					return "", domainErr(err)
				}
				out := pm.DescribePlan(pid)
				if len(res.Picked) > 0 {
					out += "\n没写执行者的调研 / 起草节点，按 find_people 自动指定了 agent：" + strings.Join(res.Picked, "、")
				}
				if started := pm.StartedNodes(pid); len(started) > 0 {
					out += "\n已经派出去在做（不用等同意）：" + strings.Join(started, "、")
				}
				if len(res.Dropped) > 0 {
					out += fmt.Sprintf("\n已去掉：%v", res.Dropped)
				}
				return out + "\n下一步：先和委托人对齐（说清做法、谁做、为什么、风险），再 propose 请相关的人确认。", nil
			case "propose":
				res, err := pm.ProposePlan(ctx, by, pid)
				if err != nil {
					return "", domainErr(err)
				}
				out := pm.DescribePlan(pid)
				if len(res.Asked) > 0 {
					out += fmt.Sprintf("\n已请确认：%v（他们的答复会作为信号回来）", res.Asked)
				}
				if len(res.Notes) > 0 {
					out += "\n外部审视提出的疑点（改，或者想清楚为什么不改）：\n- " + strings.Join(res.Notes, "\n- ")
				}
				return out, nil
			case "agree":
				if !principalSpoke(pm, tc) {
					return "", &ToolError{KindPermission, "only the principal agrees to a plan, in a turn where they said so"}
				}
				p, err := pm.AgreePlan(ctx, by, pid)
				if err != nil {
					return "", domainErr(err)
				}
				out := pm.DescribePlan(pid)
				if p.Plan.Stage == portfolio.PlanProposed {
					out += "\n委托人已同意，还在等其他人确认。"
				}
				return out, nil
			}
			return "", &ToolError{KindInvalid, "action: draft | propose | agree | status"}
		},
	})
}

func planDraft(pm *app.App, tc TurnContext, pid ProjectID, a map[string]any) (app.PlanDraft, error) {
	d := app.PlanDraft{Project: pid, Replace: a["replace"] == true}
	if k := s(a, "kind"); k != "" {
		d.Spec.Kind = portfolio.GoalKind(k)
	}
	if v := s(a, "feasibility"); v != "" {
		verdict, err := portfolio.ParseVerdict(v)
		if err != nil {
			return d, domainErr(err)
		}
		d.Spec.Feasibility = &portfolio.Feasibility{Verdict: verdict, Note: s(a, "feasibility_note")}
	}
	if _, ok := a["assumptions"]; ok {
		d.Spec.Assumptions = strs(a, "assumptions")
	}
	if _, ok := a["stop_if"]; ok {
		d.Spec.StopIf = strs(a, "stop_if")
	}
	raw, _ := a["nodes"].([]any)
	for i, x := range raw {
		m, ok := x.(map[string]any)
		if !ok {
			return d, &ToolError{KindInvalid, fmt.Sprintf("nodes[%d] is not an object", i)}
		}
		out, err := planning.ParseOutput(s(m, "output"))
		if err != nil {
			return d, domainErr(err)
		}
		est, err := app.ParseEffort(s(m, "estimate"))
		if err != nil {
			return d, domainErr(err)
		}
		due, err := dueArg(pm, tc, s(m, "due"))
		if err != nil {
			return d, err
		}
		var why NeedKind
		if w := s(m, "why"); w != "" {
			if why, err = ParseNeedKind(w); err != nil {
				return d, &ToolError{KindInvalid, err.Error()}
			}
		}
		d.Nodes = append(d.Nodes, app.NodeSpec{ID: ItemID(s(m, "id")), Ref: s(m, "ref"), Title: s(m, "title"), Done: strsOrNil(m, "done"),
			Executor: s(m, "executor"), Why: why, Output: out, DependsOn: strs(m, "depends_on"), Estimate: est, Due: due,
			Brief: s(m, "brief"), Compounding: m["compounding"] == true, Routine: s(m, "routine"), Milestone: m["milestone"] == true})
	}
	return d, nil
}

func strsOrNil(a map[string]any, k string) []string {
	if _, ok := a[k]; !ok {
		return nil
	}
	return strs(a, k)
}

// focusNeedsPlan: the message this turn handles was judged a goal or a
// multi-step request.
func focusNeedsPlan(pm *app.App, tc TurnContext) bool {
	for _, id := range tc.Focus {
		if x, ok := pm.Signal(id); ok && x.NeedsPlan {
			return true
		}
	}
	return false
}

// planExists: some project has a plan drafted in the last turns (the gate
// lets untracked help through once the work is planned).
func planExists(pm *app.App) bool {
	found := false
	pm.Store.Read(func(s *app.State) {
		for _, p := range s.Projects {
			found = found || (p.Plan.Stage != portfolio.NoPlan && p.Plan.Stage != portfolio.PlanDone)
		}
	})
	return found
}
