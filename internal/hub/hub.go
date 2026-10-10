// Package hub is the access layer: it receives messages from IM platforms and
// task events from workers, groups them into per-conversation lanes, decides
// which lane gets a "brain turn" next, and delivers the replies.
//
// Scheduling rules (see docs/design.md):
//   - One turn in flight per conversation; anything arriving meanwhile is
//     buffered and coalesced into that conversation's next turn.
//   - A short debounce lets rapid-fire messages land in the same turn.
//   - At most MaxConcurrent turns run at once across all conversations.
//   - When slots are scarce, lanes are served by priority
//     (owner message < task event < visitor message), then by waiting time.
package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/bot-connect/internal/audit"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/worker"
)

// Inbound is a message as a platform reports it. The platform fills in
// what its event carries; the hub resolves the rest of the sender's identity.
type Inbound struct {
	Platform  string
	ChatID    string
	MessageID string
	SenderID  string // platform user id (required)
	Sender    string // display name, if the event carries one
	UnionID   string // cross-app id, if the event carries one
	Text      string
	IsGroup   bool
}

type Platform interface {
	Name() string
	Start(ctx context.Context, onMessage func(Inbound)) error
	Send(ctx context.Context, chatID, text string) error
	Ack(ctx context.Context, messageID string)
}

// UserSender is implemented by platforms that can message a user directly
// (not only reply in a chat): used to reach people the bot hands work to.
type UserSender interface {
	SendUser(ctx context.Context, userID, text string) (messageID string, err error)
}

const (
	KindMessage   = "message"
	KindTaskEvent = "task_event"
	KindSchedule  = "schedule"
	KindPM        = "pm"     // project management: assignment updates, risks, follow-ups
	KindSignal    = "signal" // a wake-up of the owner-side main session
)

type Item struct {
	Kind       string
	Text       string
	From       worker.Requester
	At         time.Time
	ScheduleID string   // KindSchedule: the job that fired
	SignalIDs  []string // KindSignal: the signals this wake-up is about
	ReplyConv  string   // KindSignal: where the plain reply goes
}

func (it Item) priority() int {
	switch {
	case it.Kind == KindMessage && it.From.Privileged():
		return 0
	case it.Kind == KindTaskEvent || it.Kind == KindSchedule || it.Kind == KindPM || it.Kind == KindSignal:
		return 1
	}
	return 2
}

// Message is one entry of a conversation's plain-text history.
type Message struct {
	Role string `json:"role"` // user | assistant
	Text string `json:"text"`
}

type Conversation struct {
	Key      string    `json:"key"`
	Platform string    `json:"platform"`
	ChatID   string    `json:"chat_id"`
	IsGroup  bool      `json:"is_group"`
	History  []Message `json:"history"`
	// Brain session to resume for this conversation, tagged with the brain
	// kind that created it so switching brains starts fresh.
	BrainKind    string `json:"brain_kind,omitempty"`
	BrainSession string `json:"brain_session,omitempty"`
}

// Turn is one unit of work for the brain: everything pending in a lane.
type Turn struct {
	Conv  *Conversation
	Items []Item
	// Caller is the least-privileged requester among the items: if any item
	// came from a visitor, the whole turn runs with visitor rights.
	Caller worker.Requester
}

type Brain interface {
	HandleTurn(ctx context.Context, t Turn) (reply string, err error)
}

type Options struct {
	// KeyPrefix namespaces this bot's conversations ("" for a single bot).
	KeyPrefix string
	// PerUser: in group chats each sender gets their own conversation.
	PerUser       bool
	Scope         worker.Scope // the bot's view of the worker pool (for /status)
	Identity      *identity.Resolver
	Policy        *identity.StaticPolicy // for defaulting owners from the platform; may be nil
	Audit         audit.Sink
	MaxConcurrent int
	Debounce      time.Duration
	TurnTimeout   time.Duration
	HistoryLimit  int
	DataDir       string
	// ScheduleDone is told when a turn that handled a scheduled job ends.
	ScheduleDone func(id string)
	// Intake takes a message into the main session as an attention signal.
	// It returns false to leave the message in its conversation's own lane
	// (e.g. visitors when visitors are isolated).
	Intake func(in Inbound, user identity.User, convKey string) bool
	// TurnDone is told when a main-session turn ends: the signals it was
	// woken for and whether it answered.
	TurnDone func(signalIDs []string, answered bool)
}

type lane struct {
	conv    *Conversation
	pending []Item
	running bool
	firstAt time.Time
	lastAt  time.Time
}

type Hub struct {
	opts      Options
	brain     Brain
	workers   *worker.Manager
	platforms map[string]Platform

	mu        sync.Mutex
	lanes     map[string]*lane
	convs     map[string]*Conversation
	ownerChat map[string]string // platform → owner's latest p2p chat id
	seen      map[string]time.Time
	slots     chan struct{}
	kick      chan struct{}
	ctx       context.Context
}

func New(opts Options, workers *worker.Manager) *Hub {
	if opts.Audit == nil {
		opts.Audit = audit.Nop{}
	}
	h := &Hub{opts: opts, workers: workers, platforms: map[string]Platform{},
		lanes: map[string]*lane{}, convs: map[string]*Conversation{}, ownerChat: map[string]string{},
		seen: map[string]time.Time{}, slots: make(chan struct{}, opts.MaxConcurrent), kick: make(chan struct{}, 1)}
	h.load()
	return h
}

func (h *Hub) SetBrain(b Brain)       { h.brain = b }
func (h *Hub) AddPlatform(p Platform) { h.platforms[p.Name()] = p }

func (h *Hub) Start(ctx context.Context) error {
	h.ctx = ctx
	for _, p := range h.platforms {
		if d, ok := p.(identity.Directory); ok {
			h.opts.Identity.AddDirectory(p.Name(), d)
		}
		if pol := h.opts.Policy; pol != nil && !pol.HasOwners() {
			if src, ok := p.(identity.OwnerSource); ok {
				for _, id := range src.DefaultOwners(ctx) {
					pol.AddOwner(id)
					slog.Info("owner defaults to the app owner", "platform", p.Name(), "id", id)
				}
			}
		}
		if err := p.Start(ctx, h.onInbound); err != nil {
			return fmt.Errorf("start %s: %w", p.Name(), err)
		}
	}
	go h.dispatch(ctx)
	return nil
}

func (h *Hub) convKey(platform, chatID string) string {
	return h.opts.KeyPrefix + platform + ":" + chatID
}

// scopeFor is the bot's worker scope as seen by one user.
func (h *Hub) scopeFor(u identity.User) worker.Scope {
	sc := h.opts.Scope
	sc.Caller = u
	return sc
}

// ---------- inbound ----------

func (h *Hub) onInbound(in Inbound) {
	if in.MessageID != "" && h.dedup(in.MessageID) {
		return
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return
	}
	if p := h.platforms[in.Platform]; p != nil && in.MessageID != "" {
		go p.Ack(context.Background(), in.MessageID)
	}
	user := h.opts.Identity.Resolve(context.Background(), identity.User{
		Platform: in.Platform, ID: in.SenderID, Name: in.Sender, UnionID: in.UnionID})
	key := h.convKey(in.Platform, in.ChatID)
	if in.IsGroup && h.opts.PerUser {
		key += "#" + in.SenderID
	}
	chatType := map[bool]string{true: "group", false: "p2p"}[in.IsGroup]
	slog.Info("inbound", "conv", key, "chat_type", chatType, "sender", user.ID, "name", user.Name, "role", user.Role, "text", clip(text, 80))
	h.opts.Audit.Record(audit.Event{Type: audit.Inbound, Platform: in.Platform, Conv: key, ChatType: chatType,
		MessageID: in.MessageID, User: &user, Text: audit.Clip(text, 4000)})

	h.mu.Lock()
	conv := h.convLocked(key, in.Platform, in.ChatID, in.IsGroup)
	if user.Role == identity.RoleOwner && !in.IsGroup {
		h.ownerChat[in.Platform] = in.ChatID
	}
	h.mu.Unlock()

	if strings.HasPrefix(text, "/") {
		target := conv
		if !in.IsGroup && user.Privileged() && h.opts.Intake != nil {
			target = h.mainConv() // /reset etc. act on the main session
		}
		if h.command(target, in, user, text) {
			return
		}
	}
	if h.opts.Intake != nil && h.opts.Intake(in, user, key) {
		return
	}
	h.enqueue(key, Item{Kind: KindMessage, Text: text, At: time.Now(), From: user})
}

// PostTaskEvent feeds a finished worker task back into the conversation that
// requested it, so the brain can report the result in context.
func (h *Hub) PostTaskEvent(t worker.Task) {
	var b strings.Builder
	fmt.Fprintf(&b, "任务 %s（worker: %s）%s，耗时 %s。\n指令：%s\n", t.ID, t.Worker, statusZh(t.Status),
		t.EndedAt.Sub(t.StartedAt).Round(time.Second), clip(t.Instruction, 300))
	if t.Error != "" {
		fmt.Fprintf(&b, "错误：%s\n", clip(t.Error, 800))
	}
	if t.Result != "" {
		fmt.Fprintf(&b, "worker 最后的输出：\n%s", clip(t.Result, 4000))
	}
	h.mu.Lock()
	_, ok := h.convs[t.ConvKey]
	h.mu.Unlock()
	if !ok {
		slog.Warn("task event for unknown conversation", "task", t.ID, "conv", t.ConvKey)
		return
	}
	h.enqueue(t.ConvKey, Item{Kind: KindTaskEvent, Text: b.String(), From: t.Requester, At: time.Now()})
}

// PostScheduled delivers a due scheduled job into its conversation. It
// returns false if the conversation is unknown (e.g. state was reset).
func (h *Hub) PostScheduled(convKey, id, label, prompt string, creator worker.Requester) bool {
	h.mu.Lock()
	_, ok := h.convs[convKey]
	h.mu.Unlock()
	if !ok {
		return false
	}
	text := fmt.Sprintf("定时任务 %s「%s」触发，执行：\n%s", id, label, prompt)
	h.enqueue(convKey, Item{Kind: KindSchedule, Text: text, From: creator, At: time.Now(), ScheduleID: id})
	return true
}

// PostEvent delivers a project-management event (assignment update, risk,
// follow-up) into a conversation. It returns false if the conversation is
// unknown.
func (h *Hub) PostEvent(convKey, text string, about worker.Requester) bool {
	h.mu.Lock()
	_, ok := h.convs[convKey]
	h.mu.Unlock()
	if !ok {
		return false
	}
	h.enqueue(convKey, Item{Kind: KindPM, Text: text, From: about, At: time.Now()})
	return true
}

// SendUser messages a user directly on the given platform (falling back to
// any platform that can, e.g. the console). It returns the message id.
func (h *Hub) SendUser(ctx context.Context, platform, userID, text string) (string, error) {
	h.mu.Lock()
	var us UserSender
	name := platform
	if p, ok := h.platforms[platform].(UserSender); ok {
		us = p
	} else {
		for n, p := range h.platforms {
			if s, ok := p.(UserSender); ok {
				us, name = s, n
				break
			}
		}
	}
	h.mu.Unlock()
	if us == nil {
		return "", fmt.Errorf("no platform can message users directly")
	}
	id, err := us.SendUser(ctx, userID, text)
	h.opts.Audit.Record(audit.Event{Type: audit.Outbound, Platform: name, Conv: "user:" + userID, Text: audit.Clip(text, 2000), Error: errString(err)})
	return id, err
}

// BrainSession returns the brain session to resume for c, if it was created
// by the same kind of brain.
func (h *Hub) BrainSession(c *Conversation, kind string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.BrainKind != kind {
		return ""
	}
	return c.BrainSession
}

// BrainKind returns the kind of brain session the conversation has, if any.
func (h *Hub) BrainKind(c *Conversation) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return c.BrainKind
}

// ResetConversation drops the brain session and history (like /reset).
func (h *Hub) ResetConversation(c *Conversation) {
	h.mu.Lock()
	c.History, c.BrainKind, c.BrainSession = nil, "", ""
	h.mu.Unlock()
	h.save()
}

func (h *Hub) SetBrainSession(c *Conversation, kind, id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c.BrainKind, c.BrainSession = kind, id
}

// SendTo posts a message to a conversation immediately (outside the turn reply).
func (h *Hub) SendTo(ctx context.Context, convKey, text string) error {
	h.mu.Lock()
	c := h.convs[convKey]
	h.mu.Unlock()
	if convKey == h.MainKey() {
		c = h.replyTarget(h.mainConv(), nil)
	}
	if c == nil {
		return fmt.Errorf("unknown conversation %s", convKey)
	}
	p := h.platforms[c.Platform]
	if p == nil {
		return fmt.Errorf("platform %s not available", c.Platform)
	}
	err := p.Send(ctx, c.ChatID, text)
	h.opts.Audit.Record(audit.Event{Type: audit.Outbound, Platform: c.Platform, Conv: convKey, Text: audit.Clip(text, 2000), Error: errString(err)})
	return err
}

// SetIntake routes messages into the main session (intake turns them into
// signals) and reports main-session turns.
func (h *Hub) SetIntake(intake func(in Inbound, user identity.User, convKey string) bool, turnDone func(ids []string, answered bool)) {
	h.opts.Intake, h.opts.TurnDone = intake, turnDone
}

// MainKey is the main session: the bot's one continuous working context.
// Everyone's messages and the scaffold's signals arrive there; each turn
// runs with the rights of whoever it is for (never mixing a colleague's
// signal with the owner's in one turn), and replies go back where each
// signal came from.
func (h *Hub) MainKey() string { return h.opts.KeyPrefix + "main" }

func (h *Hub) mainConv() *Conversation {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.convLocked(h.MainKey(), "main", "main", false)
}

// WakeMain hands a wake-up to the main session; from is whose rights the
// turn runs with (the scaffold's own signals run as the owner).
func (h *Hub) WakeMain(text string, signalIDs []string, replyConv string, from identity.User) {
	if from.ID == "" {
		from = MainUser
	}
	h.mainConv()
	h.enqueue(h.MainKey(), Item{Kind: KindSignal, Text: text, From: from, At: time.Now(), SignalIDs: signalIDs, ReplyConv: replyConv})
}

// MainUser is who the scaffold's own wake-ups run as.
var MainUser = identity.User{ID: "bot-connect", Name: "bot-connect", Role: identity.RoleOwner}

// sameAudience: items that may share a main-session turn — the owner side
// together, each other person alone.
func sameAudience(a, b Item) bool {
	if a.From.Privileged() && b.From.Privileged() {
		return true
	}
	return a.From.ID == b.From.ID
}

// replyTarget resolves where a turn's plain reply goes: for the main
// session, the conversation the focus came from (else the owner's chat).
func (h *Hub) replyTarget(conv *Conversation, items []Item) *Conversation {
	if conv.Key != h.MainKey() {
		return conv
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(items) - 1; i >= 0; i-- {
		if c := h.convs[items[i].ReplyConv]; c != nil && c.Key != h.MainKey() {
			return c
		}
	}
	for pname, chat := range h.ownerChat {
		if c := h.convs[h.convKey(pname, chat)]; c != nil {
			return c
		}
	}
	return nil
}

// OwnerConv is the conversation key of the owner's private chat ("" until
// the owner has talked to the bot).
func (h *Hub) OwnerConv() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for pname, chat := range h.ownerChat {
		return h.convKey(pname, chat)
	}
	return ""
}

// NotifyOwner sends a message straight to the owner's private chat.
func (h *Hub) NotifyOwner(ctx context.Context, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for pname, chat := range h.ownerChat {
		if p := h.platforms[pname]; p != nil {
			return p.Send(ctx, chat, text)
		}
	}
	return fmt.Errorf("owner has not talked to the bot yet; no private chat to notify")
}

func (h *Hub) enqueue(key string, it Item) {
	h.mu.Lock()
	l := h.lanes[key]
	if l == nil {
		l = &lane{conv: h.convs[key]}
		h.lanes[key] = l
	}
	if len(l.pending) == 0 {
		l.firstAt = it.At
	}
	l.pending = append(l.pending, it)
	l.lastAt = it.At
	h.mu.Unlock()
	h.poke()
}

func (h *Hub) poke() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

// ---------- dispatcher ----------

func (h *Hub) dispatch(ctx context.Context) {
	for {
		wait := h.fill(ctx)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-h.kick:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// fill starts turns while slots are free and returns how long to sleep until
// the next debounced lane becomes eligible.
func (h *Hub) fill(ctx context.Context) time.Duration {
	for {
		l, wait := h.pickReady()
		if l == nil {
			return wait
		}
		select {
		case h.slots <- struct{}{}:
			go h.runTurn(ctx, l)
		default: // all slots busy; a finishing turn will poke us
			h.releasePick(l)
			return time.Second
		}
	}
}

// pickReady chooses the best eligible lane and marks it running. If none is
// eligible it returns the time until the earliest debounce expires.
func (h *Hub) pickReady() (*lane, time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	wait := time.Second
	var ready []*lane
	for _, l := range h.lanes {
		if l.running || len(l.pending) == 0 {
			continue
		}
		if d := h.opts.Debounce - now.Sub(l.lastAt); d > 0 {
			if d < wait {
				wait = d
			}
			continue
		}
		ready = append(ready, l)
	}
	if len(ready) == 0 {
		return nil, wait
	}
	sort.Slice(ready, func(i, j int) bool {
		pi, pj := lanePriority(ready[i]), lanePriority(ready[j])
		if pi != pj {
			return pi < pj
		}
		return ready[i].firstAt.Before(ready[j].firstAt)
	})
	ready[0].running = true
	return ready[0], 0
}

func (h *Hub) releasePick(l *lane) {
	h.mu.Lock()
	l.running = false
	h.mu.Unlock()
}

func lanePriority(l *lane) int {
	p := 9
	for _, it := range l.pending {
		if v := it.priority(); v < p {
			p = v
		}
	}
	return p
}

func (h *Hub) runTurn(ctx context.Context, l *lane) {
	defer func() {
		<-h.slots
		h.mu.Lock()
		l.running = false
		h.mu.Unlock()
		h.save()
		h.poke()
	}()
	h.mu.Lock()
	items := l.pending
	l.pending = nil
	if l.conv != nil && l.conv.Key == h.MainKey() && len(items) > 1 {
		// one audience per turn: rights follow whoever the turn is for
		n := 1
		for n < len(items) && sameAudience(items[0], items[n]) {
			n++
		}
		items, l.pending = items[:n], append([]Item(nil), items[n:]...)
	}
	conv := l.conv
	h.mu.Unlock()
	defer func() {
		if h.opts.ScheduleDone != nil {
			for _, it := range items {
				if it.ScheduleID != "" {
					h.opts.ScheduleDone(it.ScheduleID)
				}
			}
		}
	}()

	turn := Turn{Conv: conv, Items: items, Caller: leastPrivileged(items)}
	waited := time.Since(items[0].At).Round(time.Millisecond)
	slog.Info("turn start", "conv", conv.Key, "items", len(items), "caller", turn.Caller.Display(), "role", turn.Caller.Role, "waited", waited)
	caller := turn.Caller
	h.opts.Audit.Record(audit.Event{Type: audit.TurnStart, Platform: conv.Platform, Conv: conv.Key, User: &caller,
		Extra: map[string]any{"items": len(items), "waited": waited.String()}})
	tctx, cancel := context.WithTimeout(ctx, h.opts.TurnTimeout)
	defer cancel()
	start := time.Now()
	reply, err := h.brain.HandleTurn(tctx, turn)
	if err != nil {
		slog.Error("turn failed", "conv", conv.Key, "err", err)
		reply = "抱歉，我这边处理出错了：" + clip(err.Error(), 200)
	}
	dur := time.Since(start).Round(time.Millisecond)
	slog.Info("turn end", "conv", conv.Key, "dur", dur)
	h.opts.Audit.Record(audit.Event{Type: audit.TurnEnd, Platform: conv.Platform, Conv: conv.Key, User: &caller,
		Text: audit.Clip(reply, 4000), Error: errString(err), Duration: dur.String()})
	r := strings.TrimSpace(reply)
	answered := r != "" && r != "NO_REPLY"
	if conv.Key == h.MainKey() && h.opts.TurnDone != nil {
		var ids []string
		for _, it := range items {
			ids = append(ids, it.SignalIDs...)
		}
		defer h.opts.TurnDone(ids, answered || err == nil)
	}
	if !answered {
		return
	}
	target := h.replyTarget(conv, items)
	if target == nil {
		slog.Warn("no conversation to reply to", "conv", conv.Key)
		return
	}
	if p := h.platforms[target.Platform]; p != nil {
		if err := p.Send(ctx, target.ChatID, reply); err != nil {
			slog.Error("send reply", "conv", target.Key, "err", err)
		}
	}
}

func leastPrivileged(items []Item) worker.Requester {
	for _, it := range items {
		if !it.From.Privileged() {
			return it.From
		}
	}
	return items[0].From
}

// ---------- conversations & history ----------

func (h *Hub) convLocked(key, platform, chatID string, isGroup bool) *Conversation {
	c := h.convs[key]
	if c == nil {
		c = &Conversation{Key: key, Platform: platform, ChatID: chatID, IsGroup: isGroup}
		h.convs[key] = c
	}
	if l := h.lanes[key]; l != nil {
		l.conv = c
	}
	return c
}

// History returns a copy of the conversation's text-only history.
func (h *Hub) History(c *Conversation) []Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Message(nil), c.History...)
}

// AppendHistory records one turn (user side + assistant side), trimmed.
func (h *Hub) AppendHistory(c *Conversation, user, assistant string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c.History = append(c.History, Message{"user", user}, Message{"assistant", assistant})
	if over := len(c.History) - h.opts.HistoryLimit; over > 0 {
		over += over % 2 // keep user/assistant pairs aligned
		c.History = c.History[over:]
	}
}

type savedState struct {
	Convs     map[string]*Conversation `json:"conversations"`
	OwnerChat map[string]string        `json:"owner_chat"`
}

func (h *Hub) statePath() string { return filepath.Join(h.opts.DataDir, "conversations.json") }

func (h *Hub) load() {
	b, err := os.ReadFile(h.statePath())
	if err != nil {
		return
	}
	var s savedState
	if json.Unmarshal(b, &s) == nil {
		if s.Convs != nil {
			h.convs = s.Convs
		}
		if s.OwnerChat != nil {
			h.ownerChat = s.OwnerChat
		}
	}
}

func (h *Hub) save() {
	h.mu.Lock()
	b, err := json.MarshalIndent(savedState{Convs: h.convs, OwnerChat: h.ownerChat}, "", "  ")
	h.mu.Unlock()
	if err != nil {
		return
	}
	tmp := h.statePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, h.statePath())
	}
}

func (h *Hub) dedup(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	if _, ok := h.seen[id]; ok {
		return true
	}
	h.seen[id] = now
	if len(h.seen) > 2000 {
		for k, t := range h.seen {
			if now.Sub(t) > 30*time.Minute {
				delete(h.seen, k)
			}
		}
	}
	return false
}

// ---------- slash commands (handled without the LLM) ----------

func (h *Hub) command(conv *Conversation, in Inbound, user identity.User, text string) bool {
	fields := strings.Fields(text)
	owner := user.Privileged()
	reply := ""
	switch fields[0] {
	case "/whoami":
		reply = fmt.Sprintf("名字：%s\nID：`%s`\n角色：%s", orDash(user.Name), user.ID, user.Role)
		if user.UnionID != "" {
			reply += fmt.Sprintf("\nunion_id：`%s`", user.UnionID)
		}
	case "/help":
		reply = "直接跟我说话就行。命令：/whoami 查看身份，/status 查看 worker 状态，/cancel <任务ID> 取消任务，/reset 清空本会话记忆（大脑会开新会话）。"
	case "/status":
		if !owner {
			reply = h.workers.Overview(h.scopeFor(user))
		} else {
			reply = h.workers.Overview(h.scopeFor(user))
			if ts := h.workers.ActiveTasksFor(conv.Key); len(ts) > 0 {
				reply += "\n本会话进行中的任务："
				for _, t := range ts {
					reply += fmt.Sprintf("\n- %s @%s %s：%s", t.ID, t.Worker, t.Status, clip(t.Instruction, 60))
				}
			}
		}
	case "/cancel":
		if !owner {
			reply = "只有 owner 可以取消任务。"
		} else if len(fields) < 2 {
			reply = "用法：/cancel <任务ID>"
		} else if t, err := h.workers.Cancel(fields[1]); err != nil {
			reply = "取消失败：" + err.Error()
		} else {
			reply = fmt.Sprintf("已取消 %s（@%s）", t.ID, t.Worker)
		}
	case "/reset":
		h.mu.Lock()
		conv.History = nil
		conv.BrainKind, conv.BrainSession = "", ""
		h.mu.Unlock()
		h.save()
		reply = "已清空本会话的对话记忆（worker 的会话不受影响）。"
	default:
		return false
	}
	var err error
	if p := h.platforms[in.Platform]; p != nil {
		err = p.Send(context.Background(), in.ChatID, reply)
	}
	h.opts.Audit.Record(audit.Event{Type: audit.Outbound, Platform: in.Platform, Conv: conv.Key, User: &user,
		Text: audit.Clip(reply, 2000), Error: errString(err), Extra: map[string]any{"command": fields[0]}})
	return true
}

func statusZh(s string) string {
	switch s {
	case worker.StatusSucceeded:
		return "已完成"
	case worker.StatusFailed:
		return "失败"
	case worker.StatusCancelled:
		return "已取消"
	}
	return s
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func orDash(s string) string {
	if s == "" {
		return "（未知：平台未授予读取姓名的权限）"
	}
	return s
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
