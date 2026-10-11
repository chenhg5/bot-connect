package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// Triage of a pile: when many things arrive at once — a principal listing
// what's on their mind, an inbox after a day away — sort them into buckets
// before doing any of them. Each bucket has its own machinery, so putting a
// thing in the right one is most of handling it well.

// Bucket is where a thing goes, and what handles it.
type Bucket string

const (
	BucketHandoff  Bucket = "handoff"  // one owner can take it whole: delegate
	BucketProgram  Bucket = "program"  // several workers, stages: plan (task)
	BucketGoal     Bucket = "goal"     // known outcome, open path: plan (goal), feasibility first
	BucketExplore  Bucket = "explore"  // becomes clear by doing: plan (explore), rounds with the principal
	BucketDecision Bucket = "decision" // the principal (or someone) must choose: ask, with options
	BucketQuestion Bucket = "question" // answer it (from what we know, or an agent looks it up)
	BucketInfo     Bucket = "info"     // nothing to do now: note it (project card, person note)
	BucketDrop     Bucket = "drop"     // not worth doing / already done / duplicate: say so
)

// Next is what to do with a bucket.
var bucketNext = map[Bucket]string{
	BucketHandoff:  "整件交出去：find_people → delegate",
	BucketProgram:  "要多方配合：plan draft（task）",
	BucketGoal:     "路径未知的目标：问清约束 → plan draft（goal），先调研可行性",
	BucketExplore:  "边做边弄清楚：plan draft（explore），第一轮做便宜的尝试给委托人看",
	BucketDecision: "需要拍板：整理选项和建议，问委托人（或该拍板的人）",
	BucketQuestion: "直接回答，或让 agent 查",
	BucketInfo:     "记下来（项目卡片 / note_person），不用行动",
	BucketDrop:     "说明为什么不做（重复、已完成、不值得）",
}

var bucketOrder = []Bucket{BucketDecision, BucketHandoff, BucketQuestion, BucketProgram, BucketGoal, BucketExplore, BucketInfo, BucketDrop}

var bucketZh = map[Bucket]string{BucketHandoff: "整件交出去", BucketProgram: "需要规划", BucketGoal: "目标（先判断可行性）",
	BucketExplore: "边做边探索", BucketDecision: "要拍板", BucketQuestion: "直接回答", BucketInfo: "记下就行", BucketDrop: "可以不做"}

// Sorted is one thing, placed.
type Sorted struct {
	Text       string
	Bucket     Bucket
	Confidence float64 // of the bucket
	Urgent     float64 // probability it must be handled today
	Mine       float64 // probability the bot can settle it without the principal deciding anything
}

// Sorter places things in buckets (the L1 judgment).
type Sorter interface {
	Sort(ctx context.Context, things []string, about string) ([]Sorted, error)
}

// Sort places things; without a sorter everything is unplaced.
func (a *App) Sort(ctx context.Context, things []string, about string) ([]Sorted, error) {
	var clean []string
	for _, t := range things {
		if t = strings.TrimSpace(t); t != "" {
			clean = append(clean, t)
		}
	}
	if len(clean) == 0 {
		return nil, Invalid("nothing to sort")
	}
	if a.Sorter == nil {
		out := make([]Sorted, len(clean))
		for i, t := range clean {
			out[i] = Sorted{Text: t}
		}
		return out, nil
	}
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return a.Sorter.Sort(tctx, clean, about)
}

// DescribeSorted renders buckets in the order to work through them, urgent
// things first within each, with what to do and what's uncertain.
func DescribeSorted(ss []Sorted) string {
	by := map[Bucket][]Sorted{}
	var unsure, unplaced []Sorted
	for _, s := range ss {
		switch {
		case s.Bucket == "":
			unplaced = append(unplaced, s)
		default:
			by[s.Bucket] = append(by[s.Bucket], s)
			if s.Confidence < 0.5 {
				unsure = append(unsure, s)
			}
		}
	}
	var b strings.Builder
	var urgent []string
	for _, s := range ss {
		if s.Urgent >= 0.6 {
			urgent = append(urgent, fmt.Sprintf("「%s」", oneLine(s.Text, 40)))
		}
	}
	if len(urgent) > 0 {
		b.WriteString("今天必须处理：" + strings.Join(urgent, "、") + "\n")
	}
	for _, k := range bucketOrder {
		xs := by[k]
		if len(xs) == 0 {
			continue
		}
		sort.SliceStable(xs, func(i, j int) bool { return xs[i].Urgent > xs[j].Urgent })
		fmt.Fprintf(&b, "【%s】%d 件 —— %s\n", bucketZh[k], len(xs), bucketNext[k])
		for _, s := range xs {
			line := "  - " + oneLine(s.Text, 80)
			var tags []string
			if s.Urgent >= 0.6 {
				tags = append(tags, "急")
			}
			if s.Mine < 0.4 {
				tags = append(tags, "要委托人参与")
			}
			if s.Confidence < 0.5 {
				tags = append(tags, fmt.Sprintf("分类拿不准 %.2f", s.Confidence))
			}
			if len(tags) > 0 {
				line += "（" + strings.Join(tags, "，") + "）"
			}
			b.WriteString(line + "\n")
		}
	}
	if len(unplaced) > 0 {
		b.WriteString("没有判断层，未分类：\n")
		for _, s := range unplaced {
			b.WriteString("  - " + oneLine(s.Text, 80) + "\n")
		}
	}
	if len(unsure) > 0 {
		b.WriteString("分类拿不准的先想清楚它到底是什么，再动手。\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
