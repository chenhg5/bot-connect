package hub

import (
	"context"
	"github.com/chenhg5/bot-connect/internal/identity"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/bot-connect/internal/worker"
)

type fakePlatform struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakePlatform) Name() string                               { return "fake" }
func (f *fakePlatform) Start(context.Context, func(Inbound)) error { return nil }
func (f *fakePlatform) Ack(context.Context, string)                {}
func (f *fakePlatform) Send(_ context.Context, chat, t string) error {
	f.mu.Lock()
	f.sent = append(f.sent, chat+":"+t)
	f.mu.Unlock()
	return nil
}

type fakeBrain struct {
	mu      sync.Mutex
	order   []string // conv keys in the order turns started
	turns   []Turn
	active  int
	maxSeen int
	hold    time.Duration
}

func (b *fakeBrain) HandleTurn(ctx context.Context, t Turn) (string, error) {
	b.mu.Lock()
	b.order = append(b.order, t.Conv.ChatID)
	b.turns = append(b.turns, t)
	b.active++
	if b.active > b.maxSeen {
		b.maxSeen = b.active
	}
	b.mu.Unlock()
	time.Sleep(b.hold)
	b.mu.Lock()
	b.active--
	b.mu.Unlock()
	return "ok", nil
}

func newTestHub(t *testing.T, maxConc int, hold time.Duration) (*Hub, *fakeBrain) {
	wm, err := worker.NewManager(nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := New(Options{Identity: identity.NewResolver(identity.NewStaticPolicy([]string{"owner"}, nil)), MaxConcurrent: maxConc,
		Debounce: 50 * time.Millisecond, TurnTimeout: 5 * time.Second, HistoryLimit: 10, DataDir: t.TempDir()}, wm)
	b := &fakeBrain{hold: hold}
	h.SetBrain(b)
	h.AddPlatform(&fakePlatform{})
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return h, b
}

func msg(chat, sender, text string) Inbound {
	return Inbound{Platform: "fake", ChatID: chat, MessageID: chat + sender + text, SenderID: sender, Text: text}
}

func waitTurns(t *testing.T, b *fakeBrain, n int) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		got := len(b.turns)
		b.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d turns", n)
}

// Rapid messages in one chat coalesce into one turn; messages arriving during
// a running turn are batched into the next one.
func TestCoalescing(t *testing.T) {
	h, b := newTestHub(t, 2, 300*time.Millisecond)
	h.onInbound(msg("c1", "owner", "a"))
	h.onInbound(msg("c1", "owner", "b"))
	time.Sleep(150 * time.Millisecond) // first turn now running
	h.onInbound(msg("c1", "owner", "c"))
	h.onInbound(msg("c1", "owner", "d"))
	waitTurns(t, b, 2)
	time.Sleep(100 * time.Millisecond)
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.turns) != 2 || len(b.turns[0].Items) != 2 || len(b.turns[1].Items) != 2 {
		t.Fatalf("want 2 turns of 2 items, got %d turns: %+v", len(b.turns), b.turns)
	}
}

// With one slot, the owner jumps ahead of visitors who were waiting longer.
func TestOwnerPriority(t *testing.T) {
	h, b := newTestHub(t, 1, 200*time.Millisecond)
	h.onInbound(msg("busy", "v0", "occupy the slot"))
	time.Sleep(100 * time.Millisecond)
	h.onInbound(msg("v1", "v1", "hi"))
	h.onInbound(msg("v2", "v2", "hi"))
	time.Sleep(20 * time.Millisecond)
	h.onInbound(msg("own", "owner", "hi"))
	waitTurns(t, b, 4)
	b.mu.Lock()
	defer b.mu.Unlock()
	got := strings.Join(b.order, ",")
	if got != "busy,own,v1,v2" {
		t.Fatalf("order = %s", got)
	}
	if b.maxSeen != 1 {
		t.Fatalf("concurrency exceeded: %d", b.maxSeen)
	}
}

// A visitor message in the same batch downgrades the whole turn.
func TestLeastPrivilege(t *testing.T) {
	h, b := newTestHub(t, 1, 0)
	h.onInbound(Inbound{Platform: "fake", ChatID: "g", MessageID: "1", SenderID: "owner", Text: "x", IsGroup: true})
	h.onInbound(Inbound{Platform: "fake", ChatID: "g", MessageID: "2", SenderID: "stranger", Text: "y", IsGroup: true})
	waitTurns(t, b, 1)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.turns[0].Caller.Privileged() {
		t.Fatal("turn with a visitor item must not run as owner")
	}
}
