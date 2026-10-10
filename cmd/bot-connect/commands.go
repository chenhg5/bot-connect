package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	botconnect "github.com/chenhg5/bot-connect"
	"github.com/chenhg5/bot-connect/internal/audit"
	"github.com/chenhg5/bot-connect/internal/cli"
	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/schedule"
	"github.com/chenhg5/bot-connect/internal/session"
	"github.com/chenhg5/bot-connect/internal/tools"
	"github.com/chenhg5/bot-connect/internal/worker"
)

var configFlag = cli.Flag{Name: "config", Type: cli.String, Env: "BOT_CONNECT_CONFIG",
	Desc: "config file (default: ./config.toml if present, else ~/.bot-connect/config.toml)"}

func newApp() *cli.App {
	app := &cli.App{
		Stdout: os.Stdout, Stderr: os.Stderr, IsTTY: cli.StdoutIsTTY(), Getenv: os.Getenv,
		Global: []cli.Flag{{Name: "format", Type: cli.Enum, Enum: []string{"json", "table", "ndjson"},
			Desc: "output format (default: table in a terminal, json otherwise)"}},
	}
	app.Root = &cli.Command{
		Name:    "bot-connect",
		Summary: "one IM bot per person, driving Claude Code / Codex sessions",
		Examples: []string{
			"bot-connect config init                      # write ~/.bot-connect/config.toml",
			"bot-connect bot run                          # start the bots in the config",
			"bot-connect task list --format json          # recent tasks, for scripts and agents",
			"bot-connect schema --command \"worker list\"   # a command's flags as JSON",
		},
		Children: []*cli.Command{
			configCmd(), botCmd(), workerCmd(), templateCmd(), sessionCmd(), taskCmd(), scheduleCmd(), auditCmd(), feishuCmd(), toolCmd(),
			schemaCmd(app), versionCmd(),
		},
	}
	return app
}

// ---------- config ----------

func configPath(c *cli.Ctx) string {
	if p := c.Str("config"); p != "" {
		return config.ExpandHome(p)
	}
	if _, err := os.Stat("config.toml"); err == nil {
		return "config.toml"
	}
	return config.ExpandHome("~/.bot-connect/config.toml")
}

func loadConfig(c *cli.Ctx) (*config.Config, string, error) {
	p := configPath(c)
	if _, err := os.Stat(p); err != nil {
		return nil, p, cli.NotFound("no config at "+p, "run: bot-connect config init (or pass --config)")
	}
	cfg, err := config.Load(p)
	if err != nil {
		return nil, p, &cli.Error{Code: cli.ExitError, Type: "invalid_config", Message: err.Error(), Input: p,
			Suggestion: "fix the config (see https://github.com/chenhg5/bot-connect/blob/main/config.example.toml), then: bot-connect config validate"}
	}
	return cfg, p, nil
}

func configCmd() *cli.Command {
	return &cli.Command{Name: "config", Summary: "create, check and inspect the config file", Children: []*cli.Command{
		{
			Name: "init", Summary: "write a commented starter config", Effects: true,
			Examples: []string{"bot-connect config init", "bot-connect config init --path ./config.toml --dry-run"},
			Flags: []cli.Flag{
				{Name: "path", Type: cli.String, Default: "~/.bot-connect/config.toml", Desc: "where to write it"},
				{Name: "force", Type: cli.Bool, Desc: "overwrite an existing file"},
			},
			Run: func(c *cli.Ctx) error {
				p := config.ExpandHome(c.Str("path"))
				_, statErr := os.Stat(p)
				exists := statErr == nil
				if exists && !c.Bool("force") {
					return cli.Conflict(p+" already exists", "edit it, or pass --force to overwrite")
				}
				plan := map[string]any{"write": p, "bytes": len(botconnect.ConfigExample), "overwrite": exists}
				if c.Bool("dry-run") {
					return c.DryRun(plan)
				}
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(p, botconnect.ConfigExample, 0o600); err != nil {
					return err
				}
				return c.Out(map[string]any{"written": p, "next": "edit it, then: bot-connect config validate --config " + p}, func(w io.Writer) {
					fmt.Fprintf(w, "wrote %s\nnext: edit it, then run: bot-connect config validate --config %s\n", p, p)
				})
			},
		},
		{
			Name: "validate", Summary: "load the config and report problems (exit 0 = valid)",
			Examples: []string{"bot-connect config validate", "bot-connect config validate --config ./config.toml --format json"},
			Flags:    []cli.Flag{configFlag},
			Run: func(c *cli.Ctx) error {
				cfg, p, err := loadConfig(c)
				if err != nil {
					return err
				}
				warn := configWarnings(cfg)
				var missing []string
				for _, w := range cfg.Workers {
					if st, err := os.Stat(w.WorkDir); err != nil || !st.IsDir() {
						missing = append(missing, fmt.Sprintf("worker %s: work_dir %s is not a directory", w.Name, w.WorkDir))
					}
				}
				if len(missing) > 0 {
					return &cli.Error{Code: cli.ExitError, Type: "invalid_config", Message: strings.Join(missing, "; "), Input: p,
						Suggestion: "create the directory or fix work_dir"}
				}
				out := map[string]any{"valid": true, "config": p, "data_dir": cfg.DataDir, "bots": botRows(cfg), "workers": workerNames(cfg), "warnings": warn}
				return c.Out(out, func(w io.Writer) {
					fmt.Fprintf(w, "valid: %s\n", p)
					printBots(w, cfg)
					for _, s := range warn {
						fmt.Fprintf(w, "warning: %s\n", s)
					}
				})
			},
		},
		{
			Name: "show", Summary: "print the effective config, secrets redacted",
			Examples: []string{"bot-connect config show --format json"},
			Flags:    []cli.Flag{configFlag},
			Run: func(c *cli.Ctx) error {
				cfg, _, err := loadConfig(c)
				if err != nil {
					return err
				}
				red := redactConfig(cfg)
				return c.Out(red, func(w io.Writer) {
					b, _ := json.MarshalIndent(red, "", "  ")
					fmt.Fprintln(w, string(b))
				})
			},
		},
	}}
}

func configWarnings(cfg *config.Config) []string {
	w := []string{} // always an array in JSON
	for _, b := range cfg.Bots {
		if b.Feishu.AppID == "" && b.Feishu.LarkCLIProfile == "" {
			w = append(w, fmt.Sprintf("bot %s has no channel: set feishu.app_id or feishu.larkcli_profile (or run: bot-connect feishu setup)", b.Name))
		}
		if b.Brain.Agent == "codex" && b.Brain.Confine != nil && !*b.Brain.Confine {
			w = append(w, fmt.Sprintf("bot %s: codex brain without confine can read the whole disk", b.Name))
		}
		if b.Brain.Agent == "command" {
			w = append(w, fmt.Sprintf("bot %s: a command brain can read whatever its shell can", b.Name))
		}
		for _, m := range b.Members {
			if m == "*" {
				w = append(w, fmt.Sprintf("bot %s: members = [\"*\"] — everyone in the tenant can use per_user workers and ask read-only ones", b.Name))
			}
		}
	}
	for _, wk := range cfg.Workers {
		if wk.Access == "full" {
			w = append(w, fmt.Sprintf("worker %s: access = full — no sandbox", wk.Name))
		}
		if wk.Agent == "claudecode" && !worker.SandboxSupported() && wk.Confine != nil && *wk.Confine {
			w = append(w, fmt.Sprintf("worker %s: Claude Code has no OS sandbox on %s; confine is not enforced", wk.Name, runtime.GOOS))
		}
	}
	return w
}

func redactConfig(cfg *config.Config) any {
	b, _ := json.Marshal(cfg)
	var m any
	_ = json.Unmarshal(b, &m)
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, x := range t {
				lk := strings.ToLower(k)
				if (strings.Contains(lk, "secret") || strings.Contains(lk, "apikey") || lk == "env") && x != nil && x != "" {
					t[k] = "***"
					continue
				}
				walk(x)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(m)
	return m
}

// ---------- bot ----------

type botRow struct {
	Name    string   `json:"name"`
	Channel string   `json:"channel"`
	Brain   string   `json:"brain"`
	Owners  []string `json:"owners"`
	Admins  []string `json:"admins,omitempty"`
	Members []string `json:"members,omitempty"`
	Workers []string `json:"workers"`
	Dir     string   `json:"dir"`
}

func botRows(cfg *config.Config) []botRow {
	var out []botRow
	for _, b := range cfg.Bots {
		ch := "none"
		switch {
		case b.Feishu.LarkCLIProfile != "":
			ch = "feishu (lark-cli profile " + b.Feishu.LarkCLIProfile + ")"
		case b.Feishu.AppID != "":
			ch = "feishu (app " + b.Feishu.AppID + ")"
		}
		brain := b.Brain.Agent
		if b.Brain.Model != "" {
			brain += " / " + b.Brain.Model
		}
		owners := b.Owners
		if len(owners) == 0 {
			owners = []string{"(the Feishu app's owner)"}
		}
		ws := b.Workers
		if len(ws) == 0 {
			ws = []string{"*"}
		}
		out = append(out, botRow{Name: b.Name, Channel: ch, Brain: brain, Owners: owners, Admins: b.Admins, Members: b.Members, Workers: ws, Dir: b.Dir})
	}
	return out
}

func workerNames(cfg *config.Config) []string {
	var n []string
	for _, w := range cfg.Workers {
		n = append(n, w.Name)
	}
	return n
}

func printBots(w io.Writer, cfg *config.Config) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "BOT\tCHANNEL\tBRAIN\tWORKERS")
	for _, b := range botRows(cfg) {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", b.Name, b.Channel, b.Brain, strings.Join(b.Workers, ","))
	}
	tw.Flush()
}

func botCmd() *cli.Command {
	return &cli.Command{Name: "bot", Summary: "list and run the configured bots", Children: []*cli.Command{
		{
			Name: "list", Summary: "bots in the config: channel, brain, people, workers",
			Examples: []string{"bot-connect bot list"},
			Flags:    []cli.Flag{configFlag},
			Run: func(c *cli.Ctx) error {
				cfg, _, err := loadConfig(c)
				if err != nil {
					return err
				}
				return c.Out(botRows(cfg), func(w io.Writer) { printBots(w, cfg) })
			},
		},
		{
			Name: "run", Summary: "start the bots and keep them running (Ctrl-C to stop)",
			Desc: "Logs go to stderr (to <data_dir>/bot-connect.log with --console). Everything the bots do is\nrecorded in <data_dir>/audit/ — see: bot-connect audit list.",
			Examples: []string{
				"bot-connect bot run",
				"bot-connect bot run --console                 # chat from this terminal as the owner",
				"bot-connect bot run --only team-bot --config ./config.toml",
			},
			Flags: []cli.Flag{configFlag,
				{Name: "only", Type: cli.List, Desc: "run only this bot"},
				{Name: "console", Type: cli.Bool, Desc: "chat from this terminal as the owner (\"@name text\" speaks as a visitor)"},
				{Name: "console-bot", Type: cli.String, Desc: "with --console: which bot the terminal talks to (default: first)"},
			},
			Run: func(c *cli.Ctx) error {
				cfg, _, err := loadConfig(c)
				if err != nil {
					return err
				}
				return serve(cfg, serveOpts{Console: c.Bool("console"), ConsoleBot: c.Str("console-bot"), Only: c.List("only")})
			},
		},
	}}
}

// ---------- worker / task (state saved by the running bot) ----------

func openWorkers(c *cli.Ctx) (*config.Config, *worker.Manager, error) {
	cfg, _, err := loadConfig(c)
	if err != nil {
		return nil, nil, err
	}
	wm, err := worker.NewManager(cfg.Workers, cfg.Templates, cfg.DataDir)
	if err != nil {
		return nil, nil, &cli.Error{Code: cli.ExitError, Type: "invalid_config", Message: err.Error(), Suggestion: "run: bot-connect config validate"}
	}
	return cfg, wm, nil
}

func workerCmd() *cli.Command {
	return &cli.Command{Name: "worker", Summary: "workers: their sessions and recent tasks",
		Desc: "Shows the state the running bot last saved. Live queue status: /status in chat.",
		Children: []*cli.Command{
			{
				Name: "list", Summary: "all workers, including members' private instances",
				Examples: []string{"bot-connect worker list", "bot-connect worker list --format json"},
				Flags:    []cli.Flag{configFlag},
				Run: func(c *cli.Ctx) error {
					_, wm, err := openWorkers(c)
					if err != nil {
						return err
					}
					ws := wm.Snapshot()
					return c.Out(ws, func(w io.Writer) {
						tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
						fmt.Fprintln(tw, "WORKER\tAGENT\tACCESS\tTEMPLATE\tSESSION\tDIR")
						for _, x := range ws {
							fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", x.Name, x.Agent, x.Access, orDash(x.Template), short(x.Session), x.Dir)
						}
						tw.Flush()
					})
				},
			},
			{
				Name: "get", Summary: "one worker with its recent tasks",
				Examples: []string{"bot-connect worker get --name myapp"},
				Flags:    []cli.Flag{configFlag, {Name: "name", Type: cli.String, Required: true, Desc: "worker name"}},
				Run: func(c *cli.Ctx) error {
					_, wm, err := openWorkers(c)
					if err != nil {
						return err
					}
					for _, x := range wm.Snapshot() {
						if x.Name != c.Str("name") {
							continue
						}
						var ts []worker.Task
						for _, t := range wm.Tasks() {
							if t.Worker == x.Name && len(ts) < 10 {
								ts = append(ts, t)
							}
						}
						out := map[string]any{"worker": x, "recent_tasks": ts}
						return c.Out(out, func(w io.Writer) {
							fmt.Fprintf(w, "%s (%s, %s)\n  dir: %s\n  session: %s\n", x.Name, x.Agent, x.Access, x.Dir, orDash(x.Session))
							printTasks(w, ts)
						})
					}
					return cli.NotFound(fmt.Sprintf("no worker named %q", c.Str("name")), "run: bot-connect worker list")
				},
			},
		}}
}

func templateCmd() *cli.Command {
	return &cli.Command{Name: "template", Summary: "worker templates the brain creates workers from",
		Desc: "Templates are defined in config ([[templates]]); the brain creates and retires workers from them in chat (worker_create / worker_retire).",
		Children: []*cli.Command{{
			Name: "list", Summary: "templates with how many workers use each",
			Examples: []string{"bot-connect template list", "bot-connect template list --format json"},
			Flags:    []cli.Flag{configFlag},
			Run: func(c *cli.Ctx) error {
				_, wm, err := openWorkers(c)
				if err != nil {
					return err
				}
				ts := wm.Templates(worker.Scope{})
				return c.Out(ts, func(w io.Writer) {
					tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
					fmt.Fprintln(tw, "TEMPLATE\tAGENT\tACCESS\tWORKSPACE\tIN USE\tDESCRIPTION")
					for _, t := range ts {
						fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d/%d\t%s\n", t.Name, t.Agent, t.Access, t.Workspace, t.InUse, t.MaxInstances, t.Description)
					}
					tw.Flush()
				})
			},
		}}}
}

func taskCmd() *cli.Command {
	return &cli.Command{Name: "task", Summary: "tasks handed to workers", Children: []*cli.Command{
		{
			Name: "list", Summary: "recent tasks, newest first",
			Examples: []string{"bot-connect task list", "bot-connect task list --worker myapp --status failed --format json"},
			Flags: []cli.Flag{configFlag,
				{Name: "worker", Type: cli.String, Desc: "only this worker"},
				{Name: "status", Type: cli.Enum, Enum: []string{"queued", "running", "succeeded", "failed", "cancelled"}, Desc: "only this status"},
				{Name: "limit", Type: cli.Int, Default: "20", Desc: "max tasks"},
			},
			Run: func(c *cli.Ctx) error {
				_, wm, err := openWorkers(c)
				if err != nil {
					return err
				}
				var ts []worker.Task
				for _, t := range wm.Tasks() {
					if (c.Str("worker") == "" || t.Worker == c.Str("worker")) && (c.Str("status") == "" || t.Status == c.Str("status")) && len(ts) < c.Int("limit") {
						ts = append(ts, t)
					}
				}
				return c.Out(ts, func(w io.Writer) { printTasks(w, ts) })
			},
		},
		{
			Name: "get", Summary: "one task: instruction, requester, result",
			Examples: []string{"bot-connect task get --id t3"},
			Flags:    []cli.Flag{configFlag, {Name: "id", Type: cli.String, Required: true, Desc: "task id, e.g. t3"}},
			Run: func(c *cli.Ctx) error {
				_, wm, err := openWorkers(c)
				if err != nil {
					return err
				}
				t, ok := wm.Task(c.Str("id"))
				if !ok {
					return cli.NotFound(fmt.Sprintf("no task %q", c.Str("id")), "run: bot-connect task list")
				}
				return c.Out(t, func(w io.Writer) {
					fmt.Fprintf(w, "%s @%s [%s] by %s (%s)\ninstruction:\n%s\n", t.ID, t.Worker, t.Status, t.Requester.Display(), t.Requester.Role, t.Instruction)
					if t.Error != "" {
						fmt.Fprintf(w, "error: %s\n", t.Error)
					}
					if t.Result != "" {
						fmt.Fprintf(w, "result:\n%s\n", t.Result)
					}
				})
			},
		},
	}}
}

func printTasks(w io.Writer, ts []worker.Task) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TASK\tWORKER\tSTATUS\tWHEN\tBY\tINSTRUCTION")
	for _, t := range ts {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Worker, t.Status, t.CreatedAt.Format("01-02 15:04"), t.Requester.Display(), clip(t.Instruction, 50))
	}
	tw.Flush()
}

// ---------- schedule ----------

func openSchedules(c *cli.Ctx) (*schedule.Store, error) {
	cfg, _, err := loadConfig(c)
	if err != nil {
		return nil, err
	}
	st, err := schedule.Open(cfg.DataDir)
	if err != nil {
		return nil, cli.Failed(err.Error(), "the schedules file may be corrupt: "+filepath.Join(cfg.DataDir, "schedules.json"), false)
	}
	return st, nil
}

func printJobs(w io.Writer, jobs []schedule.Job) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID	STATE	SPEC	NEXT	RUNS	LAST	BOT	WHAT")
	for _, j := range jobs {
		state, next := "enabled", "-"
		switch {
		case j.Once && !j.Enabled && j.Runs > 0:
			state = "done"
		case !j.Enabled:
			state = "paused"
		}
		if !j.NextRun.IsZero() {
			next = j.NextRun.Format("01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n", j.ID, state, j.Spec, next, j.Runs, orDash(j.LastStatus), j.Bot, clip(firstNonEmpty(j.Description, j.Prompt), 40))
	}
	tw.Flush()
}

func scheduleCmd() *cli.Command {
	idFlag := cli.Flag{Name: "id", Type: cli.String, Required: true, Desc: "schedule id (from schedule list)"}
	change := func(name, summary string, apply func(*schedule.Store, string) (schedule.Job, error)) *cli.Command {
		return &cli.Command{Name: name, Summary: summary, Effects: true,
			Examples: []string{"bot-connect schedule " + name + " --id s1a2b3c"},
			Flags:    []cli.Flag{configFlag, idFlag},
			Run: func(c *cli.Ctx) error {
				st, err := openSchedules(c)
				if err != nil {
					return err
				}
				j, ok := st.Get(c.Str("id"))
				if !ok {
					return cli.NotFound(fmt.Sprintf("no schedule %q", c.Str("id")), "run: bot-connect schedule list")
				}
				if c.Bool("dry-run") {
					return c.DryRun(map[string]any{"action": name, "schedule": j})
				}
				j, err = apply(st, j.ID)
				if err != nil {
					return err
				}
				return c.Out(map[string]any{"action": name, "schedule": j}, func(w io.Writer) { fmt.Fprintf(w, "%s: %s\n", name, j.ID) })
			}}
	}
	return &cli.Command{Name: "schedule", Summary: "scheduled jobs (created in chat by the owner/admins)",
		Desc: "Jobs are created in chat (\"每个工作日 9 点检查 CI\"). A running bot picks up changes made here.",
		Children: []*cli.Command{
			{
				Name: "list", Summary: "all scheduled jobs",
				Examples: []string{"bot-connect schedule list", "bot-connect schedule list --bot team-bot --format json"},
				Flags:    []cli.Flag{configFlag, {Name: "bot", Type: cli.String, Desc: "only this bot"}},
				Run: func(c *cli.Ctx) error {
					st, err := openSchedules(c)
					if err != nil {
						return err
					}
					jobs := st.List(c.Str("bot"), "")
					if jobs == nil {
						jobs = []schedule.Job{}
					}
					return c.Out(jobs, func(w io.Writer) { printJobs(w, jobs) })
				},
			},
			{
				Name: "get", Summary: "one job: spec, instruction, creator, runs",
				Examples: []string{"bot-connect schedule get --id s1a2b3c"},
				Flags:    []cli.Flag{configFlag, idFlag},
				Run: func(c *cli.Ctx) error {
					st, err := openSchedules(c)
					if err != nil {
						return err
					}
					j, ok := st.Get(c.Str("id"))
					if !ok {
						return cli.NotFound(fmt.Sprintf("no schedule %q", c.Str("id")), "run: bot-connect schedule list")
					}
					return c.Out(j, func(w io.Writer) {
						printJobs(w, []schedule.Job{j})
						fmt.Fprintf(w, "\ncreated by %s (%s) at %s\ninstruction:\n%s\n", j.CreatedBy.Display(), j.CreatedBy.Role, j.CreatedAt.Format("2006-01-02 15:04"), j.Prompt)
					})
				},
			},
			change("pause", "stop a job from running (keeps it)", func(st *schedule.Store, id string) (schedule.Job, error) { return st.SetEnabled(id, false) }),
			change("resume", "let a paused job run again", func(st *schedule.Store, id string) (schedule.Job, error) { return st.SetEnabled(id, true) }),
			change("delete", "remove a job", func(st *schedule.Store, id string) (schedule.Job, error) { return st.Delete(id) }),
		}}
}

// ---------- session ----------

func sessionCmd() *cli.Command {
	return &cli.Command{Name: "session", Summary: "agent sessions on this machine (Claude Code, Codex)", Children: []*cli.Command{
		{
			Name: "list", Summary: "sessions the workers cover (--all: every session, to pick ids for `sessions = [...]`)",
			Examples: []string{"bot-connect session list", "bot-connect session list --all --agent codex --limit 10"},
			Flags: []cli.Flag{configFlag,
				{Name: "worker", Type: cli.String, Desc: "only sessions of this worker"},
				{Name: "all", Type: cli.Bool, Desc: "every session on this machine, not just covered ones"},
				{Name: "agent", Type: cli.Enum, Enum: []string{"claudecode", "codex"}, Desc: "only this agent"},
				{Name: "limit", Type: cli.Int, Default: "20", Desc: "max sessions"},
			},
			Run: func(c *cli.Ctx) error {
				cat := session.Default()
				var list []session.Info
				if c.Bool("all") {
					list, _ = cat.List(c.Str("agent"), c.Int("limit"), nil)
				} else {
					_, wm, err := openWorkers(c)
					if err != nil {
						return err
					}
					cat = session.NewCatalog(session.NewClaudeStore(""), session.NewCodexStore(""), session.NewCodexStore(wm.CodexHome()))
					owner := worker.Scope{Caller: identity.User{ID: "local", Role: identity.RoleOwner}}
					seen := map[string]bool{}
					for _, root := range wm.Roots(owner) {
						if c.Str("worker") != "" && root != c.Str("worker") {
							continue
						}
						p, _ := wm.Policy(root, owner)
						if c.Str("agent") != "" && p.Agent != c.Str("agent") {
							continue
						}
						for id := range p.IDs {
							if in, err := cat.Find(id); err == nil && !seen[id] {
								seen[id] = true
								list = append(list, in)
							}
						}
						if p.DirAll {
							l, _ := cat.List(p.Agent, c.Int("limit"), func(d string) bool { return filepath.Clean(d) == filepath.Clean(p.Dir) })
							for _, in := range l {
								if !seen[in.ID] {
									seen[in.ID] = true
									list = append(list, in)
								}
							}
						}
					}
					sort.Slice(list, func(i, j int) bool { return list[i].Updated.After(list[j].Updated) })
					if len(list) > c.Int("limit") {
						list = list[:c.Int("limit")]
					}
				}
				return c.Out(list, func(w io.Writer) {
					tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
					fmt.Fprintln(tw, "SESSION\tAGENT\tUPDATED\tDIR\tTITLE")
					for _, s := range list {
						fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.ID, s.Agent, s.Updated.Format("01-02 15:04"), s.Dir, clip(s.Title, 40))
					}
					tw.Flush()
				})
			},
		},
		{
			Name: "get", Summary: "a session's recent conversation",
			Examples: []string{"bot-connect session get --id 6f1c… --limit 10"},
			Flags: []cli.Flag{
				{Name: "id", Type: cli.String, Required: true, Desc: "session id (from session list)"},
				{Name: "limit", Type: cli.Int, Default: "20", Desc: "max messages"},
			},
			Run: func(c *cli.Ctx) error {
				cat := session.Default()
				in, err := cat.Find(c.Str("id"))
				if err != nil {
					return cli.NotFound(fmt.Sprintf("no session %q", c.Str("id")), "run: bot-connect session list --all")
				}
				h, _ := cat.History(in.Agent, in.ID, c.Int("limit"))
				out := map[string]any{"session": in, "messages": h}
				return c.Out(out, func(w io.Writer) {
					fmt.Fprintf(w, "%s [%s] %s — %s\n\n", in.ID, in.Agent, in.Dir, in.Title)
					fmt.Fprint(w, worker.RenderHistory(h, 600))
				})
			},
		},
	}}
}

// ---------- audit ----------

func auditCmd() *cli.Command {
	return &cli.Command{Name: "audit", Summary: "what the bots saw and did (messages, turns, tool calls, tasks)", Children: []*cli.Command{
		{
			Name: "list", Summary: "audit events, oldest first",
			Examples: []string{
				"bot-connect audit list --since 1h",
				"bot-connect audit list --type inbound --type tool_call --format ndjson",
				"bot-connect audit list --user ou_xxx --since 24h",
			},
			Flags: []cli.Flag{configFlag,
				{Name: "since", Type: cli.Duration, Default: "24h", Desc: "how far back"},
				{Name: "type", Type: cli.List, Desc: "inbound|turn_start|turn_end|tool_call|task|outbound|schedule"},
				{Name: "bot", Type: cli.String, Desc: "only this bot"},
				{Name: "user", Type: cli.String, Desc: "only events of this user id"},
				{Name: "limit", Type: cli.Int, Default: "200", Desc: "max events (the newest are kept)"},
			},
			Run: func(c *cli.Ctx) error {
				cfg, _, err := loadConfig(c)
				if err != nil {
					return err
				}
				types := map[string]bool{}
				for _, t := range c.List("type") {
					switch t {
					case audit.Inbound, audit.TurnStart, audit.TurnEnd, audit.ToolCall, audit.Task, audit.Outbound, audit.Schedule:
						types[t] = true
					default:
						return cli.Usage(fmt.Sprintf("unknown --type %q", t), "one of inbound|turn_start|turn_end|tool_call|task|outbound|schedule")
					}
				}
				since := time.Now().Add(-c.Dur("since"))
				var events []audit.Event
				for d := since; !d.After(time.Now().Add(24 * time.Hour)); d = d.Add(24 * time.Hour) {
					f, err := os.Open(filepath.Join(cfg.DataDir, "audit", d.Format("2006-01-02")+".jsonl"))
					if err != nil {
						continue
					}
					sc := bufio.NewScanner(f)
					sc.Buffer(make([]byte, 1<<20), 8<<20)
					for sc.Scan() {
						var e audit.Event
						if json.Unmarshal(sc.Bytes(), &e) != nil || e.Time.Before(since) {
							continue
						}
						if (len(types) > 0 && !types[e.Type]) || (c.Str("bot") != "" && e.Bot != c.Str("bot")) ||
							(c.Str("user") != "" && (e.User == nil || e.User.ID != c.Str("user"))) {
							continue
						}
						events = append(events, e)
					}
					f.Close()
				}
				if n := c.Int("limit"); len(events) > n {
					events = events[len(events)-n:]
				}
				rows := make([]any, len(events))
				for i := range events {
					rows[i] = events[i]
				}
				return c.Out(rows, func(w io.Writer) {
					tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
					fmt.Fprintln(tw, "TIME\tTYPE\tWHO\tROLE\tDETAIL")
					for _, e := range events {
						who, role := "", ""
						if e.User != nil {
							who, role = e.User.Display(), string(e.User.Role)
						}
						detail := e.Tool
						if detail == "" {
							detail = clip(strings.Join(strings.Fields(e.Text), " "), 60)
						}
						if e.Error != "" {
							detail += " ERROR: " + clip(e.Error, 40)
						}
						fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Time.Local().Format("01-02 15:04:05"), e.Type, who, role, detail)
					}
					tw.Flush()
				})
			},
		},
	}}
}

// ---------- feishu ----------

func feishuCmd() *cli.Command {
	return &cli.Command{Name: "feishu", Summary: "connect a bot to Feishu / Lark", Children: []*cli.Command{
		{
			Name: "setup", Summary: "create a Feishu bot by scanning a QR code and write it into the config", Effects: true,
			Desc:     "The QR code and progress go to stderr. On success app_id/app_secret are written into [feishu]\nand the person who scanned becomes an owner.",
			Examples: []string{"bot-connect feishu setup", "bot-connect feishu setup --config ./config.toml --timeout 5m"},
			Flags: []cli.Flag{
				{Name: "config", Type: cli.String, Env: "BOT_CONNECT_CONFIG", Default: "~/.bot-connect/config.toml", Desc: "config file to write (created from the starter config if missing)"},
				{Name: "timeout", Type: cli.Duration, Default: "10m", Desc: "how long to wait for the scan"},
			},
			Run: func(c *cli.Ctx) error {
				p := config.ExpandHome(c.Str("config"))
				if c.Bool("dry-run") {
					return c.DryRun(map[string]any{"create": "a Feishu bot (QR scan)", "write": p,
						"keys": []string{"[feishu] app_id", "[feishu] app_secret", "[bot] owners += scanner"}})
				}
				res, err := registerBot(c.Dur("timeout"))
				if err != nil {
					return cli.Failed("feishu setup: "+err.Error(), "run it again; the QR code expires after a few minutes", true)
				}
				if err := writeFeishuConfig(p, res); err != nil {
					return err
				}
				out := map[string]any{"app_id": res.appID, "brand": res.brand, "owner": res.ownerOpenID, "config": p,
					"next": "bot-connect bot run --config " + p}
				return c.Out(out, func(w io.Writer) {
					fmt.Fprintf(w, "bot created: app_id=%s (%s); owner %s\nwritten to %s\nnext: bot-connect bot run --config %s\n", res.appID, res.brand, res.ownerOpenID, p, p)
				})
			},
		},
	}}
}

// ---------- tool (for brains running as shell agents) ----------

func toolAPI() (string, error) {
	api := os.Getenv("BOT_CONNECT_API")
	if api == "" {
		return "", &cli.Error{Code: cli.ExitPermission, Type: "unavailable",
			Message:    "BOT_CONNECT_API is not set: tool commands only work inside a bot-connect brain turn",
			Suggestion: "brains started by bot-connect get it automatically"}
	}
	return api, nil
}

func toolCmd() *cli.Command {
	hc := &http.Client{Timeout: 60 * time.Second}
	return &cli.Command{Name: "tool", Summary: "bot-connect's tools, for brains that run as shell agents",
		Desc: "Inside a brain turn (BOT_CONNECT_API is set) these call the same tools MCP brains get,\nwith the same permission checks for the person in the turn.",
		Children: []*cli.Command{
			{
				Name: "list", Summary: "tools available in this turn, with their JSON argument schemas",
				Examples: []string{"bot-connect tool list --format json"},
				Run: func(c *cli.Ctx) error {
					api, err := toolAPI()
					if err != nil {
						return err
					}
					resp, err := hc.Get(api + "/tools")
					if err != nil {
						return cli.Failed(err.Error(), "the bot may have stopped; retry the turn", true)
					}
					defer resp.Body.Close()
					var list []map[string]any
					if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&list) != nil {
						return cli.Denied(fmt.Sprintf("tool server answered %s", resp.Status), "this turn's token may have expired")
					}
					return c.Out(list, func(w io.Writer) {
						for _, t := range list {
							fmt.Fprintf(w, "%-14s %s\n", t["name"], t["description"])
						}
					})
				},
			},
			{
				Name: "call", Summary: "call a tool with JSON arguments", Effects: true,
				Examples: []string{
					`bot-connect tool call --name list_workers`,
					`bot-connect tool call --name delegate --args '{"worker":"myapp","instruction":"…"}'`,
					`echo '{"task_id":"t3"}' | bot-connect tool call --name task_status --args -`,
				},
				Flags: []cli.Flag{
					{Name: "name", Type: cli.String, Required: true, Desc: "tool name (from tool list)"},
					{Name: "args", Type: cli.String, Default: "{}", Desc: "JSON object of arguments, or - to read it from stdin"},
				},
				Run: func(c *cli.Ctx) error {
					api, err := toolAPI()
					if err != nil {
						return err
					}
					body := c.Str("args")
					if body == "-" {
						b, _ := io.ReadAll(os.Stdin)
						body = string(bytes.TrimSpace(b))
					}
					var probe map[string]any
					if json.Unmarshal([]byte(body), &probe) != nil {
						return &cli.Error{Code: cli.ExitUsage, Type: "usage", Message: "--args must be a JSON object", Input: body,
							Suggestion: "see the tool's input_schema: bot-connect tool list --format json"}
					}
					url := api + "/tools/" + c.Str("name")
					if c.Bool("dry-run") {
						url += "?dry_run=1"
					}
					resp, err := hc.Post(url, "application/json", strings.NewReader(body))
					if err != nil {
						return cli.Failed(err.Error(), "the bot may have stopped; retry the turn", true)
					}
					defer resp.Body.Close()
					var r struct {
						OK        bool   `json:"ok"`
						Result    string `json:"result"`
						Error     string `json:"error"`
						ErrorType string `json:"error_type"`
						DryRun    bool   `json:"dry_run"`
					}
					if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&r) != nil {
						return cli.Denied(fmt.Sprintf("tool server answered %s", resp.Status), "this turn's token may have expired")
					}
					if !r.OK {
						e := &cli.Error{Code: cli.ExitError, Type: r.ErrorType, Message: r.Error, Input: c.Str("name") + " " + body}
						switch r.ErrorType {
						case tools.KindNotFound:
							e.Code, e.Suggestion = cli.ExitNotFound, "run: bot-connect tool call --name list_workers"
						case tools.KindPermission:
							e.Code, e.Suggestion = cli.ExitPermission, "this person may not do that; offer notify_owner instead"
						case tools.KindInvalid:
							e.Code, e.Suggestion = cli.ExitUsage, "see the input_schema in: bot-connect tool list --format json"
						}
						return e
					}
					if r.DryRun {
						return c.DryRun(map[string]any{"tool": c.Str("name"), "args": probe, "allowed": true})
					}
					return c.Out(map[string]any{"tool": c.Str("name"), "result": r.Result}, func(w io.Writer) { fmt.Fprintln(w, r.Result) })
				},
			},
		}}
}

// ---------- schema / version ----------

func schemaCmd(app *cli.App) *cli.Command {
	return &cli.Command{Name: "schema", Summary: "the command tree (or one command) as JSON: flags, types, enums, defaults, examples",
		Examples: []string{"bot-connect schema", `bot-connect schema --command "task list"`},
		Flags:    []cli.Flag{{Name: "command", Type: cli.String, Desc: `command path, e.g. "task list" (default: everything)`}},
		Run: func(c *cli.Ctx) error {
			s, err := app.Schema(strings.Fields(c.Str("command")))
			if err != nil {
				return err
			}
			c.Format = "json"
			return c.Out(s, nil)
		},
	}
}

func versionCmd() *cli.Command {
	return &cli.Command{Name: "version", Summary: "version, commit and build time",
		Examples: []string{"bot-connect version", "bot-connect version --format json"},
		Run: func(c *cli.Ctx) error {
			v, cm, t := versionInfo()
			out := map[string]string{"version": v, "commit": cm, "built": t, "go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH}
			return c.Out(out, func(w io.Writer) { fmt.Fprintf(w, "bot-connect %s (commit %s, built %s)\n", v, cm, t) })
		},
	}
}

func short(s string) string {
	if len(s) > 13 {
		return s[:8] + "…"
	}
	return orDash(s)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
