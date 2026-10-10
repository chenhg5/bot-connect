package main

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/chenhg5/bot-connect/internal/adapters/store/jsonstore"
	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/cli"
	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/domain/inbox"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	"github.com/chenhg5/bot-connect/internal/domain/portfolio"
)

var botFlag = cli.Flag{Name: "bot", Type: cli.String, Desc: "which bot (default: the first)"}

// openState reads a bot's project state (read-only view of state.json).
func openState(c *cli.Ctx) (*app.State, config.BotConfig, error) {
	cfg, _, err := loadConfig(c)
	if err != nil {
		return nil, config.BotConfig{}, err
	}
	bc := cfg.Bots[0]
	if n := c.Str("bot"); n != "" {
		found := false
		for _, b := range cfg.Bots {
			if b.Name == n {
				bc, found = b, true
			}
		}
		if !found {
			return nil, bc, cli.NotFound(fmt.Sprintf("no bot named %q", n), "run: bot-connect bot list")
		}
	}
	st, err := jsonstore.Open(filepath.Join(bc.Dir, "state.json"))
	if err != nil {
		return nil, bc, err
	}
	var s app.State
	st.Read(func(x *app.State) { s = *x })
	return &s, bc, nil
}

func fmtWhen(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format("01-02 15:04")
}

func projectCmd() *cli.Command {
	return &cli.Command{Name: "project", Summary: "projects the bot manages (read-only)", Children: []*cli.Command{{
		Name: "list", Summary: "projects with priority, deadline, members and open items",
		Examples: []string{"bot-connect project list", "bot-connect project list --format json"},
		Flags:    []cli.Flag{configFlag, botFlag},
		Run: func(c *cli.Ctx) error {
			s, _, err := openState(c)
			if err != nil {
				return err
			}
			var ps []portfolio.Project
			for _, p := range s.Projects {
				ps = append(ps, p)
			}
			sort.Slice(ps, func(i, j int) bool { return ps[i].ID < ps[j].ID })
			return c.Out(ps, func(w io.Writer) {
				tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "PROJECT\tPRIORITY\tSTATUS\tUNTIL\tOPEN ITEMS\tMEMBERS\tTITLE")
				for _, p := range ps {
					open := 0
					for _, it := range s.Items {
						if it.Project == p.ID && it.Open() {
							open++
						}
					}
					var ms []string
					for _, m := range p.Members {
						ms = append(ms, string(m.Worker)+"="+m.Role)
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%v\t%s\n", p.ID, p.Priority, p.Status, fmtWhen(p.Timebox.Until), open, ms, p.Title)
				}
				tw.Flush()
			})
		},
	}}}
}

func itemCmd() *cli.Command {
	return &cli.Command{Name: "item", Summary: "work items and who has them (read-only)", Children: []*cli.Command{{
		Name: "list", Summary: "items with status, due date and current assignment",
		Examples: []string{"bot-connect item list", "bot-connect item list --project P1 --all"},
		Flags:    []cli.Flag{configFlag, botFlag, {Name: "project", Type: cli.String, Desc: "only this project"}, {Name: "all", Type: cli.Bool, Desc: "include done / dropped"}},
		Run: func(c *cli.Ctx) error {
			s, _, err := openState(c)
			if err != nil {
				return err
			}
			var items []planning.Item
			for _, it := range s.Items {
				if (c.Str("project") == "" || string(it.Project) == c.Str("project")) && (c.Bool("all") || it.Open()) {
					items = append(items, it)
				}
			}
			sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
			return c.Out(items, func(w io.Writer) {
				tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "ITEM\tPROJECT\tSTATUS\tDUE\tASSIGNMENT\tTITLE")
				for _, it := range items {
					as := "-"
					if a, ok := s.Assignments[it.Assignment]; ok {
						as = fmt.Sprintf("%s→%s %s", a.ID, a.Worker, a.Status)
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", it.ID, it.Project, it.Status, fmtWhen(it.Due), as, it.Title)
				}
				tw.Flush()
			})
		},
	}}}
}

func inboxCmd() *cli.Command {
	return &cli.Command{Name: "inbox", Summary: "attention signals: what reached the bot and what it did about them (read-only)", Children: []*cli.Command{{
		Name: "list", Summary: "signals, newest first",
		Examples: []string{"bot-connect inbox list", "bot-connect inbox list --open"},
		Flags:    []cli.Flag{configFlag, botFlag, {Name: "open", Type: cli.Bool, Desc: "only signals not handled yet"}, {Name: "limit", Type: cli.Int, Default: "30", Desc: "max signals"}},
		Run: func(c *cli.Ctx) error {
			s, _, err := openState(c)
			if err != nil {
				return err
			}
			var sigs []inbox.Signal
			for _, x := range s.Signals {
				if !c.Bool("open") || x.Status.Open() {
					sigs = append(sigs, x)
				}
			}
			sort.Slice(sigs, func(i, j int) bool { return sigs[i].CreatedAt.After(sigs[j].CreatedAt) })
			if len(sigs) > c.Int("limit") {
				sigs = sigs[:c.Int("limit")]
			}
			return c.Out(sigs, func(w io.Writer) {
				tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "SIGNAL\tAT\tSOURCE\tREASON\tWAKE\tSTATUS\tSUMMARY")
				for _, x := range sigs {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", x.ID, x.CreatedAt.Format("01-02 15:04"), x.Source, x.Reason, x.Wake, x.Status, oneLine(x.Summary, 60))
				}
				tw.Flush()
			})
		},
	}}}
}
