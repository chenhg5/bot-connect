package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chenhg5/bot-connect/internal/adapters/store/jsonstore"
	"github.com/chenhg5/bot-connect/internal/app"
	"github.com/chenhg5/bot-connect/internal/domain/delegation"
)

type fakeReader struct{ pass bool }

func (f fakeReader) ReadCard(context.Context, string, []string) (app.CardReading, error) {
	if f.pass {
		return app.CardReading{Pass: true}, nil
	}
	return app.CardReading{Problems: []string{"读者说不清要他决定的到底是什么"}}, nil
}

func venueCard() delegation.Card {
	return delegation.Card{Ask: "年会场地选 A 还是 B？", Situation: "两家都有档期，报价都在预算内。",
		Options:   []delegation.CardOption{{Key: "A", Label: "A 酒店", Then: "本周签约，一家对接"}, {Key: "B", Label: "B 园区", Then: "省钱，但要多找三家供应商"}},
		Recommend: "A", Why: "时间紧，少一个环节少一分延期的风险"}
}

func TestDecisionCards(t *testing.T) {
	r := newRig(t, jsonstore.Memory())
	a := r.app
	bad := venueCard()
	bad.Recommend = ""
	if _, _, err := a.AskDecision(ctx, principal, "P3", "", bad, false); err == nil {
		t.Fatal("an incomplete card is refused")
	}
	a.CardReader = fakeReader{}
	var nc app.CardNotClear
	for i := 0; i < 2; i++ {
		if _, _, err := a.AskDecision(ctx, principal, "P3", "", venueCard(), true); !errors.As(err, &nc) {
			t.Fatalf("round %d: the cold reader sends it back: %v", i, err)
		}
	}
	if _, reading, err := a.AskDecision(ctx, principal, "P3", "", venueCard(), true); err != nil || reading.Pass {
		t.Fatalf("after two rewrites it may go out anyway, marked: %v %+v", err, reading)
	}
	a.CardReader = fakeReader{pass: true}
	n := len(r.dms)
	as, _, err := a.AskDecision(ctx, principal, "P3", "", venueCard(), false)
	must(t, err)
	if len(r.dms) != n+1 || !strings.Contains(r.lastDM(), "**年会场地选 A 还是 B？**") || !strings.Contains(r.lastDM(), "选 A") {
		t.Fatalf("the principal gets the card: %v", r.lastDM())
	}
	if strings.Join(as.Options, ",") != "A,B" || as.Brief.Card == nil {
		t.Fatalf("answered by the option's letter: %+v", as.Options)
	}
}
