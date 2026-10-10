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
	"github.com/chenhg5/bot-connect/internal/cli"
	"github.com/chenhg5/bot-connect/internal/sim"
)

func evalCmd() *cli.Command {
	casesFlag := cli.Flag{Name: "cases", Type: cli.String, Desc: "directory of *.toml cases (default: the built-in golden set)"}
	load := func(c *cli.Ctx) ([]sim.Case, error) {
		if d := c.Str("cases"); d != "" {
			return sim.LoadDir(d)
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
				Flags:    []cli.Flag{casesFlag},
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
				Name: "run", Summary: "run cases against the configured brain",
				Examples: []string{"bot-connect eval run", "bot-connect eval run --case G3,G6", "bot-connect eval run --critical --out ./eval-out"},
				Flags: []cli.Flag{configFlag, casesFlag,
					{Name: "case", Type: cli.List, Desc: "only these case ids"},
					{Name: "critical", Type: cli.Bool, Desc: "only critical (★) cases"},
					{Name: "out", Type: cli.String, Default: "eval-out", Desc: "where transcripts go"},
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
						c.Info("▶ %s %s", cs.ID, cs.Title)
						ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
						h, err := sim.New(ctx, cfg, cs)
						if err != nil {
							cancel()
							results = append(results, sim.Result{ID: cs.ID, Title: cs.Title, Critical: cs.Critical, Failures: []string{err.Error()}})
							continue
						}
						r := h.Run(ctx)
						h.Close()
						cancel()
						_ = os.WriteFile(filepath.Join(out, cs.ID+".md"), []byte(r.Transcript+"\n## result\n"+resultText(r)), 0o644)
						c.Info("  %s (%s)", map[bool]string{true: "PASS", false: "FAIL"}[r.Pass], r.Duration)
						for _, f := range r.Failures {
							c.Info("    ✗ %s", f)
						}
						results = append(results, r)
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
