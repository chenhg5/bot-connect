package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/chenhg5/bot-connect/evals"
	"github.com/chenhg5/bot-connect/internal/assemble"
	"github.com/chenhg5/bot-connect/internal/cli"
	"github.com/chenhg5/bot-connect/internal/judge"
	"github.com/chenhg5/bot-connect/internal/sim"
)

func evalCmd() *cli.Command {
	casesFlag := cli.Flag{Name: "cases", Type: cli.String, Desc: "directory of *.toml cases (default: the built-in suite)"}
	suiteFlag := cli.Flag{Name: "suite", Type: cli.String, Default: "golden", Desc: "built-in suite: golden (decision points) | long (weeks of virtual time)"}
	load := func(c *cli.Ctx) ([]sim.Case, error) {
		if d := c.Str("cases"); d != "" {
			return sim.LoadDir(d)
		}
		if c.Str("suite") == "long" {
			return sim.LoadFS(evals.Long, "long")
		}
		return sim.LoadFS(evals.Golden, "golden")
	}
	return &cli.Command{Name: "eval", Summary: "golden cases: a real brain in a simulated world",
		Desc: "Runs the configured brain (and its provider) against scripted cases — virtual clock, scripted people, fake agents — and checks state, messages and tool calls. " +
			"Costs model calls; each case takes ~30s–3min.",
		Children: []*cli.Command{
			{
				Name: "list", Summary: "the cases",
				Examples: []string{"bot-connect eval list"},
				Flags:    []cli.Flag{casesFlag, suiteFlag},
				Run: func(c *cli.Ctx) error {
					cases, err := load(c)
					if err != nil {
						return err
					}
					type row struct {
						ID, Title string
						Critical  bool
						Steps     int
					}
					var rows []row
					for _, x := range cases {
						rows = append(rows, row{x.ID, x.Title, x.Critical, len(x.Steps)})
					}
					return c.Out(rows, func(w io.Writer) {
						tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
						fmt.Fprintln(tw, "CASE\tCRITICAL\tSTEPS\tTITLE")
						for _, r := range rows {
							fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", r.ID, map[bool]string{true: "★", false: ""}[r.Critical], r.Steps, r.Title)
						}
						tw.Flush()
					})
				},
			},
			{
				Name: "jev", Summary: "benchmark the L1 judgments (Jev) on labelled data",
				Examples: []string{"bot-connect eval jev", "bot-connect eval jev --data ./my-scope.jsonl"},
				Flags: []cli.Flag{configFlag,
					{Name: "set", Type: cli.String, Default: "all", Desc: "scope | buckets | claims | all"},
					{Name: "data", Type: cli.String, Desc: "a dataset file instead of the built-in one (with one --set)"},
				},
				Run: func(c *cli.Ctx) error {
					cfg, _, err := loadConfig(c)
					if err != nil {
						return err
					}
					j := assemble.NewJudge(cfg.Judge)
					if _, live := j.(*judge.Jev); !live {
						return fmt.Errorf("no judge configured: set [judge] api_key (or TYPESAFE_API_KEY)")
					}
					read := func(name string) ([]byte, error) {
						if p := c.Str("data"); p != "" {
							return os.ReadFile(p)
						}
						return evals.Jev.ReadFile("jev/" + name + ".jsonl")
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
					defer cancel()
					set := c.Str("set")
					var texts []string
					reports := map[string]any{}
					if set == "all" || set == "scope" {
						raw, err := read("scope")
						if err != nil {
							return err
						}
						items, err := judge.ParseScopeItems(raw)
						if err != nil {
							return err
						}
						r := judge.BenchScope(ctx, j, items)
						reports["scope"], texts = r, append(texts, r.Text())
					}
					if set == "all" || set == "buckets" {
						raw, err := read("buckets")
						if err != nil {
							return err
						}
						items, err := judge.ParseLabelItems(raw)
						if err != nil {
							return err
						}
						r := judge.BenchBuckets(ctx, j, items)
						reports["buckets"], texts = r, append(texts, r.Text())
					}
					if set == "all" || set == "claims" {
						raw, err := read("claims")
						if err != nil {
							return err
						}
						items, err := judge.ParseClaimItems(raw)
						if err != nil {
							return err
						}
						r := judge.BenchClaims(ctx, j, items)
						reports["claims"], texts = r, append(texts, r.Text())
					}
					return c.Out(reports, func(w io.Writer) { fmt.Fprintln(w, strings.Join(texts, "\n\n")) })
				},
			},
			{
				Name: "run", Summary: "run cases against the configured brain",
				Examples: []string{"bot-connect eval run", "bot-connect eval run --case G3,G6", "bot-connect eval run --critical --out ./eval-out"},
				Flags: []cli.Flag{configFlag, casesFlag, suiteFlag,
					{Name: "case", Type: cli.List, Desc: "only these case ids"},
					{Name: "critical", Type: cli.Bool, Desc: "only critical (★) cases"},
					{Name: "out", Type: cli.String, Default: "eval-out", Desc: "where transcripts go"},
					{Name: "repeat", Type: cli.Int, Default: "1", Desc: "run each case this many times and report pass rates (the brain is not deterministic)"},
				},
				Run: func(c *cli.Ctx) error {
					cfg, _, err := loadConfig(c)
					if err != nil {
						return err
					}
					cases, err := load(c)
					if err != nil {
						return err
					}
					only := map[string]bool{}
					for _, v := range c.List("case") {
						for _, id := range strings.Split(v, ",") {
							if id = strings.TrimSpace(id); id != "" {
								only[strings.ToUpper(id)] = true
							}
						}
					}
					out := c.Str("out")
					if err := os.MkdirAll(out, 0o755); err != nil {
						return err
					}
					logf, err := os.Create(filepath.Join(out, "bot-connect.log"))
					if err != nil {
						return err
					}
					defer logf.Close()
					slog.SetDefault(slog.New(slog.NewTextHandler(logf, &slog.HandlerOptions{Level: slog.LevelInfo})))
					var results []sim.Result
					for _, cs := range cases {
						if (len(only) > 0 && !only[strings.ToUpper(cs.ID)]) || (c.Bool("critical") && !cs.Critical) {
							continue
						}
						for run := 1; run <= max(c.Int("repeat"), 1); run++ {
							name := cs.ID
							if c.Int("repeat") > 1 {
								name = fmt.Sprintf("%s#%d", cs.ID, run)
							}
							c.Info("▶ %s %s", name, cs.Title)
							ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
							h, err := sim.New(ctx, cfg, cs)
							if err != nil {
								cancel()
								results = append(results, sim.Result{ID: name, Title: cs.Title, Critical: cs.Critical, Capabilities: cs.Capabilities, Failures: []string{"setup: " + err.Error()}})
								c.Info("  FAIL (setup)\n    ✗ %v", err)
								continue
							}
							r := h.Run(ctx)
							h.Close()
							cancel()
							r.ID = name
							_ = os.WriteFile(filepath.Join(out, name+".md"), []byte(r.Transcript+"\n## result\n"+resultText(r)), 0o644)
							c.Info("  %s (%s)", map[bool]string{true: "PASS", false: "FAIL"}[r.Pass], r.Duration)
							for _, f := range r.Failures {
								c.Info("    ✗ %s", f)
							}
							results = append(results, r)
						}
					}
					c.Info("by capability (runs passed · cases passing every run):")
					for _, s := range sim.ScoreByCapability(results) {
						c.Info("  %-13s %3.0f%%  %d/%d runs · %d/%d cases reliable", s.Capability, 100*float64(s.Passed)/float64(max(s.Runs, 1)), s.Passed, s.Runs, s.Reliable, s.Cases)
					}
					if c.Int("repeat") > 1 {
						rates, order := map[string][2]int{}, []string{}
						for _, r := range results {
							id, _, _ := strings.Cut(r.ID, "#")
							x, seen := rates[id]
							if !seen {
								order = append(order, id)
							}
							x[1]++
							if r.Pass {
								x[0]++
							}
							rates[id] = x
						}
						c.Info("pass rates:")
						for _, id := range order {
							c.Info("  %s %d/%d", id, rates[id][0], rates[id][1])
						}
					}
					passed, crit, critPassed := 0, 0, 0
					for _, r := range results {
						if r.Pass {
							passed++
						}
						if r.Critical {
							crit++
							if r.Pass {
								critPassed++
							}
						}
					}
					summary := map[string]any{"passed": passed, "total": len(results), "critical_passed": critPassed, "critical_total": crit, "out": out, "results": results}
					err = c.Out(summary, func(w io.Writer) {
						tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
						fmt.Fprintln(tw, "CASE\tRESULT\tTIME\tTOOLS\tTITLE")
						for _, r := range results {
							res := "PASS"
							if !r.Pass {
								res = "FAIL"
							}
							if r.Critical {
								res += " ★"
							}
							fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.ID, res, r.Duration, strings.Join(dedupe(r.Tools), ","), r.Title)
						}
						tw.Flush()
						fmt.Fprintf(w, "\n%d/%d passed; critical %d/%d. Transcripts: %s/<case>.md\n", passed, len(results), critPassed, crit, out)
					})
					if err != nil {
						return err
					}
					if critPassed < crit {
						return &cli.Error{Code: cli.ExitError, Type: "eval_failed", Message: fmt.Sprintf("%d critical case(s) failed", crit-critPassed),
							Suggestion: "read the transcripts in " + out}
					}
					return nil
				},
			},
		}}
}

func resultText(r sim.Result) string {
	if r.Pass {
		return "PASS\n"
	}
	return "FAIL\n- " + strings.Join(r.Failures, "\n- ") + "\n"
}

func dedupe(v []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range v {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
