package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/app"
)

// ColdReader is a second reader with no context: a small model reads only
// the card, says back in its own words what is being decided and what each
// option leads to, and points at the exact sentences it couldn't follow and
// anything left for the reader to find out. The brain gets those back to
// fix — not a score.
type ColdReader struct {
	Model   string // default haiku
	Timeout time.Duration
}

var _ app.CardReader = ColdReader{}

type coldAnswer struct {
	Decision string `json:"decision"`
	Options  []struct {
		Key  string `json:"key"`
		Then string `json:"then"`
	} `json:"options"`
	Recommend string `json:"recommend"`
	Unclear   []struct {
		Quote    string `json:"quote"`
		Question string `json:"question"`
		Plain    string `json:"plain"`
		Blocking bool   `json:"blocking"`
	} `json:"unclear"`
	Homework  []string `json:"homework"`
	CanDecide bool     `json:"can_decide"`
}

const coldPrompt = `你是要拍板的人：聪明、了解自己的公司和团队，但不懂这件事所在的专业领域，也没跟进过细节，不能查资料或点链接，只有 30 秒。下面是别人发给你、请你拍板的一张卡。只根据卡上的字回答，输出一个 JSON 对象，不要别的文字：
{
  "decision": "用你自己的话，一句话说出要你决定的是什么",
  "options": [{"key": "A", "then": "用你自己的话说，选了它之后会发生什么"}],
  "recommend": "推荐的是哪个、为什么（用你自己的话）",
  "unclear": [{"quote": "卡上让你看不懂或要猜的原句", "question": "你具体不明白什么", "plain": "建议改成的大白话", "blocking": true 或 false（不弄懂这句，你会不敢决定或可能决定错，才算 true）}],
  "homework": ["卡上是否有把本该提问方自己去查、去测、去确认的事留给了你；没有就留空"],
  "can_decide": true 或 false（读完 30 秒内能否放心做决定）
}
只挑会影响你做决定的地方，最多 5 条，最要紧的放前面；一个普通人常见的词、一个说得清楚的数字，不用挑。不要客气，也不要吹毛求疵。

卡片：
`

func (c ColdReader) ReadCard(ctx context.Context, rendered string, unexplained []string) (app.CardReading, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "-p", coldPrompt+rendered, "--model", firstNonEmptyJ(c.Model, "haiku"), "--output-format", "text", "--tools", "", "--setting-sources", "")
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "ANTHROPIC_") && !strings.HasPrefix(e, "CLAUDECODE") {
			env = append(env, e)
		}
	}
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return app.CardReading{}, fmt.Errorf("cold reader: %w", err)
	}
	text := strings.TrimSpace(string(out))
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		text = text[i : j+1]
	}
	var a coldAnswer
	if err := json.Unmarshal([]byte(text), &a); err != nil {
		return app.CardReading{}, fmt.Errorf("cold reader: unreadable answer: %v", err)
	}
	blocking := 0
	for _, u := range a.Unclear {
		if u.Blocking {
			blocking++
		}
	}
	r := app.CardReading{Pass: a.CanDecide && blocking == 0 && len(nonEmpty(a.Homework)) == 0}
	r.Notes = append(r.Notes, "冷读者的理解——要决定的："+a.Decision)
	for _, o := range a.Options {
		r.Notes = append(r.Notes, fmt.Sprintf("  选 %s 会：%s", o.Key, o.Then))
	}
	if a.Recommend != "" {
		r.Notes = append(r.Notes, "  推荐："+a.Recommend)
	}
	shown := 0
	for _, u := range a.Unclear {
		p := fmt.Sprintf("看不懂「%s」：%s", u.Quote, u.Question)
		if u.Plain != "" {
			p += "（可以改成：" + u.Plain + "）"
		}
		switch {
		case u.Blocking && shown < 3:
			r.Problems = append(r.Problems, p)
			shown++
		case !u.Blocking:
			r.Notes = append(r.Notes, "可以更好："+p)
		}
	}
	for _, h := range nonEmpty(a.Homework) {
		r.Problems = append(r.Problems, "留给读者去做的事："+h)
	}
	if !a.CanDecide && len(r.Problems) == 0 {
		r.Problems = append(r.Problems, "读者读完还是不敢做决定：对照上面它的理解，看哪里和你想说的不一样")
	}
	return r, nil
}

func nonEmpty(v []string) []string {
	var out []string
	for _, x := range v {
		if strings.TrimSpace(x) != "" {
			out = append(out, x)
		}
	}
	return out
}
