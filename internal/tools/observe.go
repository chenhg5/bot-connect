package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/inbox"

	"github.com/chenhg5/bot-connect/internal/domain/observe"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

func probeSchema() props {
	return props{
		"kind":   enum("file = read a file; dir = list a directory tree with modification times; cmd = an allowed read-only command; http = GET an allowed host", "file", "dir", "cmd", "http"),
		"target": str("file / directory path, URL, or the command's program (gh, git, hp, linear…)"),
		"args":   arr("cmd: arguments, one per entry (no shell)"),
		"dir":    str("cmd: working directory (inside the open roots)"),
		"depth":  num("dir: how deep to list (default 2, max 4)"),
	}
}

func probeArg(a map[string]any) observe.Probe {
	return observe.Probe{Kind: observe.Kind(s(a, "kind")), Target: s(a, "target"), Args: strs(a, "args"), Dir: s(a, "dir"), Depth: n(a, "depth", 0)}
}

func addObserve(r *Registry, env Env) {
	pm := env.App
	r.add(Tool{
		Name: "look",
		Description: "Look at where the truth of a project lives: read a file, list a directory (with modification times — is anything moving?), run an allowed read-only command " +
			"(git / gh read commands, plus what the principal allowed), or fetch an allowed page. Cheap, bounded checks only; hand deep analysis to an agent. Secrets are masked.",
		PrincipalOnly: true,
		Schema:        obj(probeSchema(), "kind", "target"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			out, err := pm.Look(ctx, actorOf(pm, tc), probeArg(a))
			if err != nil {
				if out != "" {
					return "", domainErr(fmt.Errorf("%w\n%s", err, clipT(out, 2000)))
				}
				return "", domainErr(err)
			}
			if strings.TrimSpace(out) == "" {
				return "(empty)", nil
			}
			return out, nil
		},
	})
	r.add(Tool{
		Name: "dossier",
		Description: "Your handbook for following one project (you write it, it is shown whenever you work on the project): its goals and when each is done; " +
			"where the truth lives and how to read it (sources: the exact probes); who or what does the work and how to reach them; the rhythm; what counts as progress and as a risk; " +
			"what the principal wants to hear about. get to read; write to replace (keep it a handbook: under 6000 characters).",
		PrincipalOnly: true,
		Schema: obj(props{
			"action":  enum("", "get", "write"),
			"project": str("project id or title"),
			"text":    str("write: the whole handbook (markdown)"),
			"sources": map[string]any{"type": "array", "description": "write: where the truth lives", "items": obj(props{
				"name": str("what it is (目标文件, 开着的 PR, 工作日志…)"), "what": str("what it tells you"),
				"kind": probeSchema()["kind"], "target": probeSchema()["target"], "args": probeSchema()["args"], "dir": probeSchema()["dir"],
			}, "name", "kind", "target")},
		}, "action", "project"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			pid := projectArg(pm, a)
			switch s(a, "action") {
			case "get":
				out := pm.DescribeObserved(pid)
				if out == "" {
					return "(no dossier yet for " + string(pid) + ": look around, then write one)", nil
				}
				return out, nil
			case "write":
				var sources []observe.Source
				raw, _ := a["sources"].([]any)
				for _, x := range raw {
					m, _ := x.(map[string]any)
					if m == nil {
						continue
					}
					sources = append(sources, observe.Source{Name: s(m, "name"), What: s(m, "what"), Probe: probeArg(m)})
				}
				d, err := pm.WriteDossier(ctx, actorOf(pm, tc), pid, s(a, "text"), sources)
				if err != nil {
					return "", domainErr(err)
				}
				return fmt.Sprintf("dossier of %s saved (%d characters, %d sources). Next: watch the sources that show progress.", pid, len([]rune(d.Text)), len(d.Sources)), nil
			}
			return "", &ToolError{KindInvalid, "action: get | write"}
		},
	})
	sch := probeSchema()
	sch["action"] = enum("", "add", "list", "remove", "run")
	sch["project"] = str("project id or title")
	sch["name"] = str("add: what it keeps an eye on (开着的 PR, 工作日志, 目标状态…)")
	sch["every"] = str("add: how often to look (default 1d; at least 15m)")
	sch["stale_after"] = str("add: no change for this long is news (3d…); omit if silence is fine")
	sch["expect"] = str("add: what progress looks like here, so a change can be judged")
	sch["id"] = str("remove / run: watch id")
	r.add(Tool{
		Name: "watch",
		Description: "Keep an eye on a source without spending your attention: bot-connect looks on a rhythm, compares, and wakes you only when it changed (with the diff), " +
			"kept failing, or stayed the same longer than stale_after. add looks once right away (a probe that doesn't work is refused). list, remove, run (look now).",
		PrincipalOnly: true,
		Schema:        obj(sch, "action"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			by := actorOf(pm, tc)
			switch s(a, "action") {
			case "add":
				every, err := spanArg(firstNonEmptyT(s(a, "every"), "1d"))
				if err != nil {
					return "", &ToolError{KindInvalid, "every: " + err.Error()}
				}
				stale, err := spanArg(s(a, "stale_after"))
				if err != nil {
					return "", &ToolError{KindInvalid, "stale_after: " + err.Error()}
				}
				w, first, err := pm.AddWatch(ctx, by, projectArg(pm, a), s(a, "name"), probeArg(a), every, stale, s(a, "expect"))
				if err != nil {
					return "", domainErr(err)
				}
				return fmt.Sprintf("%s watches %s every %s. First look (the baseline):\n%s", w.ID, w.Probe, every, clipT(first, 1500)), nil
			case "list":
				var pid ProjectID
				if s(a, "project") != "" {
					pid = projectArg(pm, a)
				}
				var b strings.Builder
				for _, w := range pm.Watches(pid) {
					fmt.Fprintf(&b, "%s [%s]「%s」每 %s：%s；上次变化 %s\n", w.ID, w.Project, w.Name, w.Every, w.Probe, w.ChangedAt.Format("01-02 15:04"))
				}
				if b.Len() == 0 {
					return "(no watches)", nil
				}
				return b.String(), nil
			case "remove":
				if err := pm.RemoveWatch(ctx, by, s(a, "id")); err != nil {
					return "", domainErr(err)
				}
				return "removed " + s(a, "id"), nil
			case "run":
				for _, w := range pm.Watches("") {
					if w.ID == s(a, "id") {
						out, err := pm.Look(ctx, by, w.Probe)
						if err != nil {
							return "", domainErr(err)
						}
						return out, nil
					}
				}
				return "", &ToolError{KindNotFound, "no watch " + s(a, "id")}
			}
			return "", &ToolError{KindInvalid, "action: add | list | remove | run"}
		},
	})
	r.add(Tool{
		Name: "observe_allow",
		Description: "Allow probes to run another read-only command pattern, e.g. \"hp * get\", \"linear issue list\" (\"*\" = one non-flag argument). " +
			"Only in a turn where the principal agreed to it; never allow anything that writes.",
		PrincipalOnly: true,
		Schema:        obj(props{"pattern": str("program and read-only subcommand(s)")}, "pattern"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			if !principalSpoke(pm, tc) {
				return "", &ToolError{KindPermission, "ask the principal first; record it in the turn where they agree"}
			}
			if err := pm.AllowCommand(ctx, actorOf(pm, tc), s(a, "pattern")); err != nil {
				return "", domainErr(err)
			}
			return "allowed: " + s(a, "pattern"), nil
		},
	})
}

func addAct(r *Registry, env Env) {
	pm := env.App
	r.add(Tool{
		Name: "act",
		Description: "Do something outside the bot: run a command (cmd: program + args, no shell), write a file (write: path + content, append or replace), " +
			"or hand a message to an existing agent session (session: harness = claude / codex / …, session id, message, dir = its project folder; its reply comes back as a signal). " +
			"Every action needs the principal: a standing permission (act_allow) covers it, or they agree to it. A direct request from them (\"把 X 改成 Y\") is agreement to that one action: " +
			"confirmed=true covers exactly one action per message of theirs. For anything more — or anything they didn't spell out — propose the actions (kind=propose), show them the list, " +
			"and after they agree carry it out with kind=proposal id=… confirmed=true; it runs exactly as proposed. Never do more than they agreed to.",
		PrincipalOnly: true,
		Schema: obj(props{
			"kind": enum("cmd / write / session = one action; propose = put several actions (actions[]) to the principal; proposal = carry out a proposal (id) they agreed to",
				"cmd", "write", "session", "propose", "proposal"),
			"id":        str("proposal: its id"),
			"actions":   map[string]any{"type": "array", "description": "propose: the actions, each with kind and its fields", "items": map[string]any{"type": "object"}},
			"project":   str("the project this is for"),
			"program":   str("cmd: program name"),
			"args":      arr("cmd: arguments"),
			"dir":       str("cmd / session: working directory"),
			"path":      str("write: file path"),
			"content":   str("write: what to write"),
			"append":    boolean("write: add to the end instead of replacing"),
			"harness":   str("session: claude | codex | … (as configured)"),
			"session":   str("session: its id"),
			"message":   str("session: what to tell it — self-contained"),
			"confirmed": boolean("only in the turn where the principal agreed to exactly this action"),
		}, "kind"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			confirmed := a["confirmed"] == true
			if confirmed && !principalSpoke(pm, tc) {
				return "", &ToolError{KindPermission, "confirmed=true only in a turn where the principal agreed to this action"}
			}
			by := actorOf(pm, tc)
			var pid ProjectID
			if s(a, "project") != "" {
				pid = projectArg(pm, a)
			}
			switch s(a, "kind") {
			case "propose":
				raw, _ := a["actions"].([]any)
				var acts []observe.Action
				for _, x := range raw {
					if m, ok := x.(map[string]any); ok {
						acts = append(acts, actionArg(m))
					}
				}
				p, err := pm.ProposeActs(ctx, by, pid, acts)
				if err != nil {
					return "", domainErr(err)
				}
				return p.Describe() + "\n把这个清单原样给委托人看；他同意后，在那一轮用 kind=proposal id=" + p.ID + " confirmed=true 执行，内容会按清单原样执行。", nil
			case "proposal":
				if !confirmed {
					return "", &ToolError{KindInvalid, "carry out a proposal only with confirmed=true, in the turn where the principal agreed to it"}
				}
				lines, err := pm.CarryOut(ctx, by, s(a, "id"))
				if err != nil {
					return "", domainErr(err)
				}
				return strings.Join(lines, "\n"), nil
			}
			act := actionArg(a)
			out, err := pm.Act(ctx, by, pid, act, confirmed, principalTurn(pm, tc))
			if err != nil {
				if out != "" {
					return "", domainErr(fmt.Errorf("%w\n%s", err, clipT(out, 1500)))
				}
				return "", domainErr(err)
			}
			return firstNonEmptyT(out, "done: "+act.Describe()), nil
		},
	})
	r.add(Tool{
		Name: "act_allow",
		Description: "Record a standing permission the principal just gave, so routine actions don't need asking each time: \"write <path glob>\" (\"**\" = anything below, " +
			"e.g. write ~/code/projects/*/handoffs/**), \"cmd <program> <subcommand…>\" (\"*\" = one argument), \"session <harness> <session id|*>\". " +
			"Only in a turn where the principal said so; keep it as narrow as what they agreed to. revoke=true removes one.",
		PrincipalOnly: true,
		Schema:        obj(props{"pattern": str("the permission"), "revoke": boolean("remove it instead")}, "pattern"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			if !principalSpoke(pm, tc) {
				return "", &ToolError{KindPermission, "only in a turn where the principal gave (or took back) this permission"}
			}
			by := actorOf(pm, tc)
			if a["revoke"] == true {
				if err := pm.RevokeAct(ctx, by, s(a, "pattern")); err != nil {
					return "", domainErr(err)
				}
				return "revoked: " + s(a, "pattern"), nil
			}
			if err := pm.AllowAct(ctx, by, s(a, "pattern")); err != nil {
				return "", domainErr(err)
			}
			return "permission recorded: " + s(a, "pattern") + "\nall standing permissions: " + strings.Join(pm.Grants(), "; "), nil
		},
	})
}

// spanArg reads "30m", "4h", "3d", "1w"; "" is zero.
func spanArg(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	if k := len(v); k > 1 && (v[k-1] == 'd' || v[k-1] == 'w') {
		var x int
		if _, err := fmt.Sscanf(v[:k-1], "%d", &x); err != nil {
			return 0, err
		}
		day := 24 * time.Hour
		if v[k-1] == 'w' {
			day *= 7
		}
		return time.Duration(x) * day, nil
	}
	return time.ParseDuration(v)
}

func clipT(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func actionArg(a map[string]any) observe.Action {
	return observe.Action{Kind: observe.ActKind(s(a, "kind")), Program: s(a, "program"), Args: strs(a, "args"), Dir: s(a, "dir"),
		Path: s(a, "path"), Content: s(a, "content"), Append: a["append"] == true,
		Harness: s(a, "harness"), Session: s(a, "session"), Message: s(a, "message")}
}

// principalTurn identifies the principal's message this turn handles (its
// single confirmation belongs to it).
func principalTurn(pm *app.App, tc TurnContext) string {
	var ids []string
	for _, id := range tc.Focus {
		if x, ok := pm.Signal(id); ok && (x.Source == inbox.DirectMessage || x.Source == inbox.Mention) && x.Actor.Privileged() {
			ids = append(ids, id)
		}
	}
	return strings.Join(ids, ",")
}
