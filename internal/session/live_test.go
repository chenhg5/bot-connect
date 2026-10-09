package session

import (
	"os"
	"testing"
	"time"
)

// Smoke test against the real local stores; opt-in.
func TestLiveList(t *testing.T) {
	if os.Getenv("BOT_CONNECT_LIVE") == "" {
		t.Skip("set BOT_CONNECT_LIVE=1")
	}
	start := time.Now()
	l, _ := Default().List("", 8, nil)
	for _, s := range l {
		t.Logf("%-10s %s %s | %s | %s", s.Agent, s.ID[:8], s.Updated.Format("01-02 15:04"), s.Dir, s.Title)
	}
	t.Logf("listed %d in %s", len(l), time.Since(start))
	if len(l) > 0 {
		h, err := Default().History(l[0].Agent, l[0].ID, 3)
		t.Logf("history err=%v n=%d", err, len(h))
		for _, e := range h {
			t.Logf("  [%s] %s", e.Role, clip(e.Text, 100))
		}
	}
}
