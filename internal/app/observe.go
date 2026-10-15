package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/inbox"
	"github.com/chenhg5/bot-connect/internal/domain/observe"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// Following work the bot doesn't run. The bot looks where the truth of a
// project lives, keeps a dossier of how to follow it, and watches sources;
// code compares what it sees and wakes the brain only for a change, a run
// of failures, or a silence longer than expected. No particular system is
// known here: a project tool, a tracker, a forge are just probes.

// Prober looks at something (a file, a directory, an allowed command, a page).
type Prober interface {
	Probe(ctx context.Context, p observe.Probe) (string, error)
}

// Look runs a probe now.
func (a *App) Look(ctx context.Context, by Actor, p observe.Probe) (string, error) {
	if err := requirePrivileged(by, "look at project sources"); err != nil {
		return "", err
	}
	if a.Prober == nil {
		return "", Forbidden("looking is not set up ([observe] roots)")
	}
	p, err := a.resolveProbe(ctx, p)
	if err != nil {
		return "", err
	}
	return a.Prober.Probe(ctx, p)
}

// Dossier returns a project's dossier.
func (a *App) Dossier(pid ProjectID) (observe.Dossier, bool) {
	var d observe.Dossier
	var ok bool
	a.Store.Read(func(s *State) { d, ok = s.Dossiers[pid] })
	return d, ok
}

// WriteDossier replaces a project's dossier (text, and sources if given).
func (a *App) WriteDossier(ctx context.Context, by Actor, pid ProjectID, text string, sources []observe.Source) (observe.Dossier, error) {
	if err := requirePrivileged(by, "write a dossier"); err != nil {
		return observe.Dossier{}, err
	}
	var out observe.Dossier
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		if _, ok := s.Projects[pid]; !ok {
			return nil, NotFound("no project %s", pid)
		}
		d := s.Dossiers[pid]
		d.Project = pid
		if err := d.Write(text, sources, a.now()); err != nil {
			return nil, err
		}
		s.Dossiers[pid] = d
		out = d
		return []Event{NewEvent("dossier.written", "project:"+string(pid), a.now(), by, "chars", len([]rune(d.Text)))}, nil
	})
	return out, err
}

// AddWatch starts watching a source for a project. The first look happens
// now, so a probe that doesn't work is found out at once.
func (a *App) AddWatch(ctx context.Context, by Actor, pid ProjectID, name string, p observe.Probe, every, stale time.Duration, expect string) (observe.Watch, string, error) {
	if err := requirePrivileged(by, "watch project sources"); err != nil {
		return observe.Watch{}, "", err
	}
	if a.Prober == nil {
		return observe.Watch{}, "", Forbidden("looking is not set up ([observe] roots)")
	}
	rp, perr := a.resolveProbe(ctx, p)
	text := ""
	if perr == nil {
		text, perr = a.Prober.Probe(ctx, rp)
	}
	if perr != nil {
		return observe.Watch{}, text, fmt.Errorf("the probe doesn't work, so nothing would be watched: %w", perr)
	}
	var out observe.Watch
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		if _, ok := s.Projects[pid]; !ok {
			return nil, NotFound("no project %s", pid)
		}
		for _, w := range s.Watches {
			if w.Project == pid && w.Probe.String() == p.String() && !w.Paused {
				return nil, Conflict("%s already watches this (%s)", w.ID, w.Name)
			}
		}
		w, err := observe.NewWatch(s.NextID("W"), pid, name, p, every, stale, expect, a.now(), by)
		if err != nil {
			return nil, err
		}
		w.Record(observe.Observe(text, nil, a.now()))
		s.Watches[w.ID] = w
		out = w
		return []Event{NewEvent("watch.added", "watch:"+w.ID, a.now(), by, "project", string(pid), "probe", p.String())}, nil
	})
	return out, text, err
}

// RemoveWatch stops a watch.
func (a *App) RemoveWatch(ctx context.Context, by Actor, id string) error {
	if err := requirePrivileged(by, "stop a watch"); err != nil {
		return err
	}
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		if _, ok := s.Watches[id]; !ok {
			return nil, NotFound("no watch %s", id)
		}
		delete(s.Watches, id)
		return []Event{NewEvent("watch.removed", "watch:"+id, a.now(), by)}, nil
	})
	return err
}

// Watches lists watches (of a project, or all), by id.
func (a *App) Watches(pid ProjectID) []observe.Watch {
	var out []observe.Watch
	a.Store.Read(func(s *State) {
		for _, w := range s.Watches {
			if pid == "" || w.Project == pid {
				out = append(out, w)
			}
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// AllowCommand lets probes run another read-only command pattern ("hp * get",
// "linear issue list"). Only the principal decides that.
func (a *App) AllowCommand(ctx context.Context, by Actor, pattern string) error {
	if by.Role != RolePrincipal {
		return Forbidden("only the principal allows new commands")
	}
	pattern = strings.Join(strings.Fields(pattern), " ")
	if pattern == "" || !strings.Contains(pattern, " ") {
		return Invalid("give a program and its read-only subcommand, e.g. \"hp * get\" (a bare program would allow everything it can do)")
	}
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		for _, p := range s.Allowed {
			if p == pattern {
				return nil, nil
			}
		}
		s.Allowed = append(s.Allowed, pattern)
		return []Event{NewEvent("observe.allowed", "observe", a.now(), by, "pattern", pattern)}, nil
	})
	return err
}

// AllowedCommands are the patterns the principal allowed at run time.
func (a *App) AllowedCommands() []string {
	var out []string
	a.Store.Read(func(s *State) { out = append(out, s.Allowed...) })
	return out
}

// MaxWatchesPerTick bounds how much looking one tick does.
const MaxWatchesPerTick = 8

// runWatches looks at due watches and raises what's news.
func (a *App) runWatches(ctx context.Context) {
	if a.Prober == nil {
		return
	}
	now := a.now()
	var due []observe.Watch
	a.Store.Read(func(s *State) {
		for _, w := range s.Watches {
			if w.Due(now) {
				due = append(due, w)
			}
		}
	})
	sort.Slice(due, func(i, j int) bool { return due[i].Last.At.Before(due[j].Last.At) })
	if len(due) > MaxWatchesPerTick {
		due = due[:MaxWatchesPerTick]
	}
	for _, w := range due {
		rp, err := a.resolveProbe(ctx, w.Probe)
		text := ""
		if err == nil {
			text, err = a.Prober.Probe(ctx, rp)
		}
		o := observe.Observe(text, err, now)
		var finding observe.Finding
		var prev, cur observe.Watch
		_, _ = a.commit(ctx, func(s *State) ([]Event, error) {
			x, ok := s.Watches[w.ID]
			if !ok {
				return nil, nil
			}
			prev = x
			finding, _ = x.Record(o)
			s.Watches[w.ID] = x
			cur = x
			return nil, nil
		})
		a.raiseWatch(ctx, prev, cur, finding)
	}
}

func (a *App) raiseWatch(ctx context.Context, prev, w observe.Watch, f observe.Finding) {
	var level int
	var summary, body string
	head := fmt.Sprintf("「%s」(%s，%s)", w.Name, w.ID, w.Project)
	switch f {
	case observe.Changed:
		level = 0
		summary = head + " 有变化"
		body = fmt.Sprintf("%s 有变化。\n看的是：%s\n变化：\n%s", head, w.Probe, observe.Diff(prev.Last.Text, w.Last.Text, 30))
	case observe.Failing:
		level = 1
		summary = head + " 连续看不了"
		body = fmt.Sprintf("%s 连续 %d 次看不了：%s\n看的是：%s。修好看法（watch 删掉重加），或者换个来源。", head, w.Failures, w.Last.Err, w.Probe)
	case observe.Stale:
		level = 1
		summary = fmt.Sprintf("%s 已经 %s 没有变化", head, w.Last.At.Sub(w.ChangedAt).Round(time.Hour))
		body = fmt.Sprintf("%s 从 %s 起没有变化（%s）。", head, w.ChangedAt.Format("01-02 15:04"), w.Probe)
	default:
		return
	}
	if w.Expect != "" {
		body += "\n这里的进展应该是：" + w.Expect
	}
	body += "\n对照这个项目的小抄（dossier）判断：是正常、要跟进，还是要告诉委托人。"
	_, _, _ = a.Ingest(ctx, SignalSpec{Dedupe: fmt.Sprintf("watch:%s:%s:%s", w.ID, f, w.Last.At.Format(time.RFC3339)), Source: inbox.Observation,
		Reason: "watch_" + string(f), Actor: System("watch"), Summary: summary, Body: body, Level: level,
		Refs: inbox.Refs{Project: w.Project}})
}

// DescribeObserved is a project's dossier and watches, for the brain.
func (a *App) DescribeObserved(pid ProjectID) string {
	var b strings.Builder
	if d, ok := a.Dossier(pid); ok && d.Text != "" {
		fmt.Fprintf(&b, "小抄（%s 更新）：\n%s\n", d.UpdatedAt.Format("01-02 15:04"), d.Text)
		for _, s := range d.Sources {
			fmt.Fprintf(&b, "- 来源「%s」：%s", s.Name, s.Probe)
			if s.What != "" {
				b.WriteString("（" + s.What + "）")
			}
			b.WriteString("\n")
		}
	}
	for _, d := range a.RecentActs(pid, 5) {
		line := fmt.Sprintf("- 做过 %s：%s（%s）", d.At.Format("01-02 15:04"), d.Action.Describe(), d.Grant)
		if d.Err != "" {
			line += " ✗ " + clipRunes(d.Err, 80)
		}
		b.WriteString(line + "\n")
	}
	for _, w := range a.Watches(pid) {
		fmt.Fprintf(&b, "- 盯 %s「%s」每 %s：%s；上次 %s", w.ID, w.Name, humanDur(w.Every), w.Probe, w.Last.At.Format("01-02 15:04"))
		if !w.ChangedAt.IsZero() {
			b.WriteString("，上次变化 " + w.ChangedAt.Format("01-02 15:04"))
		}
		if w.Failures > 0 {
			fmt.Fprintf(&b, "，连续失败 %d 次", w.Failures)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
