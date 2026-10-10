// Package console is a stdin/stdout platform for local testing.
// Lines are sent as the owner; prefix a line with "@name " to speak as a
// visitor named name (e.g. "@jack 你好").
package console

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/bot-connect/internal/hub"
)

const OwnerID = "console-owner"

type Platform struct {
	mu  sync.Mutex
	seq int
}

func New() *Platform { return &Platform{} }

func (p *Platform) Name() string { return "console" }

func (p *Platform) Start(ctx context.Context, onMessage func(hub.Inbound)) error {
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			sender, chat, name := OwnerID, "owner", ""
			if strings.HasPrefix(line, "@") {
				if i := strings.IndexByte(line, ' '); i > 1 {
					name = line[1:i]
					sender, chat = "visitor-"+name, "visitor-"+name
					line = strings.TrimSpace(line[i+1:])
				}
			}
			p.mu.Lock()
			p.seq++
			id := fmt.Sprintf("c%d", p.seq)
			p.mu.Unlock()
			onMessage(hub.Inbound{Platform: "console", ChatID: chat, MessageID: id, SenderID: sender, Sender: name, Text: line})
		}
	}()
	return nil
}

// SendUser prints a direct message to a user.
func (p *Platform) SendUser(ctx context.Context, userID, text string) (string, error) {
	return fmt.Sprintf("console-%d", time.Now().UnixNano()), p.Send(ctx, "user:"+userID, text)
}

func (p *Platform) Send(ctx context.Context, chatID, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Printf("\n\033[36m[%s → %s]\033[0m\n%s\n\n", time.Now().Format("15:04:05"), chatID, text)
	return nil
}

func (p *Platform) Ack(ctx context.Context, messageID string) {}
