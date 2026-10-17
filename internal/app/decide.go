package app

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/chenhg5/bot-connect/internal/domain/delegation"
	"github.com/chenhg5/bot-connect/internal/domain/planning"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// Asking the principal to decide: a decision card, checked by a cold
// reader before it goes out, so it can be decided in thirty seconds.

// CardReader reads a card knowing nothing else and says what can't be
// understood or decided from it.
type CardReader interface {
	ReadCard(ctx context.Context, rendered string, unexplained []string) (CardReading, error)
}

// CardReading is the cold reader's verdict.
type CardReading struct {
	Pass     bool
	Problems []string // what to fix, concretely
	Notes    []string // advice that doesn't block
	Scores   map[string]float64
}

// CardNotClear: the cold reader couldn't decide from the card.
type CardNotClear struct{ Reading CardReading }

func (e CardNotClear) Error() string {
	s := "决策卡还没讲清楚。一个不了解这件事的读者只读了这张卡：\n"
	if len(e.Reading.Notes) > 0 {
		s += strings.Join(e.Reading.Notes, "\n") + "\n"
	}
	return s + "它卡住的地方：\n- " + strings.Join(e.Reading.Problems, "\n- ") +
		"\n照着改：理解偏了的地方说得更直接，看不懂的句子换成它建议的大白话或你自己的。连续两次没过，可以用 send_anyway=true 发出。"
}

// MaxCardRewrites before a card may go out anyway.
const MaxCardRewrites = 2

type cardTries struct {
	mu sync.Mutex
	n  map[string]int
}

var tries = cardTries{n: map[string]int{}}

func (t *cardTries) fail(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.n[key]++
	return t.n[key]
}

func (t *cardTries) count(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.n[key]
}

// AskDecision puts a decision to the principal as a card. The card must be
// complete (one question, options with consequences, a recommendation,
// short enough to read in thirty seconds) and pass the cold reader, unless
// it already failed twice and anyway is set.
func (a *App) AskDecision(ctx context.Context, by Actor, pid ProjectID, item ItemID, c delegation.Card, anyway bool) (delegation.Assignment, CardReading, error) {
	if err := requirePrivileged(by, "ask the principal to decide"); err != nil {
		return delegation.Assignment{}, CardReading{}, err
	}
	if err := delegation.ValidateCard(c); err != nil {
		return delegation.Assignment{}, CardReading{}, err
	}
	rendered := c.Render()
	reading := CardReading{Pass: true}
	key := strings.TrimSpace(c.Ask)
	if a.CardReader != nil {
		r, err := a.CardReader.ReadCard(ctx, "（今天是 "+a.now().Format("2006-01-02 Mon 15:04")+"）\n"+rendered, delegation.Unexplained(rendered))
		if err == nil {
			reading = r
		} else if a.Log != nil {
			a.Log.Info("cold reader unavailable; sending the card unchecked", "err", err)
		}
	}
	if !reading.Pass {
		if !anyway || tries.count(key) < MaxCardRewrites {
			tries.fail(key)
			return delegation.Assignment{}, reading, CardNotClear{Reading: reading}
		}
	}
	if pid == "" {
		pid = OrgProject
	}
	if item == "" {
		it, err := a.PlanItem(ctx, by, planning.Spec{Project: pid, Title: "决定：" + oneLine(c.Ask, 60)})
		if err != nil {
			return delegation.Assignment{}, reading, err
		}
		item = it.ID
	}
	card := c
	as, err := a.Delegate(ctx, by, DelegateSpec{Item: item, Worker: Principal, Kind: delegation.Decision, Why: NeedDecision,
		Options: c.Keys(), Brief: delegation.Brief{Goal: c.Ask, Card: &card}})
	if err != nil {
		return as, reading, fmt.Errorf("could not put the decision: %w", err)
	}
	return as, reading, nil
}
