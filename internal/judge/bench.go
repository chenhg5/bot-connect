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
