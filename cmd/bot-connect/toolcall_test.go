package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/chenhg5/bot-connect/internal/cli"
	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/tools"
	"github.com/chenhg5/bot-connect/internal/toolserver"
	"github.com/chenhg5/bot-connect/internal/worker"
)

type nopMsg struct{}

func (nopMsg) NotifyOwner(context.Context, string) error    { return nil }
func (nopMsg) SendTo(context.Context, string, string) error { return nil }

// The path a shell-agent brain uses: `bot-connect tool call` against a live
// tool server, with exit codes an agent can act on.
func TestToolCallAgainstToolServer(t *testing.T) {
	wm, err := worker.NewManager([]config.Worker{{Name: "proj", Agent: "claudecode", WorkDir: t.TempDir()}}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := toolserver.New(tools.New(tools.Env{Bot: "b", Workers: wm, Messenger: nopMsg{}}))
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	owner, _ := srv.Open(tools.TurnContext{ConvKey: "c", Caller: identity.User{ID: "me", Role: identity.RoleOwner}})
	visitor, _ := srv.Open(tools.TurnContext{ConvKey: "c", Caller: identity.User{ID: "v", Role: identity.RoleVisitor}})

	run := func(token string, args ...string) (int, string, string) {
		t.Setenv("BOT_CONNECT_API", srv.APIURL(token))
		var out, errb bytes.Buffer
		a := newApp()
		a.Stdout, a.Stderr = &out, &errb
		return a.Main(append([]string{"tool"}, args...)), out.String(), errb.String()
	}

	if code, out, _ := run(owner, "call", "--name", "list_workers"); code != cli.ExitOK || !strings.Contains(out, "proj") {
		t.Fatalf("list_workers: exit %d %s", code, out)
	}
	if code, out, _ := run(owner, "call", "--name", "agent_task", "--args", `{"worker":"proj","instruction":"x"}`, "--dry-run"); code != cli.ExitDryRun || !strings.Contains(out, `"allowed": true`) {
		t.Fatalf("dry-run delegate: exit %d %s", code, out)
	}
	if len(wm.Tasks()) != 0 {
		t.Fatal("dry-run must not create a task")
	}
	if code, _, errb := run(visitor, "call", "--name", "agent_task", "--args", `{"worker":"proj","instruction":"x"}`); code != cli.ExitNotFound && code != cli.ExitPermission {
		t.Fatalf("visitor delegate: exit %d %s", code, errb)
	}
	if code, _, _ := run(owner, "call", "--name", "nope"); code != cli.ExitNotFound {
		t.Fatalf("unknown tool: exit %d", code)
	}
	if code, _, _ := run(owner, "call", "--name", "task_status"); code != cli.ExitUsage {
		t.Fatalf("missing required arg: exit %d", code)
	}
	if code, _, _ := run(owner, "call", "--name", "list_workers", "--args", "not json"); code != cli.ExitUsage {
		t.Fatalf("bad --args: exit %d", code)
	}
}
