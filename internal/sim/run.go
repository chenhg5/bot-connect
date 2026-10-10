package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// Result of one case.
type Result struct {
	ID         string
	Title      string
	Critical   bool
	Pass       bool
	Failures   []string
	Duration   time.Duration
	Tools      []string // tools used, in order
	Transcript string
}

// Run plays a case's steps and checks its expectations.
func (h *Harness) Run(ctx context.Context) Result {
	c := h.Case
	res := Result{ID: c.ID, Title: c.Title, Critical: c.Critical}
	start := time.Now()
	var tr strings.Builder
	fmt.Fprintf(&tr, "# %s %s\n(virtual start %s)\n", c.ID, c.Title, h.Clock.Now().Format("2006-01-02 15:04 Mon"))
	caseMsgs, caseTools := len(h.plat.snapshot()), len(h.rec.snapshot())
	for i, st := range c.Steps {
		msgs0, tools0, before := len(h.plat.snapshot()), len(h.rec.snapshot()), h.statuses()
		stepStart := time.Now()
		label, err := h.do(ctx, st)
		fmt.Fprintf(&tr, "\n## step %d: %s\n", i+1, label)
		if err != nil {
			res.Failures = append(res.Failures, fmt.Sprintf("step %d: %v", i+1, err))
			break
		}
		timeout := 3 * time.Minute
		if d, err := time.ParseDuration(st.Timeout); err == nil {
			timeout = d
		}
		if !h.waitIdle(timeout) {
			res.Failures = append(res.Failures, fmt.Sprintf("step %d: the bot was still busy after %s", i+1, timeout))
		}
		msgs, calls := h.plat.snapshot()[msgs0:], h.rec.snapshot()[tools0:]
		writeLog(&tr, msgs, calls)
		for _, ck := range st.Expect {
			if f := h.check(ck, msgs, calls, before, stepStart); f != "" {
				res.Failures = append(res.Failures, fmt.Sprintf("step %d %s", i+1, f))
			}
		}
	}
	msgs, calls := h.plat.snapshot()[caseMsgs:], h.rec.snapshot()[caseTools:]
	for _, ck := range c.Expect {
		if f := h.check(ck, msgs, calls, nil, start); f != "" {
			res.Failures = append(res.Failures, "end "+f)
		}
	}
	for _, x := range calls {
		res.Tools = append(res.Tools, x.Tool)
	}
	fmt.Fprintf(&tr, "\n## final state\n%s", h.stateText())
	res.Pass, res.Duration, res.Transcript = len(res.Failures) == 0, time.Since(start).Round(time.Second), tr.String()
	return res
}

// do performs one step.
func (h *Harness) do(ctx context.Context, st Step) (string, error) {
	switch {
	case st.Say != "":
		from := firstNonEmpty(st.From, "owner")
		user, chat, name := ownerUser, ownerChat, "chicken"
		if from != "owner" {
			user, chat, name = userOf(from), dmOf(from), from
			for _, p := range h.Case.People {
				if p.ID == from && p.Name != "" {
					name = p.Name
				}
			}
		}
		if st.Group != "" {
			chat = st.Group
		}
		h.plat.mu.Lock()
		h.plat.seq++
		id := fmt.Sprintf("m%d", h.plat.seq)
		h.plat.mu.Unlock()
		h.plat.on(hubInbound(chat, id, user, name, st.Say, st.Group != ""))
		where := "私聊"
		if st.Group != "" {
			where = "群 " + st.Group
		}
		return fmt.Sprintf("%s（%s）：%s", name, where, st.Say), nil
	case st.Advance != "":
		d, err := time.ParseDuration(st.Advance)
		if err != nil {
			return "", fmt.Errorf("advance: %v", err)
		}
		h.Clock.Advance(d)
		h.App.Tick(ctx)
		return fmt.Sprintf("时间过去 %s → %s", st.Advance, h.Clock.Now().Format("01-02 15:04 Mon")), nil
	case st.Deliver != "":
		var id AssignmentID
		h.App.Store.Read(func(s *app.State) {
			for _, a := range s.Assignments {
				if string(a.Worker) == st.Deliver && a.Open() {
					id = a.ID
				}
			}
		})
		if id == "" {
			return "", fmt.Errorf("%s has no open assignment to deliver", st.Deliver)
		}
		if err := h.App.Report(ctx, System("driver:agent"), id, app.Report{Action: "deliver", Result: st.Result}); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s 交付 %s：%s", st.Deliver, id, st.Result), nil
	}
	return "", fmt.Errorf("a step needs say, advance or deliver")
}

// statuses snapshots item and assignment statuses (for "unchanged").
func (h *Harness) statuses() map[string]string {
	out := map[string]string{}
	h.App.Store.Read(func(s *app.State) {
		for id, it := range s.Items {
			out["item:"+string(id)] = string(it.Status) + "|" + fmtT(it.Due)
		}
		for id, a := range s.Assignments {
			out["assignment:"+string(id)] = string(a.Status) + "|" + fmtT(a.Brief.Due)
		}
	})
	return out
}

func fmtT(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}

// chatsFor maps a check's target to the chats messages to them go to.
func (h *Harness) chatsFor(to string) []string {
	switch {
	case to == "" || to == "owner":
		return []string{ownerChat}
	case strings.HasPrefix(to, "group:"):
		return []string{strings.TrimPrefix(to, "group:")}
	}
	return []string{dmOf(to), "user:" + userOf(to)}
}

func (h *Harness) check(ck Check, msgs []Msg, calls []ToolCall, before map[string]string, since time.Time) string {
	why := ""
	if ck.Why != "" {
		why = "（" + ck.Why + "）"
	}
	switch ck.Type {
	case "reply", "no_message":
		var texts []string
		targets := h.chatsFor(ck.To)
		for _, m := range msgs {
			if ck.Type == "no_message" && ck.To == "" {
				texts = append(texts, m.Text)
				continue
			}
			for _, t := range targets {
				if m.Chat == t {
					texts = append(texts, m.Text)
				}
			}
		}
		if ck.Type == "no_message" {
			if len(texts) > 0 {
				return fmt.Sprintf("no_message to %q: got %d: %s%s", ck.To, len(texts), clip(texts[0], 120), why)
			}
			return ""
		}
		if len(texts) == 0 || len(texts) < ck.Min {
			return fmt.Sprintf("reply to %q: expected a message, got %d%s", firstNonEmpty(ck.To, "owner"), len(texts), why)
		}
		all := strings.Join(texts, "\n")
		if len(ck.ContainsAny) > 0 && !containsAny(all, ck.ContainsAny) {
			return fmt.Sprintf("reply to %q: none of %v in: %s%s", firstNonEmpty(ck.To, "owner"), ck.ContainsAny, clip(all, 200), why)
		}
		for _, x := range ck.NotContains {
			if strings.Contains(strings.ToLower(all), strings.ToLower(x)) {
				return fmt.Sprintf("reply to %q: must not contain %q: %s%s", firstNonEmpty(ck.To, "owner"), x, clip(all, 200), why)
			}
		}
	case "assignment", "no_assignment":
		n := 0
		h.App.Store.Read(func(s *app.State) {
			for _, a := range s.Assignments {
				if h.matchAssignment(s, a, ck) {
					n++
				}
			}
		})
		switch {
		case ck.Type == "no_assignment" && n > 0:
			return fmt.Sprintf("no_assignment %s: found %d%s", desc(ck), n, why)
		case ck.Type == "assignment" && ck.Count != nil && n != *ck.Count:
			return fmt.Sprintf("assignment %s: want %d, found %d%s", desc(ck), *ck.Count, n, why)
		case ck.Type == "assignment" && ck.Count == nil && n == 0:
			return fmt.Sprintf("assignment %s: none found; have:\n%s%s", desc(ck), h.stateText(), why)
		}
	case "item":
		found := false
		h.App.Store.Read(func(s *app.State) {
			for _, it := range s.Items {
				if (ck.Title == "" || strings.Contains(it.Title, ck.Title)) && (ck.Status == "" || string(it.Status) == ck.Status) && (ck.Due == "" || fmtT(it.Due) == ck.Due) {
					found = true
				}
			}
		})
		if !found {
			return fmt.Sprintf("item %s: none found; have:\n%s%s", desc(ck), h.stateText(), why)
		}
	case "tool":
		used := map[string]bool{}
		for _, c := range calls {
			if c.Error == "" {
				used[c.Tool] = true
			}
		}
		for _, t := range ck.Called {
			if !used[t] {
				return fmt.Sprintf("tool: %s was not used (used: %v)%s", t, keys(used), why)
			}
		}
		for _, t := range ck.NotCalled {
			if used[t] {
				return fmt.Sprintf("tool: %s must not be used%s", t, why)
			}
		}
	case "unchanged":
		after := h.statuses()
		for k, v := range before {
			if after[k] != v {
				return fmt.Sprintf("unchanged: %s went %s → %s%s", k, v, after[k], why)
			}
		}
		for k := range after {
			if _, ok := before[k]; !ok {
				return fmt.Sprintf("unchanged: %s was created%s", k, why)
			}
		}
	case "latency":
		max, err := time.ParseDuration(ck.Max)
		if err != nil {
			return "latency: bad max"
		}
		if len(msgs) == 0 {
			return "latency: no message" + why
		}
		if d := msgs[0].At.Sub(since); d > max {
			return fmt.Sprintf("latency: first message after %s (max %s)%s", d.Round(100*time.Millisecond), max, why)
		}
	default:
		return "unknown check type " + ck.Type
	}
	return ""
}

func (h *Harness) matchAssignment(s *app.State, a delegation.Assignment, ck Check) bool {
	title := s.Items[a.Item].Title + " " + a.Brief.Goal
	if parent := s.Items[a.Item].Parent; parent != "" {
		title += " " + s.Items[parent].Title
	}
	return (ck.Worker == "" || string(a.Worker) == ck.Worker) &&
		(ck.Kind == "" || string(a.Kind) == ck.Kind) &&
		(ck.Status == "" || statusIn(string(a.Status), ck.Status)) &&
		(ck.Title == "" || strings.Contains(title, ck.Title)) &&
		(ck.Due == "" || fmtT(a.Brief.Due) == ck.Due)
}

// statusIn: "offered|accepted" matches either.
func statusIn(s, want string) bool {
	for _, w := range strings.Split(want, "|") {
		if s == w {
			return true
		}
	}
	return false
}

func desc(ck Check) string {
	var p []string
	for k, v := range map[string]string{"worker": ck.Worker, "kind": ck.Kind, "status": ck.Status, "title": ck.Title, "due": ck.Due} {
		if v != "" {
			p = append(p, k+"="+v)
		}
	}
	sort.Strings(p)
	return "{" + strings.Join(p, " ") + "}"
}

func containsAny(s string, words []string) bool {
	low := strings.ToLower(s)
	for _, w := range words {
		if strings.Contains(low, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func writeLog(b *strings.Builder, msgs []Msg, calls []ToolCall) {
	type line struct {
		at   time.Time
		text string
	}
	var lines []line
	for _, m := range msgs {
		lines = append(lines, line{m.At, fmt.Sprintf("→ %s: %s", m.Chat, m.Text)})
	}
	for _, c := range calls {
		args, _ := json.Marshal(c.Args)
		t := fmt.Sprintf("· tool %s (%s) %s", c.Tool, c.Caller, clip(string(args), 300))
		if c.Error != "" {
			t += " ✗ " + clip(c.Error, 200)
		}
		lines = append(lines, line{c.At, t})
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].at.Before(lines[j].at) })
	for _, l := range lines {
		b.WriteString(l.text + "\n")
	}
}

// stateText lists items and assignments (for failures and transcripts).
func (h *Harness) stateText() string {
	var b strings.Builder
	h.App.Store.Read(func(s *app.State) {
		var ids []string
		for id := range s.Items {
			ids = append(ids, string(id))
		}
		sort.Strings(ids)
		for _, id := range ids {
			it := s.Items[ItemID(id)]
			fmt.Fprintf(&b, "- item %s「%s」%s due=%s owner=%s\n", it.ID, it.Title, it.Status, fmtT(it.Due), it.Owner)
		}
		var as []string
		for id := range s.Assignments {
			as = append(as, string(id))
		}
		sort.Strings(as)
		for _, id := range as {
			a := s.Assignments[AssignmentID(id)]
			fmt.Fprintf(&b, "- assignment %s %s→%s %s due=%s item=%s\n", a.ID, a.Kind, a.Worker, a.Status, fmtT(a.Brief.Due), a.Item)
		}
	})
	return b.String()
}
