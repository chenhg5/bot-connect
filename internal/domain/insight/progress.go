// Package insight computes what can be computed — pace, forecasts,
// progress, health, staffing candidates — so the brain reads numbers instead
// of estimating them. Everything here is a pure function of a world snapshot.
package insight

import (
	"fmt"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/world"
)

// Pace is how long a worker takes relative to estimates (1 = on estimate).
type Pace interface{ Factor(w WorkerID) float64 }

// PaceStats is learned from delivered work: actual duration / estimate.
type PaceStats struct {
	ratios map[WorkerID][]float64
}

// MinSamples before a worker's pace is trusted.
const MinSamples = 2

func PaceFrom(w *world.World) PaceStats {
	p := PaceStats{ratios: map[WorkerID][]float64{}}
	for _, a := range w.Assignments {
		if a.Kind != delegation.Work || a.Brief.Estimate <= 0 || (a.Status != delegation.Verified && a.Status != delegation.Delivered) {
			continue
		}
		start, end := a.Started(), a.DeliveredAt()
		if start.IsZero() || !end.After(start) {
			continue
		}
		p.ratios[a.Worker] = append(p.ratios[a.Worker], float64(end.Sub(start))/float64(a.Brief.Estimate))
	}
	return p
}

func (p PaceStats) Factor(w WorkerID) float64 {
	rs := p.ratios[w]
	if len(rs) < MinSamples {
		return 1
	}
	sum := 0.0
	for _, r := range rs {
		sum += r
	}
	f := sum / float64(len(rs))
	return min(max(f, 0.5), 3)
}

func (p PaceStats) Samples(w WorkerID) int { return len(p.ratios[w]) }

// CalendarFactor is wall-clock time per hour of effort: people work about
// 8 hours a day, agents run around the clock. Unassigned work is assumed to
// go to a person.
func CalendarFactor(w *world.World, owner WorkerID) float64 {
	if owner != "" && w.Workers[owner].Kind.Agentic() {
		return 1
	}
	return 3
}

// Remaining is the wall-clock time an item still needs: its estimate,
// adjusted by its owner's pace and working hours, minus the time already
// spent on it.
func Remaining(w *world.World, it planning.Item, pace Pace) time.Duration {
	if it.Estimate <= 0 || !it.Open() {
		return 0
	}
	f := 1.0
	if pace != nil && it.Owner != "" {
		f = pace.Factor(it.Owner)
	}
	cf := CalendarFactor(w, it.Owner)
	total := time.Duration(float64(it.Estimate) * f * cf)
	if it.Status == planning.Active && !it.StartedAt.IsZero() {
		total -= w.Now.Sub(it.StartedAt)
	}
	return max(total, time.Duration(float64(it.Estimate)*cf/10)) // started work is never "done" by the clock alone
}

// Forecast is the expected finish of every open item, following dependencies.
func Forecast(w *world.World, pace Pace) map[ItemID]time.Time {
	out := map[ItemID]time.Time{}
	var finish func(id ItemID, depth int) time.Time
	finish = func(id ItemID, depth int) time.Time {
		if t, ok := out[id]; ok {
			return t
		}
		it := w.Items[id]
		start := w.Now
		if depth < 32 {
			for _, d := range it.DependsOn {
				if dep, ok := w.Items[d]; ok && dep.Open() {
					if f := finish(d, depth+1); f.After(start) {
						start = f
					}
				}
			}
		}
		t := start.Add(Remaining(w, it, pace))
		out[id] = t
		return t
	}
	for id, it := range w.Items {
		if it.Open() {
			finish(id, 0)
		}
	}
	return out
}

type Health string

const (
	Green  Health = "green"
	Yellow Health = "yellow"
	Red    Health = "red"
)

// Progress of a project.
type Progress struct {
	Done          float64    // 0..1, by estimated effort of leaf items
	Planned       float64    // 0..1 by the timebox; -1 without one
	Forecast      *time.Time // when the last open item is expected to finish
	NextMilestone *planning.Item
	Open, Total   int
	Health        Health
	Reasons       []string
}

// RiskCount is how many open signals of each level a project has (from attention).
type RiskCount struct{ High, Medium int }

// ProjectProgress computes progress and health.
func ProjectProgress(w *world.World, pid ProjectID, fc map[ItemID]time.Time, risks RiskCount) Progress {
	p := w.Projects[pid]
	g := w.Graph()
	var done, total float64
	pr := Progress{Planned: p.Elapsed(w.Now)}
	for _, it := range w.ItemsOf(pid, true) {
		if it.Status == planning.Dropped {
			continue
		}
		if it.Kind == planning.Milestone {
			if it.Open() && it.Due != nil && (pr.NextMilestone == nil || it.Due.Before(*pr.NextMilestone.Due)) {
				m := it
				pr.NextMilestone = &m
			}
			continue
		}
		if len(g.Children(it.ID)) > 0 {
			continue // parents are counted through their children
		}
		weight := it.Estimate.Hours()
		if weight <= 0 {
			weight = 1
		}
		total += weight
		pr.Total++
		if it.Status == planning.Done {
			done += weight
		} else {
			pr.Open++
			if f, ok := fc[it.ID]; ok && (pr.Forecast == nil || f.After(*pr.Forecast)) {
				f := f
				pr.Forecast = &f
			}
		}
	}
	if total > 0 {
		pr.Done = done / total
	}
	pr.Health = Green
	if pr.Planned >= 0 && pr.Done < pr.Planned-0.1 {
		pr.Health = Yellow
		pr.Reasons = append(pr.Reasons, fmt.Sprintf("进度 %.0f%% 落后于计划 %.0f%%", pr.Done*100, pr.Planned*100))
	}
	if risks.Medium > 0 && pr.Health == Green {
		pr.Health = Yellow
		pr.Reasons = append(pr.Reasons, fmt.Sprintf("%d 个中风险", risks.Medium))
	}
	if p.Timebox.Until != nil && pr.Forecast != nil && pr.Forecast.After(*p.Timebox.Until) {
		pr.Health = Red
		pr.Reasons = append(pr.Reasons, "预计完成时间晚于项目截止 "+p.Timebox.Until.Format("01-02"))
	}
	if risks.High > 0 {
		pr.Health = Red
		pr.Reasons = append(pr.Reasons, fmt.Sprintf("%d 个高风险", risks.High))
	}
	return pr
}
