package judge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/inbox"
	"github.com/chenhg5/bot-connect/internal/domain/insight"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
	"github.com/chenhg5/bot-connect/internal/domain/world"
)

// ---- triage: what an incoming message is ----

// Triage classifies incoming messages: intent, urgency, and — for a reply
// to a request waiting on the sender — what the reply does.
type Triage struct {
	J   Judge
	App *app.App
}

var _ app.Triage = (*Triage)(nil)

var intents = map[string]string{
	"chat":     "寒暄、闲聊、打招呼",
	"status":   "询问某件事或某个项目的进度、状态",
	"request":  "提出新的工作需求、任务或要求",
	"reply":    "对派给他的某件事的回复：接下、拒绝、改时间、提问、汇报进展、交付",
	"decision": "对一个待决事项做决定：同意、不同意、在选项里选一个",
	"who":      "问某件事该找谁、谁负责",
	"other":    "以上都不是",
}

var actions = map[string]string{
	"accept":   "答应接下这件事（没有提出改时间）",
	"decline":  "拒绝做",
	"counter":  "提出另一个完成时间或范围",
	"question": "就这件事提问",
	"progress": "汇报进展，还没做完",
	"deliver":  "说已经做完并交付",
	"none":     "不是对某件事的回复",
}

func (t *Triage) Assess(ctx context.Context, s inbox.Signal) (app.TriageResult, error) {
	state := map[string]any{"sender": s.ActorName, "sender_role": string(s.Actor.Role), "message": s.Body}
	if s.Actor.Worker != "" && t.App != nil {
		if v := t.App.AssigneeView(s.Actor.Worker); v != "" {
			state["requests_waiting_on_sender"] = v
		}
	}
	ans, err := t.J.Ask(ctx, state, map[string]Question{
		"intent": Choice("`sender` 发来的这条 `message` 主要是在做什么？", intents),
		"action": Choice("如果 `message` 是在回复派给 `sender` 的某件事（见 `requests_waiting_on_sender`），它在做什么？", actions),
		"urgent": Noul("`message` 是否需要马上处理（线上故障、有人被卡住、今天就要的结果、明确说很急）？"),
	})
	if err != nil {
		return app.TriageResult{}, err
	}
	r := app.TriageResult{Intent: ans["intent"].Choice}
	if a := ans["action"]; a.Choice != "none" && r.Intent == "reply" {
		r.Action, r.Confidence = a.Choice, a.Probabilities[a.Choice]
	}
	level := 0
	switch u := ans["urgent"].Noul; {
	case u >= 0.7:
		level = 2
	case u >= 0.4:
		level = 1
	}
	r.Level = &level
	return r, nil
}

// ---- guard: does an outgoing message leak? ----

// Guard checks messages to people outside the owner side before they go
// out: does the text reveal projects they aren't part of, other people's
// work, or the owner's private matters? Exact project titles are caught by
// code; paraphrases by the judgment.
type Guard struct {
	J         Judge
	App       *app.App
	Threshold float64 // block at or above (default 0.6)
}

// Verdict of a guard check.
type Verdict struct {
	Block  bool
	Reason string
	Score  float64
}

// Check judges text for a recipient (a platform user id; "" when unknown)
// who asked asked.
func (g *Guard) Check(ctx context.Context, recipient, asked, text string) Verdict {
	private, theirs := g.topics(recipient)
	low := strings.ToLower(text)
	for _, t := range private {
		if t != "" && strings.Contains(low, strings.ToLower(t)) {
			return Verdict{Block: true, Reason: "mentions 「" + t + "」, which they aren't part of", Score: 1}
		}
	}
	if g.J == nil {
		return Verdict{}
	}
	state := map[string]any{"recipient_projects": theirs, "private_topics": private, "recipient_asked": asked, "message": text}
	ans, err := g.J.Ask(ctx, state, map[string]Question{
		"leak": Noul("`message` 要发给一位同事。它是否透露了这位同事不该知道的内容：`private_topics` 里的项目或事情（哪怕换了说法）、" +
			"别人的具体工作或任务、主人的私事或机密？回答同事自己的工作、他参与的项目（`recipient_projects`）、或者告诉他该找谁，都不算泄露。"),
	})
	if err != nil {
		return Verdict{}
	}
	th := g.Threshold
	if th <= 0 {
		th = 0.6
	}
	if s := ans["leak"].Noul; s >= th {
		return Verdict{Block: true, Reason: fmt.Sprintf("may reveal something they aren't part of (%.2f)", s), Score: s}
	}
	return Verdict{Score: ans["leak"].Noul}
}

// topics splits open projects into those the recipient is not in (private)
// and those they are in.
func (g *Guard) topics(recipient string) (private, theirs []string) {
	if g.App == nil {
		return nil, nil
	}
	var who WorkerID
	if recipient != "" {
		who, _ = g.App.WorkerFor(recipient)
	}
	w := g.App.World(context.Background())
	for id, p := range w.Projects {
		if id == OrgProject || !p.Open() {
			continue
		}
		member := false
		for _, m := range p.Members {
			member = member || (who != "" && m.Worker == who)
		}
		for _, it := range w.Items {
			if it.Project == id && who != "" && it.Owner == who {
				member = true
			}
		}
		if member {
			theirs = append(theirs, p.Title)
		} else {
			private = append(private, p.Title)
		}
	}
	sort.Strings(private)
	sort.Strings(theirs)
	return
}

// ---- staffing: semantic fit ----

// Matcher scores how well each worker fits a need, by meaning rather than
// by matching words (an insight.Matcher).
type Matcher struct {
	J Judge
}

var _ insight.Matcher = Matcher{}

func (Matcher) Name() string { return "semantic" }

func (m Matcher) Match(n insight.Need, w *world.World) []insight.Candidate {
	if n.Text == "" || m.J == nil || n.Kind == NeedApproval {
		return nil
	}
	profiles := map[string]any{}
	qs := map[string]Question{}
	for id, wk := range w.Workers {
		if id == Owner {
			continue
		}
		var roles []string
		for _, p := range w.Projects {
			for _, mb := range p.ActiveMembers(w.Now) {
				if mb.Worker == id {
					roles = append(roles, fmt.Sprintf("%s：%s（%s）", p.Title, mb.Role, strings.Join(mb.Duties, "、")))
				}
			}
		}
		var caps []string
		for _, c := range wk.Capability {
			caps = append(caps, c.String())
		}
		key := "w_" + string(id)
		profiles[key] = map[string]any{"name": wk.Name, "kind": string(wk.Kind), "description": wk.Description, "skills": wk.Skills, "roles": roles, "capabilities": caps}
		qs[key] = Score("`workers."+key+"` 适不适合负责 `need` 这件事（看职责、角色、技能、能力）？",
			"完全不相关", "沾点边", "相关，能做", "正是他的职责")
	}
	if len(qs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	ans, err := m.J.Ask(ctx, map[string]any{"need": n.Text, "workers": profiles}, qs)
	if err != nil {
		return nil
	}
	var out []insight.Candidate
	for key, a := range ans {
		if a.Score < 1.5 { // below "related"
			continue
		}
		out = append(out, insight.Candidate{Worker: WorkerID(strings.TrimPrefix(key, "w_")), Score: a.Score,
			Reasons: []string{fmt.Sprintf("语义匹配 %.1f/3", a.Score)}})
	}
	return out
}
