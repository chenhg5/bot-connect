// Package audit records what the bot saw and did, as structured events:
// every inbound message (with the resolved sender), every brain turn, every
// tool call, every worker task. Sinks are pluggable; the default writes one
// JSONL file per day under <data_dir>/audit/.
package audit

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/chenhg5/bot-connect/internal/identity"
)

const (
	Inbound   = "inbound"    // a message arrived (incl. slash commands)
	TurnStart = "turn_start" // the brain started a turn
	TurnEnd   = "turn_end"   // the brain finished (reply or error)
	ToolCall  = "tool_call"  // the brain called a bot-connect tool
	Task      = "task"       // a worker task changed state
	Outbound  = "outbound"   // the bot sent a message outside a turn reply
	Schedule  = "schedule"   // a scheduled job fired / was skipped / disabled
)

type Event struct {
	Time      time.Time      `json:"time"`
	Type      string         `json:"type"`
	Bot       string         `json:"bot,omitempty"`
	Platform  string         `json:"platform,omitempty"`
	Conv      string         `json:"conv,omitempty"`
	ChatType  string         `json:"chat_type,omitempty"` // p2p | group
	MessageID string         `json:"message_id,omitempty"`
	User      *identity.User `json:"user,omitempty"` // sender / caller / requester
	Text      string         `json:"text,omitempty"`
	Tool      string         `json:"tool,omitempty"`
	Args      any            `json:"args,omitempty"`
	TaskID    string         `json:"task_id,omitempty"`
	Worker    string         `json:"worker,omitempty"`
	Status    string         `json:"status,omitempty"`
	Error     string         `json:"error,omitempty"`
	Duration  string         `json:"duration,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

type Sink interface {
	Record(Event)
}

// ForBot stamps every event with a bot name.
type ForBot struct {
	Bot  string
	Sink Sink
}

func (f ForBot) Record(e Event) {
	if e.Bot == "" {
		e.Bot = f.Bot
	}
	f.Sink.Record(e)
}

// Multi fans out to several sinks.
type Multi []Sink

func (m Multi) Record(e Event) {
	for _, s := range m {
		s.Record(e)
	}
}

// Nop discards events.
type Nop struct{}

func (Nop) Record(Event) {}

// JSONL writes <dir>/YYYY-MM-DD.jsonl.
type JSONL struct {
	dir  string
	mu   sync.Mutex
	day  string
	file *os.File
}

func NewJSONL(dir string) (*JSONL, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &JSONL{dir: dir}, nil
}

func (j *JSONL) Record(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	day := e.Time.Format("2006-01-02")
	if j.file == nil || day != j.day {
		if j.file != nil {
			j.file.Close()
		}
		f, err := os.OpenFile(filepath.Join(j.dir, day+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			slog.Error("audit: open", "err", err)
			j.file = nil
			return
		}
		j.file, j.day = f, day
	}
	_, _ = j.file.Write(append(b, '\n'))
}

func (j *JSONL) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file != nil {
		return j.file.Close()
	}
	return nil
}

// Clip shortens text stored in audit events.
func Clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
