package assemble

import (
	"regexp"
	"sync"

	"github.com/chenhg5/bot-connect/internal/audit"
)

// ClaimLog keeps the tool calls of the current brain turn per conversation,
// so a reply that says something was done can be checked against what was
// actually done. Plug it into the tool server's audit sink.
type ClaimLog struct {
	mu    sync.Mutex
	calls map[string][]audit.Event
}

func NewClaimLog() *ClaimLog { return &ClaimLog{calls: map[string][]audit.Event{}} }

func (c *ClaimLog) Record(e audit.Event) {
	if e.Type != audit.ToolCall {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[e.Conv] = append(c.calls[e.Conv], e)
}

// Reset starts a new turn in a conversation.
func (c *ClaimLog) Reset(conv string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.calls, conv)
}

// Turn returns this turn's calls in the given conversations.
func (c *ClaimLog) Turn(convs ...string) []audit.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []audit.Event
	seen := map[string]bool{}
	for _, k := range convs {
		if !seen[k] {
			seen[k] = true
			out = append(out, c.calls[k]...)
		}
	}
	return out
}

// claimWords: a reply that may say something was done.
var claimWords = regexp.MustCompile(`已(经)?.{0,4}(派|交给|交由|通知|转达|告知|停|取消|撤|创建|建好|安排|接受|更新|改为|提交|发出|发给|启动|记下|登记|确认)`)

func mayClaim(text string) bool { return claimWords.MatchString(text) }
