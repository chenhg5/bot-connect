package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/attention"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/insight"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/workforce"
	"github.com/chenhg5/bot-connect/internal/identity"
)

// actorOf maps the turn's caller to a domain actor (and the worker they are).
func actorOf(pm *app.App, tc TurnContext) Actor {
	u := tc.Caller
	a := Actor{UserID: u.ID, Role: Role(u.Role), Via: "brain"}
	if u.Role == identity.RoleOwner {
		a.Worker = Owner
	} else if id, ok := pm.WorkerFor(u.ID, u.UnionID, u.Email); ok {
		a.Worker = id
	}
	return a
}

// domainErr gives domain errors their tool error kinds.
func domainErr(err error) error {
	var de *DomainError
	if errors.As(err, &de) {
		kind := map[string]string{"invalid": KindInvalid, "forbidden": KindPermission, "not_found": KindNotFound}[de.Kind]
		if kind == "" {
			kind = "conflict"
		}
		return &ToolError{kind, de.Msg}
	}
	return err
}

func strs(a map[string]any, k string) []string {
	switch v := a[k].(type) {
	case []any:
		var out []string
		for _, x := range v {
			if s := strings.TrimSpace(fmt.Sprint(x)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		var out []string
		for _, x := range strings.FieldsFunc(v, func(r rune) bool { return r == ';' || r == '；' || r == '\n' }) {
			if x = strings.TrimSpace(x); x != "" {
				out = append(out, x)
			}
		}
		return out
	}
	return nil
}

func arr(d string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": d}
}

func enum(d string, vals ...string) map[string]any {
	return map[string]any{"type": "string", "enum": vals, "description": d}
}

func parseNeeds(v []string) ([]planning.Need, error) {
	var out []planning.Need
	for _, x := range v {
		kind, detail, _ := strings.Cut(x, ":")
		k, err := ParseNeedKind(strings.TrimSpace(kind))
		if err != nil {
			return nil, &ToolError{KindInvalid, err.Error()}
		}
		out = append(out, planning.Need{Kind: k, Detail: strings.TrimSpace(detail)})
	}
	return out, nil
}

func addPM(r *Registry, env Env) {
	pm := env.App

	r.add(Tool{
		Name: "brief",
		Description: "The page you work from: the agenda computed by bot-connect (Now / Today / Watch, with keys and suggested actions) and every active project's card " +
			"(progress, health, next milestone, members and roles, open items). Already in your context each turn; call it again after changes, or with project=… for one project in full.",
		OwnerOnly: true,
		Schema:    obj(props{"project": str("one project in full (card, decisions, conventions, all open items)")}),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			out := pm.Briefing(ctx, app.BriefingOptions{Focus: ProjectID(s(a, "project")), MaxItems: n(a, "max_items", 8)})
			if out == "" {
				return "(nothing to manage yet: no projects, no open items)", nil
			}
			return out, nil
		},
	})
	r.add(Tool{
		Name: "find_people",
		Description: "Who should do something, ranked with reasons: project roles first, then company roles, skills, capabilities (agents first: a person is only a fallback when an agent can do it), " +
			"approval rules (resolved to whoever holds the role now), people who can act in the real world, and who already has the context. Unavailable or non-consenting workers are listed as excluded, with why.",
		OwnerOnly: true,
		Schema: obj(props{
			"need":       str("what is needed, in words: “定埋点方案”, “review PR touching media”"),
			"project":    str("project id (default: the company)"),
			"kind":       enum("why a particular kind of worker is needed; omit for ordinary work", "capability", "approval", "decision", "physical", "relationship", "judgment", "knowledge"),
			"capability": str("system access needed, e.g. repo:tapnow:write, gcloud-logging:read"),
			"action":     str("kind=approval: the action, e.g. merge:main, deploy:prod"),
			"scope":      str("kind=approval: its scope, e.g. repo:tapnow"),
			"item":       str("an item this continues (prefers whoever has the context)"),
		}),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			need := insight.Need{Project: ProjectID(firstLine(s(a, "project"), string(OrgProject))), Text: s(a, "need"), Kind: NeedKind(s(a, "kind")),
				Action: s(a, "action"), Scope: s(a, "scope"), Item: ItemID(s(a, "item")), By: actorOf(pm, tc)}
			if c := s(a, "capability"); c != "" {
				x := workforce.ParseCapability(c)
				need.Capability = &x
				if need.Kind == "" {
					need.Kind = NeedCapability
				}
			}
			cs := pm.Candidates(ctx, need)
			if len(cs) == 0 {
				return "(no candidates — nobody's role, skills or capabilities match; ask the owner who should do it)", nil
			}
			var b strings.Builder
			for _, c := range cs {
				kind := "人"
				if c.Agent {
					kind = "agent"
				}
				fmt.Fprintf(&b, "- %s（%s）分 %.1f：%s", c.Worker, kind, c.Score, strings.Join(c.Reasons, "；"))
				if c.Excluded != "" {
					b.WriteString(" ✗ " + c.Excluded)
				}
				b.WriteString("\n")
			}
			return b.String(), nil
		},
	})
	r.add(Tool{
		Name:        "plan_item",
		Description: "Add an item (a piece of work, or a milestone) to a project: title, acceptance criteria, due, effort, dependencies, and what it needs (needs: \"capability:repo:tapnow:write\", \"approval:deploy:prod\", \"decision:…\", \"physical:…\").",
		OwnerOnly:   true,
		Schema: obj(props{
			"project":    str("project id (default: the company)"),
			"title":      str("what must be done"),
			"parent":     str("parent item id (sub-item)"),
			"milestone":  boolean("this is a milestone"),
			"done":       arr("acceptance criteria, one per entry"),
			"due":        str("deadline in the person's words — 下周三 / 周五 18:00 / 明天下午 / 月底 / 17号 — or 2026-10-17 18:00 / 2h / 3d; bot-connect resolves it"),
			"estimate":   str("effort: 30m | 4h | 1d (= 8h)"),
			"priority":   enum("default: the project's", "P0", "P1", "P2", "P3"),
			"needs":      arr("kind:detail, e.g. capability:repo:tapnow:write, approval:deploy:prod"),
			"depends_on": arr("item ids this waits for"),
		}, "title"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			sp, err := itemSpec(pm, a)
			if err != nil {
				return "", err
			}
			it, err := pm.PlanItem(ctx, actorOf(pm, tc), sp)
			if err != nil {
				return "", domainErr(err)
			}
			out := "Planned " + string(it.ID) + "「" + it.Title + "」in " + string(it.Project)
			if it.Due != nil {
				out += ", due " + it.Due.Format("2006-01-02 15:04 Mon")
			}
			return out, nil
		},
	})
	r.add(Tool{
		Name: "delegate",
		Description: "Hand tracked work to a worker (agent, person, the owner) — it is followed up, risks are watched, and the result comes back here for review. " +
			"Give item=<id>, or project + goal to create the item on the way. Agents first: a person only when there is a reason (why) — approval, decision, physical, relationship, judgment, knowledge, or a capability no agent has. " +
			"kind=approval|decision|clarification|review asks for an answer (decision needs options); work is delivered and then verified with review. Keep the brief self-contained and minimal (an external worker sees only it).",
		OwnerOnly: true,
		Schema: obj(props{
			"worker":   str("worker id or name (roster in the brief); \"owner\" for the owner"),
			"item":     str("existing item id"),
			"project":  str("with goal: project to create the item in (default: the company)"),
			"goal":     str("what to do and why, self-contained"),
			"context":  str("only what the worker needs to know"),
			"done":     arr("acceptance criteria"),
			"evidence": str("what proof to hand in (link, commit, numbers…)"),
			"due":      str("deadline (see plan_item)"),
			"estimate": str("effort (see plan_item)"),
			"kind":     enum("default work", "work", "approval", "decision", "clarification", "action", "review"),
			"why":      enum("required for a person", "approval", "decision", "physical", "relationship", "judgment", "knowledge", "capability"),
			"options":  arr("kind=decision/approval: the choices"),
		}, "worker"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			by := actorOf(pm, tc)
			itemID := ItemID(s(a, "item"))
			if itemID == "" {
				if s(a, "goal") == "" {
					return "", &ToolError{KindInvalid, "give item, or goal (and project) to create one"}
				}
				sp, err := itemSpec(pm, map[string]any{"project": a["project"], "title": a["goal"], "done": a["done"], "due": a["due"], "estimate": a["estimate"]})
				if err != nil {
					return "", err
				}
				it, err := pm.PlanItem(ctx, by, sp)
				if err != nil {
					return "", domainErr(err)
				}
				itemID = it.ID
			}
			due, err := app.ParseWhen(s(a, "due"), pm.Clock.Now())
			if err != nil {
				return "", domainErr(err)
			}
			est, err := app.ParseEffort(s(a, "estimate"))
			if err != nil {
				return "", domainErr(err)
			}
			wid, err := pm.ResolveWorker(s(a, "worker"))
			if err != nil {
				return "", domainErr(err)
			}
			as, err := pm.Delegate(ctx, by, app.DelegateSpec{Item: itemID, Worker: wid, Kind: delegation.AskKind(s(a, "kind")),
				Why: NeedKind(s(a, "why")), Options: strs(a, "options"), Conv: tc.ConvKey,
				Brief: delegation.Brief{Goal: s(a, "goal"), Context: s(a, "context"), Done: strs(a, "done"), Evidence: s(a, "evidence"), Due: due, Estimate: est}})
			if err != nil {
				if as.ID != "" {
					return "", domainErr(fmt.Errorf("%s created but not delivered: %w", as.ID, err))
				}
				return "", domainErr(err)
			}
			dueText := ""
			if as.Brief.Due != nil {
				dueText = ", due " + as.Brief.Due.Format("2006-01-02 15:04 Mon")
			}
			return fmt.Sprintf("%s → %s（%s，status %s — not accepted yet unless it says accepted）on item %s%s; the result comes back here.", as.ID, as.Worker, as.Kind, as.Status, as.Item, dueText), nil
		},
	})
	r.add(Tool{
		Name:        "review",
		Description: "Requester side of an assignment: verify a delivery (check every acceptance criterion first), revise (say exactly what's missing), cancel, answer_question (reply to the worker's question), accept_counter (take their proposed deadline).",
		OwnerOnly:   true,
		Schema: obj(props{"assignment": str("assignment id, e.g. A12"), "action": enum("", "verify", "revise", "cancel", "answer_question", "accept_counter"),
			"note": str("what's missing / the answer / why")}, "assignment", "action"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			if err := pm.Review(ctx, actorOf(pm, tc), AssignmentID(s(a, "assignment")), s(a, "action"), s(a, "note")); err != nil {
				return "", domainErr(err)
			}
			as, _ := pm.Assignment(AssignmentID(s(a, "assignment")))
			return fmt.Sprintf("%s is now %s; the worker was told.", as.ID, as.Status), nil
		},
	})
	r.add(Tool{
		Name: "respond",
		Description: "Worker side of an assignment, for a person replying in chat (or the owner reporting on their behalf): accept, decline (reason), counter (new due), progress (note, eta), " +
			"ask (a question), deliver (result + evidence), answer (choice for an approval/decision/clarification/review), release (hand it back).",
		Schema: obj(props{
			"assignment": str("assignment id"),
			"action":     enum("", "accept", "decline", "counter", "progress", "ask", "deliver", "answer", "release"),
			"note":       str("reason / progress / question / remark"),
			"eta":        str("expected finish (accept / progress)"),
			"due":        str("counter: proposed deadline"),
			"result":     str("deliver: what was done"),
			"evidence":   arr("deliver: links, commits, numbers"),
			"choice":     str("answer: the chosen option (approve / reject / one of the options)"),
		}, "assignment", "action"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			id := AssignmentID(s(a, "assignment"))
			as, ok := pm.Assignment(id)
			if !ok {
				return "", &ToolError{KindNotFound, "no assignment " + string(id)}
			}
			by := actorOf(pm, tc)
			switch {
			case by.Worker == as.Worker:
			case by.Privileged():
				by.Worker, by.Via = as.Worker, "on_behalf"
			default:
				return "", &ToolError{KindPermission, "permission denied: " + string(id) + " is not assigned to you"}
			}
			now := pm.Clock.Now()
			eta, err := app.ParseWhen(s(a, "eta"), now)
			if err != nil {
				return "", domainErr(err)
			}
			due, err := app.ParseWhen(s(a, "due"), now)
			if err != nil {
				return "", domainErr(err)
			}
			choice := s(a, "choice")
			switch strings.ToLower(choice) {
			case "批准", "同意", "lgtm", "ok":
				choice = "approve"
			case "拒绝", "不同意":
				choice = "reject"
			}
			if err := pm.Report(ctx, by, id, app.Report{Action: s(a, "action"), Note: s(a, "note"), ETA: eta, Due: due,
				Result: s(a, "result"), Evidence: strs(a, "evidence"), Choice: choice}); err != nil {
				return "", domainErr(err)
			}
			as, _ = pm.Assignment(id)
			return fmt.Sprintf("%s is now %s.", id, as.Status), nil
		},
	})
	r.add(Tool{
		Name:        "resolve",
		Description: "Record what you did about an agenda entry (by its key) so it doesn't come back until it should: done | acted (waiting on others) | deferred | handed_to_owner | dismissed. acted / deferred / handed_to_owner come back at `until` (default 24h).",
		OwnerOnly:   true,
		Schema: obj(props{"key": str("agenda key, e.g. will_miss:I7"), "outcome": enum("", "done", "acted", "deferred", "handed_to_owner", "dismissed"),
			"until": str("when to look again (see plan_item due)"), "note": str("what was done")}, "key", "outcome"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			until, err := app.ParseWhen(s(a, "until"), pm.Clock.Now())
			if err != nil {
				return "", domainErr(err)
			}
			if err := pm.Resolve(ctx, actorOf(pm, tc), s(a, "key"), attention.Outcome(s(a, "outcome")), until, s(a, "note")); err != nil {
				return "", domainErr(err)
			}
			return "Recorded " + s(a, "outcome") + " for " + s(a, "key"), nil
		},
	})
	r.add(Tool{
		Name:        "authorize",
		Description: "Before an action that may need approval (merge:main, deploy:prod, publish:external, spend…): checks the approval rules. Allowed → go ahead. Otherwise an approval request goes to whoever the rule names now (e.g. this week's on-call) and you get its assignment id; the answer comes back here.",
		OwnerOnly:   true,
		Schema:      obj(props{"item": str("the item the action belongs to"), "action": str("e.g. deploy:prod"), "scope": str("e.g. repo:tapnow"), "why": str("context for the approver")}, "item", "action"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			g, err := pm.Authorize(ctx, actorOf(pm, tc), ItemID(s(a, "item")), s(a, "action"), s(a, "scope"), s(a, "why"))
			if err != nil {
				return "", domainErr(err)
			}
			switch {
			case g.Allowed:
				return "allowed", nil
			case g.Assignment != nil:
				return fmt.Sprintf("needs approval: %s asked (%s, %s)", g.Assignment.Worker, g.Assignment.ID, g.Assignment.Status), nil
			}
			return "needs approval, but no approver is available", nil
		},
	})
	r.add(Tool{
		Name:        "project_setup",
		Description: "Start a project: title, priority, timebox, objective. Its messages and reports default to this conversation. Then add members and roles with project_update.",
		OwnerOnly:   true,
		Schema: obj(props{"title": str(""), "id": str("optional short id"), "parent": str("parent project (default: the company)"),
			"priority": enum("", "P0", "P1", "P2", "P3"), "from": str("start"), "until": str("end"), "objective": str("the outcome and how it's measured")}, "title"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			now := pm.Clock.Now()
			pr, err := ParsePriority(s(a, "priority"))
			if err != nil {
				return "", &ToolError{KindInvalid, err.Error()}
			}
			from, err := app.ParseWhen(s(a, "from"), now)
			if err != nil {
				return "", domainErr(err)
			}
			until, err := app.ParseWhen(s(a, "until"), now)
			if err != nil {
				return "", domainErr(err)
			}
			if from == nil && until != nil {
				from = &now
			}
			p, err := pm.CreateProject(ctx, actorOf(pm, tc), app.ProjectSpec{ID: ProjectID(s(a, "id")), Parent: ProjectID(s(a, "parent")), Title: s(a, "title"),
				Priority: pr, Timebox: Period{From: from, Until: until}, Objective: portfolio.Objective{Text: s(a, "objective")}, Home: tc.ConvKey})
			if err != nil {
				return "", domainErr(err)
			}
			return "Created project " + string(p.ID) + "「" + p.Title + "」", nil
		},
	})
	r.add(Tool{
		Name: "project_update",
		Description: "Change a project (\"org\" = the company): priority, status; add a member with a role, duties and period (member_*); end a membership; set an approval rule (policy_*); " +
			"add to the project card (card_*: scope, a decision with its why, a convention, a link). Several fields may be set at once.",
		OwnerOnly: true,
		Schema: obj(props{
			"project": str("project id, or org"), "priority": enum("", "P0", "P1", "P2", "P3"), "status": enum("", "active", "paused", "done", "cancelled"),
			"member_worker": str("add member: worker id"), "member_role": str("their role here"), "member_duties": arr("what to go to them for"),
			"member_from": str("role starts"), "member_until": str("role ends"),
			"end_member_worker": str("end a membership: worker id"), "end_member_role": str("…and role"),
			"policy_action": str("approval rule: action, e.g. deploy:prod"), "policy_scope": str("its scope"), "policy_approvers": arr("roles or worker ids"),
			"card_scope": str("set the scope"), "card_decision": str("add a decision (what and why)"), "card_convention": str("add a convention"), "card_link": str("add a link"),
		}, "project"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			by := actorOf(pm, tc)
			now := pm.Clock.Now()
			mf, err := app.ParseWhen(s(a, "member_from"), now)
			if err != nil {
				return "", domainErr(err)
			}
			mu, err := app.ParseWhen(s(a, "member_until"), now)
			if err != nil {
				return "", domainErr(err)
			}
			for _, k := range []string{"member_worker", "end_member_worker"} {
				if v := s(a, k); v != "" {
					id, err := pm.ResolveWorker(v)
					if err != nil {
						return "", domainErr(err)
					}
					a[k] = string(id)
				}
			}
			p, err := pm.ChangeProject(ctx, by, ProjectID(s(a, "project")), func(p *portfolio.Project, now time.Time) ([]Event, error) {
				var evs []Event
				add := func(e []Event, err error) error { evs = append(evs, e...); return err }
				if v := s(a, "priority"); v != "" {
					pr, err := ParsePriority(v)
					if err != nil {
						return nil, Invalid("%v", err)
					}
					evs = append(evs, p.SetPriority(pr, now, by)...)
				}
				if v := s(a, "status"); v != "" {
					if err := add(p.Transition(portfolio.Status(v), now, by)); err != nil {
						return nil, err
					}
				}
				if w := s(a, "member_worker"); w != "" {
					if err := add(p.AddMember(portfolio.Membership{Worker: WorkerID(w), Role: s(a, "member_role"), Duties: strs(a, "member_duties"),
						Period: Period{From: mf, Until: mu}}, now, by)); err != nil {
						return nil, err
					}
				}
				if w := s(a, "end_member_worker"); w != "" {
					if err := add(p.EndMembership(WorkerID(w), s(a, "end_member_role"), now, by)); err != nil {
						return nil, err
					}
				}
				if act := s(a, "policy_action"); act != "" {
					if err := add(p.SetPolicy(portfolio.ApprovalRule{Action: act, Scope: s(a, "policy_scope"), Approvers: strs(a, "policy_approvers")}, now, by)); err != nil {
						return nil, err
					}
				}
				d := portfolio.CardDelta{AddDecision: s(a, "card_decision"), AddConvention: s(a, "card_convention"), AddLink: s(a, "card_link")}
				if v := s(a, "card_scope"); v != "" {
					d.SetScope = &v
				}
				if d.SetScope != nil || d.AddDecision != "" || d.AddConvention != "" || d.AddLink != "" {
					if err := add(p.UpdateCard(d, now, by)); err != nil {
						return nil, err
					}
				}
				if len(evs) == 0 {
					return nil, Invalid("nothing to change")
				}
				return evs, nil
			})
			if err != nil {
				return "", domainErr(err)
			}
			return "Updated " + string(p.ID), nil
		},
	})
	r.add(Tool{
		Name:        "item_update",
		Description: "Change an item: a new due date (an open assignment follows and its worker is told), drop it (its assignment is cancelled), or mark it done without a delivery.",
		OwnerOnly:   true,
		Schema:      obj(props{"item": str("item id"), "due": str("new deadline"), "drop": str("drop it, with the reason"), "complete": boolean("mark done")}, "item"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			by, id := actorOf(pm, tc), ItemID(s(a, "item"))
			switch {
			case s(a, "drop") != "":
				if err := pm.DropItem(ctx, by, id, s(a, "drop")); err != nil {
					return "", domainErr(err)
				}
				return "Dropped " + string(id), nil
			case a["complete"] == true:
				if err := pm.CompleteItem(ctx, by, id); err != nil {
					return "", domainErr(err)
				}
				return "Completed " + string(id), nil
			case s(a, "due") != "":
				due, err := app.ParseWhen(s(a, "due"), pm.Clock.Now())
				if err != nil {
					return "", domainErr(err)
				}
				if err := pm.RescheduleItem(ctx, by, id, due); err != nil {
					return "", domainErr(err)
				}
				return "Rescheduled " + string(id) + " to " + due.Format("01-02 15:04"), nil
			}
			return "", &ToolError{KindInvalid, "give due, drop or complete"}
		},
	})
	r.add(Tool{
		Name:        "note_person",
		Description: "Record how someone is doing (presence: away/busy…, receptiveness: low/normal/high, load) with the evidence. A note about someone else is a guess: it expires within a day and is never shown to anyone but the owner. People can declare their own state.",
		Schema: obj(props{"worker": str("worker id"), "dim": enum("", "presence", "receptiveness", "load", "context"),
			"value": str("e.g. away, low"), "detail": str("evidence, briefly")}, "worker", "dim", "value"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			wid, err := pm.ResolveWorker(s(a, "worker"))
			if err != nil {
				return "", domainErr(err)
			}
			err = pm.NoteWorker(ctx, actorOf(pm, tc), wid, workforce.Fact{Dim: workforce.Dim(s(a, "dim")), Value: s(a, "value"), Detail: s(a, "detail")})
			if err != nil {
				return "", domainErr(err)
			}
			return "noted", nil
		},
	})
}

func itemSpec(pm *app.App, a map[string]any) (planning.Spec, error) {
	now := pm.Clock.Now()
	due, err := app.ParseWhen(s(a, "due"), now)
	if err != nil {
		return planning.Spec{}, domainErr(err)
	}
	est, err := app.ParseEffort(s(a, "estimate"))
	if err != nil {
		return planning.Spec{}, domainErr(err)
	}
	needs, err := parseNeeds(strs(a, "needs"))
	if err != nil {
		return planning.Spec{}, err
	}
	sp := planning.Spec{Project: ProjectID(firstLine(s(a, "project"), string(OrgProject))), Parent: ItemID(s(a, "parent")), Title: s(a, "title"),
		Acceptance: strs(a, "done"), Due: due, Estimate: est, Needs: needs}
	if a["milestone"] == true {
		sp.Kind = planning.Milestone
	}
	if v := s(a, "priority"); v != "" {
		pr, err := ParsePriority(v)
		if err != nil {
			return planning.Spec{}, &ToolError{KindInvalid, err.Error()}
		}
		sp.Priority = &pr
	}
	for _, d := range strs(a, "depends_on") {
		sp.DependsOn = append(sp.DependsOn, ItemID(d))
	}
	return sp, nil
}
