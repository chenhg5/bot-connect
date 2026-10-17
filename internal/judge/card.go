package judge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/app"
)

// CardReader is the cold reader: someone who knows nothing about the
// project reads only the card and tries to decide. What they can't answer
// goes back to the brain as concrete problems.
type CardReader struct{ J Judge }

var _ app.CardReader = CardReader{}

var cardQuestions = []struct {
	id, q, problem string
	invert         bool // a yes is the problem
}{
	{"what", "只读这张卡（`card`），一个完全不了解这个项目的人，能不能用一句话说出要他决定的是哪一件事？",
		"读者说不清要他决定的到底是什么：把 ask 改成一个具体的、只有一个答案的问题", false},
	{"then", "只读这张卡，读者能不能说出每个选项选了之后会发生什么（用户或系统能看到的结果），不用猜？",
		"有选项的后果要靠猜：每个 then 写成选了之后能看到的具体结果", false},
	{"plain", "一个不懂技术、没听说过这个项目的人，不查任何资料、不点链接，能看懂这张卡并做出决定吗？" +
		"出现了没解释的技术词、内部简称或状态名（见 `unexplained_terms`）就算看不懂。",
		"有看不懂的词或说法：把 unexplained_terms 里的词换成大白话，或紧跟着用括号解释；先说用户能看到的变化，再说技术名词", false},
	{"why", "推荐的选项是否明确，理由是否让人一读就信（说的是对用户或目标的影响，而不是“更规范”“更好”这类空话）？",
		"推荐的理由太空：说清选它对用户或目标具体好在哪", false},
	{"homework", "这张卡有没有把本该由提问方自己去查、去测、去确认的事留给读者（比如让读者去看链接、去确认、去测试、去问别人）？",
		"把该自己做的调查留给了读者：能查的先查完，卡上只留读者独有的取舍", true},
	{"unknowns", "如果卡上有还不确定的事（`card` 里的“还不确定”），是否说清了是谁在处理、卡在哪一步、读者需不需要动手？没有不确定的事就回答是。",
		"不确定的部分没说清谁在处理、要不要读者动手", false},
}

func (c CardReader) ReadCard(ctx context.Context, rendered string, unexplained []string) (app.CardReading, error) {
	if c.J == nil {
		return app.CardReading{Pass: true}, nil
	}
	qs := map[string]Question{}
	for _, x := range cardQuestions {
		qs[x.id] = Noul(x.q)
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ans, err := c.J.Ask(ctx, map[string]any{"card": rendered, "unexplained_terms": unexplained}, qs)
	if err != nil {
		return app.CardReading{}, err
	}
	r := app.CardReading{Pass: true, Scores: map[string]float64{}}
	for _, x := range cardQuestions {
		p := ans[x.id].Noul
		r.Scores[x.id] = p
		bad := p < 0.7
		if x.invert {
			bad = p > 0.3
		}
		if bad {
			r.Pass = false
			r.Problems = append(r.Problems, fmt.Sprintf("%s（%.2f）", x.problem, p))
		}
	}
	if len(unexplained) > 0 && r.Pass {
		r.Notes = append(r.Notes, "可以再看看这些词要不要换成大白话："+strings.Join(unexplained, "、"))
	}
	return r, nil
}
