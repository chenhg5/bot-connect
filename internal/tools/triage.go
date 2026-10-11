package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/chenhg5/bot-connect/internal/app"
)

func addTriage(r *Registry, env Env) {
	pm := env.App
	r.add(Tool{
		Name: "triage",
		Description: "Sort a pile before doing any of it: each thing goes into the bucket that decides how it is handled — " +
			"handoff (delegate whole), program (plan), goal (plan, feasibility first), explore (plan rounds with the principal), decision (someone must choose), " +
			"question (answer), info (note it), drop (say why not) — with urgency and whether the principal must be involved. " +
			"Use it when several things arrive at once (a list from the principal, a full inbox) or when you're unsure what kind of thing something is. " +
			"Check the placements that look wrong or uncertain, then work bucket by bucket.",
		PrincipalOnly: true,
		Schema: obj(props{
			"things": arr("the things, one per entry, each self-contained"),
			"inbox":  boolean("sort everything waiting in the inbox instead"),
			"about":  str("context that helps place them (the project, what the principal is focused on)"),
		}),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			things := strs(a, "things")
			var ids []string
			if a["inbox"] == true {
				for _, x := range pm.Inbox(false) {
					things = append(things, fmt.Sprintf("%s：%s", x.ID, oneLineT(firstNonEmptyT(x.Body, x.Summary), 300)))
					ids = append(ids, x.ID)
				}
			}
			ss, err := pm.Sort(ctx, things, s(a, "about"))
			if err != nil {
				return "", domainErr(err)
			}
			out := app.DescribeSorted(ss)
			if len(ids) > 0 {
				out += "\n（来自收件箱：处理完用 signal_update 记下结果）"
			}
			return out, nil
		},
	})
}

func oneLineT(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func firstNonEmptyT(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}
