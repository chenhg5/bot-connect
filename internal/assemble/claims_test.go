package assemble

import (
	"testing"

	"github.com/chenhg5/bot-connect/internal/audit"
)

func TestClaimLogAndPrefilter(t *testing.T) {
	c := NewClaimLog()
	c.Record(audit.Event{Type: audit.ToolCall, Conv: "main", Tool: "delegate"})
	c.Record(audit.Event{Type: audit.Outbound, Conv: "main"})
	c.Record(audit.Event{Type: audit.ToolCall, Conv: "dm", Tool: "brief"})
	if n := len(c.Turn("main", "dm", "main")); n != 2 {
		t.Fatalf("this turn's calls in both conversations, once: %d", n)
	}
	c.Reset("main")
	if n := len(c.Turn("main")); n != 0 {
		t.Fatalf("a new turn starts empty: %d", n)
	}
	for text, want := range map[string]bool{"任务已停。": true, "已交给 dev，周五交付": true, "已经通知王五了": true,
		"我打算让 researcher 先调研": false, "在的，什么事？": false, "首页进度 60%": false} {
		if mayClaim(text) != want {
			t.Fatalf("mayClaim(%q) != %v", text, want)
		}
	}
}
