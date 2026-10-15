package observe

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// Acting on work the bot doesn't run: running a command, writing a file,
// handing a message to an existing agent session. Like looking, nothing
// here knows a particular system. Every action needs the principal — once,
// for this exact action, or as a standing permission for a pattern.

type ActKind string

const (
	ActCmd     ActKind = "cmd"     // run a program with arguments (no shell)
	ActWrite   ActKind = "write"   // write a file (append or replace)
	ActSession ActKind = "session" // hand a message to an existing agent session
)

// Action is one thing the bot does outside itself.
type Action struct {
	Kind    ActKind  `json:"kind"`
	Program string   `json:"program,omitempty"` // cmd
	Args    []string `json:"args,omitempty"`    // cmd
	Dir     string   `json:"dir,omitempty"`     // cmd / session working directory
	Path    string   `json:"path,omitempty"`    // write
	Content string   `json:"content,omitempty"` // write
	Append  bool     `json:"append,omitempty"`  // write: add to the end instead of replacing
	Harness string   `json:"harness,omitempty"` // session: which agent CLI (claude, codex, …)
	Session string   `json:"session,omitempty"` // session: its id
	Message string   `json:"message,omitempty"` // session: what to tell it
}

// ContentLimit bounds what one write may put in a file.
const ContentLimit = 64 << 10

func (a Action) Validate() error {
	switch a.Kind {
	case ActCmd:
		if a.Program == "" || strings.ContainsAny(a.Program, `/\`) {
			return Invalid("a command needs its program by name (no path)")
		}
	case ActWrite:
		if a.Path == "" {
			return Invalid("a write needs a path")
		}
		if len(a.Content) > ContentLimit {
			return Invalid("content is %d bytes; at most %d", len(a.Content), ContentLimit)
		}
	case ActSession:
		if a.Harness == "" || a.Session == "" || strings.TrimSpace(a.Message) == "" {
			return Invalid("handing to a session needs harness, session and message")
		}
	default:
		return Invalid("unknown action %q (cmd, write, session)", a.Kind)
	}
	return nil
}

// Describe is the action in one line, as the principal approves it.
func (a Action) Describe() string {
	switch a.Kind {
	case ActCmd:
		s := "运行 " + strings.TrimSpace(a.Program+" "+strings.Join(a.Args, " "))
		if a.Dir != "" {
			s += "（在 " + a.Dir + "）"
		}
		return s
	case ActWrite:
		verb := "改写"
		if a.Append {
			verb = "追加到"
		}
		return fmt.Sprintf("%s %s（%d 字）", verb, a.Path, len([]rune(a.Content)))
	case ActSession:
		return fmt.Sprintf("给 %s 会话 %s 递话：%s", a.Harness, a.Session, clipRunes(a.Message, 80))
	}
	return string(a.Kind)
}

// Key is what standing permissions match against: "cmd <program> <args…>",
// "write <path>", "session <harness> <session>".
func (a Action) Key() []string {
	switch a.Kind {
	case ActCmd:
		return append([]string{"cmd", a.Program}, a.Args...)
	case ActWrite:
		return []string{"write", filepath.Clean(a.Path)}
	case ActSession:
		return []string{"session", a.Harness, a.Session}
	}
	return nil
}

// Permits reports whether a standing permission pattern covers an action.
// Patterns: "cmd hp task update *" ("*" = one non-flag argument, extra
// arguments allowed), "write ~/code/projects/*/handoffs/**" (path glob;
// "**" = anything below), "session codex *".
func Permits(pattern string, a Action, home string) bool {
	f := strings.Fields(pattern)
	key := a.Key()
	if len(f) < 2 || len(key) < 2 || f[0] != key[0] {
		return false
	}
	if f[0] == "write" {
		return pathMatch(expandHome(strings.Join(f[1:], " "), home), key[1])
	}
	if len(key) < len(f) {
		return false
	}
	for i, t := range f {
		if t == "*" {
			if strings.HasPrefix(key[i], "-") {
				return false
			}
			continue
		}
		if t != key[i] {
			return false
		}
	}
	return true
}

func expandHome(p, home string) string {
	if strings.HasPrefix(p, "~/") && home != "" {
		return filepath.Join(home, p[2:])
	}
	return p
}

// pathMatch: glob per segment; a trailing "/**" matches anything below.
func pathMatch(pattern, path string) bool {
	pattern, path = filepath.Clean(pattern), filepath.Clean(path)
	if strings.HasSuffix(pattern, string(filepath.Separator)+"**") {
		base := strings.TrimSuffix(pattern, string(filepath.Separator)+"**")
		pp, ps := strings.Split(base, string(filepath.Separator)), strings.Split(path, string(filepath.Separator))
		if len(ps) <= len(pp) {
			return false
		}
		for i := range pp {
			if ok, _ := filepath.Match(pp[i], ps[i]); !ok {
				return false
			}
		}
		return true
	}
	ok, _ := filepath.Match(pattern, path)
	return ok
}

// ValidPermission checks a standing permission pattern is narrow enough.
func ValidPermission(pattern string) error {
	f := strings.Fields(pattern)
	if len(f) < 2 {
		return Invalid("a permission is \"cmd <program> <subcommand…>\", \"write <path glob>\" or \"session <harness> <session|*>\"")
	}
	switch f[0] {
	case "cmd":
		if len(f) < 3 || f[2] == "*" {
			return Invalid("name the program and at least its subcommand (\"cmd hp task update *\"); a bare program would allow everything it can do")
		}
	case "write":
		p := strings.Join(f[1:], " ")
		if p == "/**" || p == "~/**" || strings.Count(p, "/") < 2 {
			return Invalid("a write permission must be narrow (a project's folder, not a whole disk)")
		}
	case "session":
		if len(f) < 3 {
			return Invalid("\"session <harness> <session id or *>\"")
		}
	default:
		return Invalid("unknown kind %q (cmd, write, session)", f[0])
	}
	return nil
}

// Done is one action carried out (the audit trail).
type Done struct {
	At      time.Time `json:"at"`
	Project ProjectID `json:"project,omitempty"`
	Action  Action    `json:"action"`
	By      Actor     `json:"by"`
	Grant   string    `json:"grant"` // "once" or the permission pattern
	Result  string    `json:"result,omitempty"`
	Err     string    `json:"err,omitempty"`
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
