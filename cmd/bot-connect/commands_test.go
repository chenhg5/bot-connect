package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/bot-connect/internal/cli"
)

func walk(c *cli.Command, path []string, f func(*cli.Command, []string)) {
	f(c, path)
	for _, ch := range c.Children {
		walk(ch, append(append([]string(nil), path...), ch.Name), f)
	}
}

// Agent CLI guide checks for every command: a summary, examples on every
// runnable command, --help that works and stays under 50 lines, valid schema.
func TestEveryCommandFollowsTheGuide(t *testing.T) {
	app := newApp()
	walk(app.Root, nil, func(c *cli.Command, path []string) {
		name := strings.Join(append([]string{"bot-connect"}, path...), " ")
		if c.Summary == "" {
			t.Errorf("%s: no summary", name)
		}
		if c.Run != nil && len(c.Examples) == 0 {
			t.Errorf("%s: no examples", name)
		}
		var out, errb bytes.Buffer
		a := newApp()
		a.Stdout, a.Stderr = &out, &errb
		if code := a.Main(append(path, "--help")); code != cli.ExitOK {
			t.Errorf("%s --help: exit %d", name, code)
		}
		if n := strings.Count(out.String(), "\n"); n > 50 {
			t.Errorf("%s --help is %d lines (keep it under 50)", name, n)
		}
		s, err := app.Schema(path)
		if err != nil {
			t.Errorf("%s: schema: %v", name, err)
		}
		if _, err := json.Marshal(s); err != nil {
			t.Errorf("%s: schema not JSON: %v", name, err)
		}
	})
}

func TestToolCallWithoutTurnIsPermissionError(t *testing.T) {
	t.Setenv("BOT_CONNECT_API", "")
	var out, errb bytes.Buffer
	a := newApp()
	a.Stdout, a.Stderr = &out, &errb
	if code := a.Main([]string{"tool", "call", "--name", "list_workers"}); code != cli.ExitPermission {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout must stay empty on error: %q", out.String())
	}
}
