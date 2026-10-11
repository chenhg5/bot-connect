package judge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Component benchmarks: labelled data for one judgment, run with the same
// questions production asks, scored for accuracy, confusion and calibration.

// ScopeItem is one labelled principal message.
type ScopeItem struct {
	Text       string `json:"text"`
	Label      string `json:"label"` // handoff | program | goal | explore | other
	Impossible bool   `json:"impossible,omitempty"`
}

func ParseScopeItems(b []byte) ([]ScopeItem, error) {
	var out []ScopeItem
	sc := bufio.NewScanner(bytes.NewReader(b))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var it ScopeItem
		if err := json.Unmarshal([]byte(line), &it); err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		if _, ok := scopes[it.Label]; !ok {
			return nil, fmt.Errorf("line %d: unknown label %q", n, it.Label)
		}
		out = append(out, it)
	}
	return out, sc.Err()
}

// ScopeReport is how the scope judgment did.
type ScopeReport struct {
	N, Correct int
	Confusion  map[string]map[string]int // label → predicted → count
	// The decision production makes from it: does this need a plan first?
	GateN, GateCorrect int
	// Obviously impossible requests (threshold 0.7).
	ImpTP, ImpFP, ImpFN int
	// Calibration: mean probability of the chosen class when right / wrong.
	ConfRight, ConfWrong float64
	Misses               []string
	Errors               int
}

// BenchScope runs the scope and impossibility questions over the items.
func BenchScope(ctx context.Context, j Judge, items []ScopeItem) ScopeReport {
	type res struct {
		it  ScopeItem
		ans map[string]Answer
		err error
	}
	results := make([]res, len(items))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, it := range items {
		wg.Add(1)
		go func(i int, it ScopeItem) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ans, err := j.Ask(ctx, map[string]any{"sender_role": "principal", "message": it.Text},
				map[string]Question{"scope": ScopeQuestion, "impossible": ImpossibleQuestion})
			results[i] = res{it, ans, err}
		}(i, it)
	}
	wg.Wait()
	r := ScopeReport{Confusion: map[string]map[string]int{}}
	var right, wrong []float64
	for _, x := range results {
		if x.err != nil {
			r.Errors++
			continue
		}
		sc, imp := x.ans["scope"], x.ans["impossible"].Noul
		r.N++
		if r.Confusion[x.it.Label] == nil {
			r.Confusion[x.it.Label] = map[string]int{}
		}
		r.Confusion[x.it.Label][sc.Choice]++
		p := sc.Probabilities[sc.Choice]
		if sc.Choice == x.it.Label {
			r.Correct++
			right = append(right, p)
		} else {
			wrong = append(wrong, p)
			r.Misses = append(r.Misses, fmt.Sprintf("%s → %s (%.2f)：%s", x.it.Label, sc.Choice, p, x.it.Text))
		}
		saysImp := imp >= 0.7
		switch {
		case saysImp && x.it.Impossible:
			r.ImpTP++
		case saysImp && !x.it.Impossible:
			r.ImpFP++
			r.Misses = append(r.Misses, fmt.Sprintf("误判为做不到 (%.2f)：%s", imp, x.it.Text))
		case !saysImp && x.it.Impossible:
			r.ImpFN++
			r.Misses = append(r.Misses, fmt.Sprintf("没看出做不到 (%.2f)：%s", imp, x.it.Text))
		}
		if !x.it.Impossible { // the gate only applies to doable requests
			want := x.it.Label == "goal" || x.it.Label == "program" || x.it.Label == "explore"
			got := sc.Probabilities["goal"]+sc.Probabilities["program"]+sc.Probabilities["explore"] >= 0.6 && !saysImp
			r.GateN++
			if want == got {
				r.GateCorrect++
			}
		}
	}
	r.ConfRight, r.ConfWrong = mean(right), mean(wrong)
	sort.Strings(r.Misses)
	return r
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// Text renders the report.
func (r ScopeReport) Text() string {
	var b strings.Builder
	pct := func(a, n int) float64 { return 100 * float64(a) / float64(max(n, 1)) }
	fmt.Fprintf(&b, "scope：%d/%d 正确（%.0f%%）；规划闸门 %d/%d（%.0f%%）；做不到：TP %d FP %d FN %d", r.Correct, r.N, pct(r.Correct, r.N),
		r.GateCorrect, r.GateN, pct(r.GateCorrect, r.GateN), r.ImpTP, r.ImpFP, r.ImpFN)
	if r.Errors > 0 {
		fmt.Fprintf(&b, "；%d 个请求失败", r.Errors)
	}
	fmt.Fprintf(&b, "\n校准：选中类别的平均概率 对的 %.2f / 错的 %.2f\n", r.ConfRight, r.ConfWrong)
	labels := []string{"handoff", "program", "goal", "explore", "other"}
	b.WriteString("混淆矩阵（行=标签，列=判断）\n          ")
	for _, l := range labels {
		fmt.Fprintf(&b, "%8s", l)
	}
	b.WriteString("\n")
	for _, l := range labels {
		fmt.Fprintf(&b, "%-10s", l)
		for _, p := range labels {
			fmt.Fprintf(&b, "%8d", r.Confusion[l][p])
		}
		b.WriteString("\n")
	}
	if len(r.Misses) > 0 {
		b.WriteString("错的：\n- " + strings.Join(r.Misses, "\n- ") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// LabelItem is one labelled thing for a choice judgment.
type LabelItem struct {
	Text  string `json:"text"`
	Label string `json:"label"`
}

// ClaimItem is one labelled reply for the honesty check.
type ClaimItem struct {
	Reply       string   `json:"reply"`
	Succeeded   []string `json:"succeeded"`
	Failed      []string `json:"failed"`
	Unsupported bool     `json:"unsupported"`
}

func parseLines[T any](b []byte) ([]T, error) {
	var out []T
	sc := bufio.NewScanner(bytes.NewReader(b))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var x T
		if err := json.Unmarshal([]byte(line), &x); err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		out = append(out, x)
	}
	return out, sc.Err()
}

func ParseLabelItems(b []byte) ([]LabelItem, error) { return parseLines[LabelItem](b) }
func ParseClaimItems(b []byte) ([]ClaimItem, error) { return parseLines[ClaimItem](b) }

// Report is a generic result: accuracy, confusion, calibration, misses.
type Report struct {
	Name                 string
	N, Correct, Errors   int
	Confusion            map[string]map[string]int
	ConfRight, ConfWrong float64
	Misses               []string
}

func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s：%d/%d 正确（%.0f%%）", r.Name, r.Correct, r.N, 100*float64(r.Correct)/float64(max(r.N, 1)))
	if r.Errors > 0 {
		fmt.Fprintf(&b, "；%d 个请求失败", r.Errors)
	}
	fmt.Fprintf(&b, "；校准 对的 %.2f / 错的 %.2f\n", r.ConfRight, r.ConfWrong)
	if len(r.Misses) > 0 {
		b.WriteString("错的：\n- " + strings.Join(r.Misses, "\n- ") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// BenchBuckets runs the pile sorter over labelled things (one batch, as the
// triage tool does).
func BenchBuckets(ctx context.Context, j Judge, items []LabelItem) Report {
	r := Report{Name: "buckets", Confusion: map[string]map[string]int{}}
	things := make([]string, len(items))
	for i, it := range items {
		things[i] = it.Text
	}
	got, err := Sorter{J: j}.Sort(ctx, things, "")
	if err != nil {
		r.Errors = len(items)
		return r
	}
	var right, wrong []float64
	for i, it := range items {
		g := got[i]
		r.N++
		if r.Confusion[it.Label] == nil {
			r.Confusion[it.Label] = map[string]int{}
		}
		r.Confusion[it.Label][string(g.Bucket)]++
		if string(g.Bucket) == it.Label {
			r.Correct++
			right = append(right, g.Confidence)
		} else {
			wrong = append(wrong, g.Confidence)
			r.Misses = append(r.Misses, fmt.Sprintf("%s → %s (%.2f)：%s", it.Label, g.Bucket, g.Confidence, it.Text))
		}
	}
	r.ConfRight, r.ConfWrong = mean(right), mean(wrong)
	sort.Strings(r.Misses)
	return r
}

// BenchClaims runs the honesty check (threshold 0.7, as production).
func BenchClaims(ctx context.Context, j Judge, items []ClaimItem) Report {
	r := Report{Name: "claims"}
	var right, wrong []float64
	for _, it := range items {
		p, err := ClaimCheck(ctx, j, "chicken", it.Reply, it.Succeeded, it.Failed)
		if err != nil {
			r.Errors++
			continue
		}
		r.N++
		conf := p
		if !it.Unsupported {
			conf = 1 - p
		}
		if (p >= 0.7) == it.Unsupported {
			r.Correct++
			right = append(right, conf)
		} else {
			wrong = append(wrong, conf)
			r.Misses = append(r.Misses, fmt.Sprintf("标注 %v，判断 %.2f：%s（成功 %v，失败 %v）", it.Unsupported, p, it.Reply, it.Succeeded, it.Failed))
		}
	}
	r.ConfRight, r.ConfWrong = mean(right), mean(wrong)
	return r
}
