package pm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/reach"
	"github.com/chenhg5/bot-connect/internal/workforce"
)

// OwnerID is the worker id of the bot's owner as a person.
const OwnerID = "owner"

// person is a human worker (from [[people]], or the owner).
type person struct {
	svc *Service
	bot string
	cfg config.Person
}

var _ workforce.Worker = (*person)(nil)

func (p *person) loc() *time.Location {
	if p.cfg.Timezone != "" {
		if l, err := time.LoadLocation(p.cfg.Timezone); err == nil {
			return l
		}
	}
	return time.Local
}

func (p *person) hours() reach.Hours {
	return reach.Hours{Location: p.loc(), WorkHours: p.cfg.WorkHours, QuietHours: p.cfg.QuietHours}
}

func (p *person) Profile() workforce.Profile {
	acc := p.cfg.AcceptFrom
	if len(acc) == 0 {
		acc = []string{"owner"}
	}
	principal := "self"
	if p.cfg.ID == OwnerID {
		principal = "owner"
	}
	return workforce.Profile{ID: p.cfg.ID, Name: p.cfg.Name, Kind: workforce.KindHuman, Description: p.cfg.Description,
		Skills: p.cfg.Skills, Principal: principal, Trust: workforce.External,
		Interaction: workforce.Interaction{Sessions: true, CanAsk: true, CanRefuse: p.cfg.ID != OwnerID, CanCounter: true},
		Contact:     p.cfg.Contact,
		Norms: workforce.Norms{Timezone: p.cfg.Timezone, WorkHours: p.cfg.WorkHours, QuietHours: p.cfg.QuietHours,
			MaxNudgesPerDay: p.maxNudges(), MinNudgeInterval: p.minInterval(), AcceptFrom: acc},
		Cost: workforce.Cost{Unit: "hours", Social: 2}}
}

func (p *person) maxNudges() int {
	if p.cfg.MaxNudgesPerDay > 0 {
		return p.cfg.MaxNudgesPerDay
	}
	return 2
}

func (p *person) minInterval() time.Duration {
	if p.cfg.MinNudgeInterval.Duration > 0 {
		return p.cfg.MinNudgeInterval.Duration
	}
	return 3 * time.Hour
}

// Observe: availability from work / quiet hours, load from open assignments,
// receptiveness from recent declines.
func (p *person) Observe(ctx context.Context) []workforce.Fact {
	now := p.svc.now()
	h := p.hours()
	presence := "available"
	switch {
	case reach.In(h.QuietHours, now, h.Location):
		presence = "dnd"
	case len(h.WorkHours) > 0 && !reach.In(h.WorkHours, now, h.Location):
		presence = "off_hours"
	}
	open, declined := p.svc.loadOf(p.bot, p.cfg.ID, now.Add(-7*24*time.Hour))
	facts := []workforce.Fact{
		{Dim: workforce.Presence, Value: presence, Source: workforce.Observed, At: now},
		{Dim: workforce.Load, Value: fmt.Sprintf("%d open", open), Num: float64(open), Source: workforce.Observed, At: now},
	}
	consent := "no"
	if p.cfg.Consent || p.cfg.ID == OwnerID {
		consent = "yes"
	}
	facts = append(facts, workforce.Fact{Dim: workforce.Consent, Value: consent, Source: workforce.Declared, At: now})
	if declined > 0 {
		facts = append(facts, workforce.Fact{Dim: workforce.Receptiveness, Value: "low", Detail: fmt.Sprintf("declined %d in 7 days", declined),
			Source: workforce.Observed, At: now, Expires: now.Add(24 * time.Hour)})
	}
	return facts
}

// Offer sends the brief as a request the person can accept, decline or counter.
func (p *person) Offer(ctx context.Context, a workforce.Assignment) error {
	who := p.svc.botName(p.bot)
	text := fmt.Sprintf("📋 **%s 的请求**（%s，来自 %s）\n\n%s\n\n直接回复我即可：**接下** / **不接**（说下原因）/ **改时间**（给个时间）。有问题也可以直接问。",
		a.ID, who, a.Requester.Display(), a.Brief.Instruction())
	if p.cfg.ID == OwnerID {
		text = fmt.Sprintf("📋 **给你的待办 %s**\n\n%s\n\n做完了告诉我（附上结果）；做不了或要改时间也直接说。", a.ID, a.Brief.Instruction())
	}
	_, err := p.svc.reachPerson(ctx, p.bot, p, reach.Message{Title: a.ID, Text: text, Ref: a.ID, Urgency: urgencyOf(a)})
	return err
}

func urgencyOf(a workforce.Assignment) reach.Urgency {
	if a.Brief.Priority >= 2 {
		return reach.Urgent
	}
	return reach.Normal
}

// Notify relays a requester-side event to the person.
func (p *person) Notify(ctx context.Context, a workforce.Assignment, ev workforce.Event) error {
	var text string
	u := reach.Normal
	switch ev.Type {
	case workforce.EvAnswer:
		text = fmt.Sprintf("关于 %s，你的问题有回复了：\n%s", a.ID, ev.Note)
	case workforce.EvVerify:
		text = fmt.Sprintf("✅ %s 已验收，谢谢！%s", a.ID, ev.Note)
	case workforce.EvRevise:
		text = fmt.Sprintf("关于 %s，需要再改一下：\n%s", a.ID, ev.Note)
	case workforce.EvCancel:
		text = fmt.Sprintf("%s 不用做了，已取消。%s", a.ID, ev.Note)
	case workforce.EvRedate:
		text = fmt.Sprintf("%s 的截止时间改为 %s。%s", a.ID, ev.Due.In(p.loc()).Format("01-02 15:04"), ev.Note)
	case workforce.EvAccept: // requester accepted the person's counter-proposal
		text = fmt.Sprintf("%s 按你提的时间来，截止 %s。", a.ID, dueText(a.Brief.Due, p.loc()))
	case workforce.EvNudge:
		u = reach.Reminder
		if a.Brief.Due != nil && p.svc.now().After(*a.Brief.Due) {
			u = reach.Urgent
		}
		text = fmt.Sprintf("⏰ 提醒 %s：%s\n%s", a.ID, ev.Note, oneLine(a.Brief.Goal, 120))
	default:
		return nil
	}
	_, err := p.svc.reachPerson(ctx, p.bot, p, reach.Message{Title: a.ID, Text: text, Ref: a.ID, Urgency: u})
	return err
}

func dueText(t *time.Time, loc *time.Location) string {
	if t == nil {
		return "未定"
	}
	return t.In(loc).Format("01-02 15:04")
}

// matches reports whether a resolved user is this person.
func (p *person) matches(u identity.User) bool {
	if p.cfg.ID == OwnerID {
		return u.Role == identity.RoleOwner
	}
	for _, id := range p.cfg.Identities {
		if _, rest, ok := strings.Cut(id, ":"); ok { // "feishu:ou_…", "email:a@b.com"
			id = rest
		}
		if id != "" && (id == u.ID || id == u.UnionID || strings.EqualFold(id, u.Email)) {
			return true
		}
	}
	return false
}

// workerFor returns the protocol adapter for a worker id, as seen by a bot.
func (s *Service) workerFor(bot, id string) (workforce.Worker, error) {
	if p := s.personFor(bot, id); p != nil {
		return p, nil
	}
	if s.agents != nil && s.agents.DirOf(id) != "" {
		return s.agentAdapter(bot, id), nil
	}
	return nil, fmt.Errorf("no worker or person named %q", id)
}

func (s *Service) personFor(bot, id string) *person {
	if id == OwnerID || id == "me" {
		return &person{svc: s, bot: bot, cfg: config.Person{ID: OwnerID, Name: s.ownerName(bot) + "（主人）", Consent: true,
			Contact: []reach.Route{{Channel: reach.OwnerChat, Address: OwnerID}}}}
	}
	for _, p := range s.people {
		if p.ID == id && allowedBot(p.Bots, bot) {
			return &person{svc: s, bot: bot, cfg: p}
		}
	}
	return nil
}

func allowedBot(bots []string, bot string) bool {
	if len(bots) == 0 {
		return true
	}
	for _, b := range bots {
		if b == bot {
			return true
		}
	}
	return false
}

// PersonOf returns the person id a user is (for replies), or "".
func (s *Service) PersonOf(bot string, u identity.User) string {
	for _, p := range s.people {
		if allowedBot(p.Bots, bot) && (&person{cfg: p}).matches(u) {
			return p.ID
		}
	}
	if u.Role == identity.RoleOwner {
		return OwnerID
	}
	return ""
}

func oneLine(x string, n int) string {
	x = strings.Join(strings.Fields(x), " ")
	r := []rune(x)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return x
}
