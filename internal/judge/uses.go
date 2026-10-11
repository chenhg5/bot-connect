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

// The questions triage asks about a principal's request (shared with the
// benchmark in bench.go, so it measures what production asks).
var (
	ScopeQuestion      = Choice("`message` 是委托人交给 bot 的一句话。它属于哪一类？", scopes)
	ImpossibleQuestion = Noul("如果 `message` 提出了一个目标或要求，凭常识就能判断它在给定的时间或条件下基本不可能做到吗（比如一周内 10 万 star、一天开发完一个操作系统）？没有提出目标就回答否。")
)

// scopes: how big a principal's request is, which decides whether it needs a plan.
var scopes = map[string]string{
	"goal":    "结果导向、路径还不确定的目标（赚多少钱、把某件事做起来），要先调研可行性",
	"program": "要建立一套东西或推进一个多阶段的事：需要先调研、再出方案、再和人确认、再落地，涉及多个人或 agent 配合",
	"explore": "要边做边弄清楚的事：产品、创意、研发、写作，目标本身会在过程中变清楚，需要委托人反复看东西、给方向、试错",
	"handoff": "一件交付物明确的事，可以整件交给一个负责人或一个 agent（他自己会拆解）",
	"other":   "提问、闲聊、对已有事项的回复",
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
		"intent":     Choice("`sender` 发来的这条 `message` 主要是在做什么？", intents),
		"action":     Choice("如果 `message` 是在回复派给 `sender` 的某件事（见 `requests_waiting_on_sender`），它在做什么？", actions),
		"urgent":     Noul("`message` 是否需要马上处理（线上故障、有人被卡住、今天就要的结果、明确说很急）？"),
		"scope":      ScopeQuestion,
		"several":    Noul("`message` 里是否同时提了三件或更多互相独立的事（要分别处理）？"),
		"impossible": ImpossibleQuestion,
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
	isPrincipal := s.Actor.Role == RolePrincipal || s.Actor.Role == RoleAdmin
	if isPrincipal && ans["several"].Noul >= 0.7 {
		r.Hints = append(r.Hints, fmt.Sprintf("这条消息里有好几件独立的事（%.2f）：先用 triage 把它们分类（交出去 / 规划 / 探索 / 拍板 / 回答 / 记下 / 不做），再按类处理，别一件件凭感觉做。", ans["several"].Noul))
		return r, nil
	}
	if isPrincipal && ans["impossible"].Noul >= 0.7 {
		r.Hints = append(r.Hints, fmt.Sprintf("按常识这个要求在给定条件下基本做不到（%.2f）：直接说明为什么，并给出能做到的版本，不用派人去调研显而易见的事。", ans["impossible"].Noul))
	} else if sc := ans["scope"]; isPrincipal && sc.Probabilities["goal"]+sc.Probabilities["program"]+sc.Probabilities["explore"] >= 0.6 {
		r.NeedsPlan = true
		r.Scope = sc.Choice
		if sc.Choice == "explore" {
			r.Hints = append(r.Hints, fmt.Sprintf("这是要边做边弄清楚的事（%.2f）：用 plan draft kind=explore，列出要回答的问题，第一轮只做最便宜、能并行的尝试（几个不同方向的草稿 / 原型），做完拿给委托人看再决定下一轮；不要排完整路线。", sc.Probabilities["explore"]))
		} else if sc.Probabilities["goal"] >= sc.Probabilities["program"] {
			r.Hints = append(r.Hints, fmt.Sprintf("这是一个路径还不确定的目标（%.2f）：约束（资源、时间、底线）消息里没说清的才问，一次问完；已经说了就直接用 plan draft（kind=goal）安排 agent 并行调研可行性，结论要有调研支撑；不要直接 delegate。", sc.Probabilities["goal"]))
		} else {
			r.Hints = append(r.Hints, fmt.Sprintf("这是要多个 worker 配合、分阶段推进的事（%.2f）：先用 plan draft 规划（谁做什么、真实的依赖、并行、哪一步要人），调研和起草节点会马上派出去；不要直接 delegate。", sc.Probabilities["program"]))
		}
	}
	return r, nil
}

// ---- guard: does an outgoing message leak? ----

// Guard checks messages to people outside the principal side before they go
// out: does the text reveal projects they aren't part of, other people's
// work, or the principal's private matters? Exact project titles are caught by
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
			"别人的具体工作或任务、委托人的私事或机密？回答同事自己的工作、他参与的项目（`recipient_projects`）、或者告诉他该找谁，都不算泄露。"),
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
			if it.Project == id && who != "" && it.Assignee == who {
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
		if id == Principal {
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

// ---- plan review: an outside look before a plan is proposed ----

// PlanReview asks narrow questions about a plan, each a property a good PM's
// plan has. Low answers come back as notes for the brain to address or
// explain; nothing is blocked.
type PlanReview struct {
	J         Judge
	Threshold float64 // a note when the probability is below (default 0.5)
}

var _ app.PlanReviewer = (*PlanReview)(nil)

var planChecks = []struct{ id, q, note string }{
	{"agents_first", "`plan.nodes` 里交给人（executor_kind=人）的节点，是不是都确实需要人（做决定、审批、现实世界的动作、只有他知道的信息、关系），而调研、起草、分析、写代码这类事都交给了 agent？",
		"有交给人的节点其实 agent 能做：调研、起草、分析这类事先给 agent，人只做需要人的那一步"},
	{"parallel", "`plan.nodes` 里互不依赖的工作是不是安排成了并行（没有不必要的 depends_on），而不是一件做完再做下一件？",
		"有些节点之间没有真实的依赖，却排成了串行：去掉不必要的 depends_on，让它们并行"},
	{"feasibility", "如果 `plan.kind` 是 goal（结果导向、路径不确定的目标），计划是否先安排了调研来判断可行性，并且 `plan.feasibility` 的结论有调研或数据支撑（不是拍脑袋）？如果是 task，回答是。",
		"目标的可行性还没有依据：先安排调研节点，再下结论"},
	{"risk_fit", "计划的做法（先验证还是先投入、有没有止损条件 `plan.stop_if`）是否符合 `plan.style` 里委托人的风险偏好？",
		"做法和委托人的风险偏好不太一致：看看要不要先验证、加止损条件"},
	{"confirm_before_commit", "计划是否把真正的投入（改动、花钱、对外的动作）放在相关的人确认之后，而不是一开始就替人定好落地细节？",
		"落地细节定得太早：先和相关的人对齐方案，再展开落地节点"},
}

func (r *PlanReview) ReviewPlan(ctx context.Context, v app.PlanView) []string {
	if r.J == nil {
		return nil
	}
	nodes := make([]map[string]any, 0, len(v.Nodes))
	for _, n := range v.Nodes {
		nodes = append(nodes, map[string]any{"id": n.ID, "title": n.Title, "executor": n.Executor, "executor_kind": n.ExecutorKind,
			"why_person": n.Why, "output": n.Output, "depends_on": n.DependsOn, "compounding": n.Compounding})
	}
	state := map[string]any{"plan": map[string]any{"project": v.Project, "objective": v.Objective, "kind": string(v.Kind),
		"feasibility": map[string]any{"verdict": string(v.Feasibility.Verdict), "note": v.Feasibility.Note},
		"assumptions": v.Assumptions, "stop_if": v.StopIf, "style": v.Style, "nodes": nodes}}
	qs := map[string]Question{}
	for _, c := range planChecks {
		qs[c.id] = Noul(c.q)
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	ans, err := r.J.Ask(ctx, state, qs)
	if err != nil {
		return nil
	}
	th := r.Threshold
	if th <= 0 {
		th = 0.5
	}
	var notes []string
	for _, c := range planChecks {
		if a, ok := ans[c.id]; ok && a.Noul < th {
			notes = append(notes, fmt.Sprintf("%s（%.2f）", c.note, a.Noul))
		}
	}
	return notes
}

// ---- exploration: is a round going in circles? ----

// LearningCheck compares what a round concluded with earlier rounds.
type LearningCheck struct{ J Judge }

var _ app.LearningChecker = LearningCheck{}

func (l LearningCheck) CheckLearning(ctx context.Context, goal string, prior []string, now string) []string {
	if l.J == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ans, err := l.J.Ask(ctx, map[string]any{"goal": goal, "earlier_rounds": prior, "this_round": now}, map[string]Question{
		"circles":     Noul("`this_round` 是否回到了 `earlier_rounds` 里已经被放弃或否定过的方向（在原地打转）？"),
		"contradicts": Noul("`this_round` 的结论是否和 `earlier_rounds` 的某条结论相矛盾，而且没有说明为什么改变看法？"),
	})
	if err != nil {
		return nil
	}
	var notes []string
	if a := ans["circles"].Noul; a >= 0.6 {
		notes = append(notes, fmt.Sprintf("这一轮像是回到了之前放弃过的方向（%.2f）：和委托人说清楚这次有什么不同，否则别再走一遍", a))
	}
	if a := ans["contradicts"].Noul; a >= 0.6 {
		notes = append(notes, fmt.Sprintf("这一轮的结论和之前的矛盾（%.2f）：记下为什么改变看法", a))
	}
	return notes
}

// ---- pile triage: place many things in buckets at once ----

// Sorter places each thing in a bucket (what kind of handling it needs),
// and judges urgency and whether the bot can settle it alone.
type Sorter struct{ J Judge }

var _ app.Sorter = Sorter{}

var buckets = map[string]string{
	"handoff":  "一件交付物明确的事，可以整件交给一个负责人或一个 agent 去做",
	"program":  "要多个人或 agent 配合、分阶段推进的事（先调研、出方案、确认、再落地）",
	"goal":     "结果明确但路径不确定的目标，要先判断能不能做到",
	"explore":  "要边做边弄清楚的事：产品、创意、研发，需要委托人反复看东西给方向",
	"decision": "需要委托人或某个人在几个选项里拍板的事",
	"question": "一个问题，回答就行（已知的信息或查一下）",
	"info":     "告知性的信息，记下来就行，现在不用做什么",
	"drop":     "不值得做、已经做完、或者和别的事重复",
}

func (st Sorter) Sort(ctx context.Context, things []string, about string) ([]app.Sorted, error) {
	if st.J == nil {
		return nil, ErrUnavailable
	}
	out := make([]app.Sorted, len(things))
	type chunk struct{ from, to int }
	var chunks []chunk
	for i := 0; i < len(things); i += 10 {
		chunks = append(chunks, chunk{i, min(i+10, len(things))})
	}
	errs := make(chan error, len(chunks))
	for _, c := range chunks {
		go func(c chunk) {
			items := map[string]string{}
			qs := map[string]Question{}
			for i := c.from; i < c.to; i++ {
				k := fmt.Sprintf("t%d", i)
				items[k] = things[i]
				qs[k+"_b"] = Choice("`things."+k+"` 是委托人那边的一件事。它属于哪一类（决定用什么方式处理）？", buckets)
				qs[k+"_u"] = Noul("`things." + k + "` 是否今天就必须处理（有明确的今天的期限、线上故障、有人被卡住）？")
				qs[k+"_m"] = Noul("`things." + k + "` 是否不需要委托人做任何决定或提供信息，bot 安排 worker 就能办成？")
			}
			ans, err := st.J.Ask(ctx, map[string]any{"context": about, "things": items}, qs)
			if err != nil {
				errs <- err
				return
			}
			for i := c.from; i < c.to; i++ {
				k := fmt.Sprintf("t%d", i)
				b := ans[k+"_b"]
				out[i] = app.Sorted{Text: things[i], Bucket: app.Bucket(b.Choice), Confidence: b.Probabilities[b.Choice],
					Urgent: ans[k+"_u"].Noul, Mine: ans[k+"_m"].Noul}
			}
			errs <- nil
		}(c)
	}
	for range chunks {
		if err := <-errs; err != nil {
			return nil, err
		}
	}
	return out, nil
}
