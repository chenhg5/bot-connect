package tools

import (
	"context"
	"strings"

	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/observe"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

func addWakeup(r *Registry, env Env) {
	pm := env.App
	r.add(Tool{
		Name: "wakeup",
		Description: "Have bot-connect wake you later: once (when: 周五 15:00 / 明天上午 / 2h) or on a rhythm (every: cron \"0 9 * * 1\" = Mondays 9:00, @every 1d, @daily, " +
			"optional \"TZ=Asia/Shanghai \" prefix). At the time, a wakeup signal with your note arrives and wakes you. Use it for things you must remember to DO later — " +
			"follow up on a promise, prepare a weekly report, check back after someone's holiday. To know when something CHANGES, use watch instead (it costs no turns). " +
			"A condition (if_*) is looked at by bot-connect first, at no cost: if it shows nothing (or fails) this time is skipped — \"weekdays 9:00, if there are open PRs\" " +
			"(if_kind=cmd if_target=gh if_args=[pr,list]) or an approved toolbox tool. Reminders the principal asks for fire on time; wakeups you give yourself (and rhythms) keep to the working hours and the day's budget. " +
			"The overview shows the next wakeups; list / cancel them here (the principal's routines are theirs to change).",
		PrincipalOnly: true,
		Schema: obj(props{
			"action":    enum("", "add", "list", "cancel"),
			"note":      str("add: what to do when woken — self-contained (who, what, why, where to look)"),
			"when":      str("add, once: in the person's words or a duration"),
			"every":     str("add, recurring: cron / @every / @daily"),
			"project":   str("add: the project it's about"),
			"item":      str("add: the item it's about"),
			"id":        str("cancel: wakeup id"),
			"if_kind":   enum("add, optional condition: file | dir | cmd | http | tool (see look)", "file", "dir", "cmd", "http", "tool"),
			"if_target": str("add: condition target"),
			"if_args":   arr("add: condition arguments"),
			"if_dir":    str("add: condition working directory"),
			"window":    boolean("add: keep to the working hours (default: yes for rhythms, no for one-off times)"),
		}, "action"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			by := actorOf(pm, tc)
			switch s(a, "action") {
			case "add":
				var pid ProjectID
				if s(a, "project") != "" {
					pid = projectArg(pm, a)
				}
				when := s(a, "when")
				if when != "" {
					if _, err := dueArg(pm, tc, when); err != nil {
						return "", err
					}
				}
				sp := app.WakeupSpec{Note: s(a, "note"), When: when, Every: s(a, "every"), Project: pid, Item: ItemID(s(a, "item")), Window: s(a, "every") != ""}
				sp.Asked = principalSpoke(pm, tc) // a reminder the principal asked for fires on time
				if v, ok := a["window"].(bool); ok {
					sp.Window = v
				}
				if s(a, "if_kind") != "" {
					sp.If = &observe.Probe{Kind: observe.Kind(s(a, "if_kind")), Target: s(a, "if_target"), Args: strs(a, "if_args"), Dir: s(a, "if_dir")}
				}
				w, err := pm.ScheduleWakeup(ctx, by, sp)
				if err != nil {
					return "", domainErr(err)
				}
				return "scheduled " + w.Describe(), nil
			case "list":
				var b strings.Builder
				for _, w := range pm.Wakeups() {
					b.WriteString("- " + w.Describe() + "\n")
				}
				if b.Len() == 0 {
					return "(no wakeups)", nil
				}
				return b.String(), nil
			case "cancel":
				if err := pm.CancelWakeup(ctx, by, s(a, "id")); err != nil {
					return "", domainErr(err)
				}
				return "cancelled " + s(a, "id"), nil
			}
			return "", &ToolError{KindInvalid, "action: add | list | cancel"}
		},
	})
}
