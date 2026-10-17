package delegation

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// Card is a decision put to a person so they can decide in about thirty
// seconds without opening anything or knowing the jargon: one question,
// the situation in plain words with a concrete example, two to four
// options each with what happens if chosen, a recommendation and why, what
// was verified and what wasn't, and what happens if they don't answer.
//
// The person is the bottleneck. Everything an agent could check is checked
// before the card is written; what is left is the choice only they can make.
type Card struct {
	Ask        string       `json:"ask"`                  // the one question, in one sentence
	Situation  string       `json:"situation"`            // what is going on, plain words
	Example    string       `json:"example,omitempty"`    // one concrete case: before → after
	Options    []CardOption `json:"options"`              // 2–4
	Recommend  string       `json:"recommend"`            // the key of the recommended option
	Why        string       `json:"why"`                  // why that one
	Verified   []string     `json:"verified,omitempty"`   // facts already checked
	Unverified []string     `json:"unverified,omitempty"` // what isn't known, who is on it, whether it blocks
	IfNoAnswer string       `json:"if_no_answer,omitempty"`
}

// CardOption is one choice and what follows from it.
type CardOption struct {
	Key   string `json:"key"`            // A, B, C…
	Label string `json:"label"`          // short
	Then  string `json:"then"`           // what happens if chosen, observable
	Cost  string `json:"cost,omitempty"` // the price or risk of it
}

// Reading budgets: about thirty seconds for the core (ask, situation,
// example, options, recommendation); the rest is detail one may skip.
const (
	CardCoreLimit  = 380
	CardTotalLimit = 900
)

// Problems lists what makes the card not usable as is.
func (c Card) Problems() []string {
	var out []string
	ask := strings.TrimSpace(c.Ask)
	switch {
	case ask == "":
		out = append(out, "ask: 写出要对方决定的那一个问题")
	case strings.Count(ask, "？")+strings.Count(ask, "?") > 1:
		out = append(out, "ask: 只问一个问题（现在有多个问号）")
	case utf8.RuneCountInString(ask) > 60:
		out = append(out, "ask: 一句话说完，60 字以内")
	}
	if strings.TrimSpace(c.Situation) == "" {
		out = append(out, "situation: 用大白话说清现在是什么情况")
	}
	if n := len(c.Options); n < 2 || n > 4 {
		out = append(out, "options: 给 2–4 个选项")
	}
	keys := map[string]bool{}
	for i, o := range c.Options {
		if strings.TrimSpace(o.Key) == "" || strings.TrimSpace(o.Label) == "" {
			out = append(out, fmt.Sprintf("options[%d]: 需要 key 和 label", i))
		}
		if strings.TrimSpace(o.Then) == "" {
			out = append(out, fmt.Sprintf("option %s: 写清选了之后会发生什么（then）", o.Key))
		}
		keys[o.Key] = true
	}
	if !keys[c.Recommend] {
		out = append(out, "recommend: 推荐一个选项（填它的 key）")
	}
	if strings.TrimSpace(c.Why) == "" {
		out = append(out, "why: 说一句为什么推荐它")
	}
	if n := c.coreLen(); n > CardCoreLimit {
		out = append(out, fmt.Sprintf("太长：核心部分 %d 字，30 秒读不完（上限 %d）——只留决策需要的事实，细节放 verified / unverified", n, CardCoreLimit))
	}
	if n := utf8.RuneCountInString(c.Render()); n > CardTotalLimit {
		out = append(out, fmt.Sprintf("太长：整张卡 %d 字（上限 %d）", n, CardTotalLimit))
	}
	return out
}

func (c Card) coreLen() int {
	n := utf8.RuneCountInString(c.Ask + c.Situation + c.Example + c.Why)
	for _, o := range c.Options {
		n += utf8.RuneCountInString(o.Label + o.Then + o.Cost)
	}
	return n
}

// Keys are the option keys.
func (c Card) Keys() []string {
	var out []string
	for _, o := range c.Options {
		out = append(out, o.Key)
	}
	return out
}

// Render is the card as the person reads it: the question first, the core
// next, details last.
func (c Card) Render() string {
	var b strings.Builder
	b.WriteString("**" + strings.TrimSpace(c.Ask) + "**\n\n")
	b.WriteString(strings.TrimSpace(c.Situation) + "\n")
	if c.Example != "" {
		b.WriteString("\n例子：" + strings.TrimSpace(c.Example) + "\n")
	}
	b.WriteString("\n")
	for _, o := range c.Options {
		mark := ""
		if o.Key == c.Recommend {
			mark = " 👈 推荐"
		}
		fmt.Fprintf(&b, "**%s. %s**%s\n选了会：%s\n", o.Key, o.Label, mark, strings.TrimSpace(o.Then))
		if o.Cost != "" {
			b.WriteString("代价：" + strings.TrimSpace(o.Cost) + "\n")
		}
	}
	b.WriteString("\n推荐 " + c.Recommend + "：" + strings.TrimSpace(c.Why) + "\n")
	if len(c.Verified) > 0 || len(c.Unverified) > 0 || c.IfNoAnswer != "" {
		b.WriteString("\n——细节（可跳过）——\n")
		for _, v := range c.Verified {
			b.WriteString("✓ 已确认：" + v + "\n")
		}
		for _, v := range c.Unverified {
			b.WriteString("？还不确定：" + v + "\n")
		}
		if c.IfNoAnswer != "" {
			b.WriteString("不回复的话：" + c.IfNoAnswer + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Vague phrases stand in for a fact the reader has to guess at. They are
// about language, not any business: whether the reader understands the
// card's terms is the cold reader's call, not a word list's.
var Vague = []string{"可能会", "看一下", "大概", "应该没问题", "需要确认", "待确认", "后续再看", "有待", "也许"}

var explained = regexp.MustCompile(`^\s*[（(]`)

// Unexplained returns vague phrases, and any extra terms given (a
// project's own words, from data), used without an explanation right after.
func Unexplained(text string, extra ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range append(append([]string(nil), Vague...), extra...) {
		if w == "" || seen[w] {
			continue
		}
		rest := text
		for {
			i := strings.Index(rest, w)
			if i < 0 {
				break
			}
			after := rest[i+len(w):]
			if !explained.MatchString(after) {
				out = append(out, w)
				seen[w] = true
				break
			}
			rest = after
		}
	}
	return out
}

// ValidateCard is the hard check before a card is sent.
func ValidateCard(c Card) error {
	if p := c.Problems(); len(p) > 0 {
		return Invalid("决策卡还不能发：\n- %s", strings.Join(p, "\n- "))
	}
	return nil
}
