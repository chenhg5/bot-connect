package tools

import (
	"context"
	"strings"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

func addWakeup(r *Registry, env Env) {
	pm := env.App
	r.add(Tool{
		Name: "wakeup",
		Description: "Have bot-connect wake you later: once (when: 周五 15:00 / 明天上午 / 2h) or on a rhythm (every: cron \"0 9 * * 1\" = Mondays 9:00, @every 1d, @daily, " +
			"optional \"TZ=Asia/Shanghai \" prefix). At the time, a wakeup signal with your note arrives and wakes you. Use it for things you must remember to DO later — " +
			"follow up on a promise, prepare a weekly report, check back after someone's holiday. To know when something CHANGES, use watch instead (it costs no turns). " +
			"The overview shows the next wakeups; list / cancel them here.",
		PrincipalOnly: true,
		Schema: obj(props{
			"action":  enum("", "add", "list", "cancel"),
			"note":    str("add: what to do when woken — self-contained (who, what, why, where to look)"),
			"when":    str("add, once: in the person's words or a duration"),
			"every":   str("add, recurring: cron / @every / @daily"),
			"project": str("add: the project it's about"),
			"item":    str("add: the item it's about"),
			"id":      str("cancel: wakeup id"),
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
				w, err := pm.AddWakeup(ctx, by, s(a, "note"), when, s(a, "every"), pid, ItemID(s(a, "item")))
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
