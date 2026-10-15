package probe

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/observe"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// DefaultSessions are how to hand a message to an existing session of
// common agent CLIs: {session}, {message} are filled in; the command runs
// in the action's directory. Configuration can add or replace any.
var DefaultSessions = map[string][]string{
	"claude": {"claude", "-p", "{message}", "--resume", "{session}", "--output-format", "text"},
	"codex":  {"codex", "exec", "resume", "{session}", "{message}"},
}

// Do carries out an action. Permission is the caller's business; this only
// keeps it inside the open roots and bounded in time and output.
func (l *Local) Do(ctx context.Context, a observe.Action) (string, error) {
	if err := a.Validate(); err != nil {
		return "", err
	}
	switch a.Kind {
	case observe.ActWrite:
		return l.write(a)
	case observe.ActCmd:
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return l.run(ctx, a.Program, a.Args, a.Dir)
	case observe.ActSession:
		tpl, ok := l.Sessions[a.Harness]
		if !ok {
			tpl, ok = DefaultSessions[a.Harness]
		}
		if !ok || len(tpl) == 0 {
			return "", NotFound("no way to reach a %q session (add [act.sessions.%s])", a.Harness, a.Harness)
		}
		argv := make([]string, len(tpl))
		for i, t := range tpl {
			argv[i] = strings.NewReplacer("{session}", a.Session, "{message}", a.Message).Replace(t)
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		return l.run(ctx, argv[0], argv[1:], a.Dir)
	}
	return "", Invalid("unknown action %q", a.Kind)
}

func (l *Local) write(a observe.Action) (string, error) {
	path, err := filepath.Abs(expand(a.Path))
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	// the directory may not exist yet: check the nearest existing ancestor
	probeDir := dir
	for {
		if _, err := os.Stat(probeDir); err == nil {
			break
		}
		parent := filepath.Dir(probeDir)
		if parent == probeDir {
			break
		}
		probeDir = parent
	}
	if _, err := l.Inside(probeDir); err != nil {
		return "", err
	}
	if st, err := os.Lstat(path); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return "", Forbidden("%s is a symlink; not writing through it", a.Path)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if _, err := l.Inside(dir); err != nil {
		return "", err
	}
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if a.Append {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	n, err := f.WriteString(a.Content)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", n, path), nil
}

func (l *Local) run(ctx context.Context, program string, args []string, dir string) (string, error) {
	if strings.ContainsAny(program, `/\`) {
		return "", Forbidden("give the program by name, not by path")
	}
	if dir == "" && len(l.Roots) > 0 {
		dir = l.Roots[0]
	}
	real, err := l.Inside(dir)
	if err != nil {
		return "", err
	}
	bin, err := exec.LookPath(program)
	if err != nil {
		return "", NotFound("%s is not installed", program)
	}
	c := exec.CommandContext(ctx, bin, args...)
	c.Dir = real
	c.Env = append(os.Environ(), "PAGER=cat", "GIT_PAGER=cat", "GH_PAGER=cat", "NO_COLOR=1", "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0", "TERM=dumb")
	var out strings.Builder
	c.Stdout, c.Stderr = &limited{w: &out, n: l.maxBytes() + 1}, &limited{w: &out, n: l.maxBytes() + 1}
	err = c.Run()
	text := l.clip(Redact(out.String()))
	if ctx.Err() != nil {
		return text, fmt.Errorf("timed out")
	}
	if err != nil {
		return text, fmt.Errorf("%s: %v", program, err)
	}
	return text, nil
}
