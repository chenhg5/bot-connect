package toolserver

import (
	"context"
	"encoding/json"
	"github.com/chenhg5/bot-connect/internal/identity"
	"net/http"
	"strings"
	"testing"

	"github.com/chenhg5/bot-connect/internal/tools"
	"github.com/chenhg5/bot-connect/internal/worker"
)

type nopMsg struct{}

func (nopMsg) NotifyOwner(context.Context, string) error    { return nil }
func (nopMsg) SendTo(context.Context, string, string) error { return nil }

func rpc(t *testing.T, url, body string) map[string]any {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func TestMCPPrivilege(t *testing.T) {
	wm, _ := worker.NewManager(nil, nil, t.TempDir())
	s := New(tools.New(tools.Env{Bot: "b", Workers: wm, Messenger: nopMsg{}}))
	if err := s.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	visitor, closeV := s.Open(tools.TurnContext{ConvKey: "c", Caller: worker.Requester{ID: "jack", Name: "jack", Role: identity.RoleVisitor}})
	owner, _ := s.Open(tools.TurnContext{ConvKey: "c", Caller: worker.Requester{ID: "me", Role: identity.RoleOwner}})

	init := rpc(t, s.MCPURL(visitor), `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if init["result"] == nil {
		t.Fatalf("initialize: %v", init)
	}
	count := func(tok string) int {
		r := rpc(t, s.MCPURL(tok), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		return len(r["result"].(map[string]any)["tools"].([]any))
	}
	if v, o := count(visitor), count(owner); v >= o {
		t.Fatalf("visitor sees %d tools, owner %d", v, o)
	}
	call := rpc(t, s.MCPURL(visitor), `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"delegate","arguments":{"worker":"x","instruction":"rm -rf"}}}`)
	if call["result"].(map[string]any)["isError"] != true {
		t.Fatalf("visitor delegate should be denied: %v", call)
	}
	closeV()
	resp, _ := http.Post(s.MCPURL(visitor), "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired token should be rejected, got %d", resp.StatusCode)
	}
}
