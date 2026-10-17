package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

func addDecide(r *Registry, env Env) {
	pm := env.App
	r.add(Tool{
		Name: "decide",
		Description: "Put a decision to the principal as a card they can decide in thirty seconds, without opening anything or knowing the jargon. " +
			"Before writing it, check everything an agent could check; the card holds only the choice that is theirs. Fields: ask (the one question), " +
			"situation (what is going on, in plain words, observable behaviour first), example (one concrete case: \"以前…现在…\"), options (2–4, each with then = what happens if chosen, " +
			"cost = its price), recommend (the option key) and why, verified (facts you checked), unverified (what isn't known — who is on it, whether it needs them), if_no_answer. " +
			"bot-connect checks it is complete and short, then a cold reader who knows nothing of the project reads only the card; what they couldn't follow comes back to you to fix. " +
			"Their answer (a letter) comes back as a signal; record it with respond.",
		PrincipalOnly: true,
		Schema: obj(props{
			"project":   str("project id or title"),
			"item":      str("the item this decision is about, if any"),
			"ask":       str("the one question, one sentence"),
			"situation": str("what is going on, plain words; what the person would see, not the internals"),
			"example":   str("one concrete case: 以前… → 现在…"),
			"options": map[string]any{"type": "array", "description": "2–4 choices", "items": obj(props{
				"key": str("A, B, C…"), "label": str("short name"), "then": str("what happens if chosen — observable"), "cost": str("its price or risk"),
			}, "key", "label", "then")},
			"recommend":    str("the key you recommend"),
			"why":          str("why, in terms of the user or the goal"),
			"verified":     arr("facts you already checked"),
			"unverified":   arr("what isn't known: who is on it, where it's stuck, whether it needs the principal"),
			"if_no_answer": str("what you'll do if they don't answer, and when you'll ask again"),
			"send_anyway":  boolean("only after the cold reader rejected this card twice"),
		}, "ask", "situation", "options", "recommend", "why"),
		Handler: func(ctx context.Context, tc TurnContext, a map[string]any) (string, error) {
			c := delegation.Card{Ask: s(a, "ask"), Situation: s(a, "situation"), Example: s(a, "example"), Recommend: s(a, "recommend"), Why: s(a, "why"),
				Verified: strs(a, "verified"), Unverified: strs(a, "unverified"), IfNoAnswer: s(a, "if_no_answer")}
			raw, _ := a["options"].([]any)
			for _, x := range raw {
				if m, ok := x.(map[string]any); ok {
					c.Options = append(c.Options, delegation.CardOption{Key: s(m, "key"), Label: s(m, "label"), Then: s(m, "then"), Cost: s(m, "cost")})
				}
			}
			var pid ProjectID
			if s(a, "project") != "" {
				pid = projectArg(pm, a)
			}
			as, reading, err := pm.AskDecision(ctx, actorOf(pm, tc), pid, ItemID(s(a, "item")), c, a["send_anyway"] == true)
			var nc app.CardNotClear
			if errors.As(err, &nc) {
				return "", &ToolError{KindInvalid, err.Error()}
			}
			if err != nil {
				return "", domainErr(err)
			}
			out := fmt.Sprintf("%s sent to the principal as a decision card; their answer comes back as a signal (record it with respond assignment=%s action=answer choice=<key>). "+
				"The card is the message: don't repeat it, summarise it or mention how it was checked — reply NO_REPLY unless there is something else they need to know.", as.ID, as.ID)
			if !reading.Pass {
				out += "\n冷读检查仍有问题（已注明发出）：" + strings.Join(reading.Problems, "；")
			}
			if len(reading.Notes) > 0 {
				out += "\n" + strings.Join(reading.Notes, "\n")
			}
			return out, nil
		},
	})
}
