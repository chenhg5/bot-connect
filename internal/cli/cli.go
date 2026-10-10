// Package cli is a small command framework built for agents as much as for
// people (see https://github.com/Johnixr/agent-cli-guide):
//
//   - noun-verb command tree; help, flag validation and `schema` output all
//     come from the same definitions
//   - GNU long flags only (--name value | --name=value | --flag | --no-flag)
//   - --format json|table: JSON when stdout is not a terminal; data on
//     stdout, messages on stderr
//   - semantic exit codes and structured errors with a suggestion
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Exit codes. Stable: changing their meaning is a breaking change.
const (
	ExitOK         = 0
	ExitError      = 1  // general error — read the message
	ExitUsage      = 2  // invalid command, flag or value — fix the call
	ExitNotFound   = 3  // the named resource doesn't exist
	ExitPermission = 4  // not allowed
	ExitConflict   = 5  // already exists
	ExitDryRun     = 10 // --dry-run: checks passed, nothing was changed
)

// Error is a structured, actionable error.
type Error struct {
	Code       int    `json:"-"`
	Type       string `json:"error"` // usage | not_found | permission_denied | conflict | invalid_config | unavailable | failed
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
	Retryable  bool   `json:"retryable"`
	Input      string `json:"input,omitempty"` // what the caller sent, when relevant
}

func (e *Error) Error() string { return e.Message }

func Usage(msg, suggestion string) *Error {
	return &Error{Code: ExitUsage, Type: "usage", Message: msg, Suggestion: suggestion}
}
func NotFound(msg, suggestion string) *Error {
	return &Error{Code: ExitNotFound, Type: "not_found", Message: msg, Suggestion: suggestion}
}
func Denied(msg, suggestion string) *Error {
	return &Error{Code: ExitPermission, Type: "permission_denied", Message: msg, Suggestion: suggestion}
}
func Conflict(msg, suggestion string) *Error {
	return &Error{Code: ExitConflict, Type: "conflict", Message: msg, Suggestion: suggestion}
}
func Failed(msg, suggestion string, retryable bool) *Error {
	return &Error{Code: ExitError, Type: "failed", Message: msg, Suggestion: suggestion, Retryable: retryable}
}

// errDryRun is returned by DryRun to exit with ExitDryRun after printing.
var errDryRun = errors.New("dry run")

type FlagType string

const (
	String   FlagType = "string"
	Bool     FlagType = "bool"
	Int      FlagType = "int"
	Duration FlagType = "duration"
	Enum     FlagType = "enum"
	List     FlagType = "list" // repeatable string
)

type Flag struct {
	Name     string   `json:"name"` // without dashes
	Type     FlagType `json:"type"`
	Enum     []string `json:"enum,omitempty"`
	Default  string   `json:"default,omitempty"`
	Required bool     `json:"required"`
	Desc     string   `json:"description"`
	Env      string   `json:"env,omitempty"` // environment variable fallback
}

type Command struct {
	Name     string           `json:"name"`
	Summary  string           `json:"summary"`
	Desc     string           `json:"description,omitempty"`
	Examples []string         `json:"examples,omitempty"`
	Flags    []Flag           `json:"flags,omitempty"`
	Effects  bool             `json:"side_effects"` // creates/changes something; supports --dry-run
	Children []*Command       `json:"commands,omitempty"`
	Run      func(*Ctx) error `json:"-"`

	parent *Command
}

// App is the root of a command tree.
type App struct {
	Root   *Command
	Global []Flag // available on every command
	Stdout io.Writer
	Stderr io.Writer
	IsTTY  bool
	Getenv func(string) string
}

// Ctx is what a command's Run receives.
type Ctx struct {
	App    *App
	Cmd    *Command
	values map[string]any
	Format string // json | table | ndjson
}

func (c *Ctx) Str(name string) string        { v, _ := c.values[name].(string); return v }
func (c *Ctx) Bool(name string) bool         { v, _ := c.values[name].(bool); return v }
func (c *Ctx) Int(name string) int           { v, _ := c.values[name].(int); return v }
func (c *Ctx) Dur(name string) time.Duration { v, _ := c.values[name].(time.Duration); return v }
func (c *Ctx) List(name string) []string     { v, _ := c.values[name].([]string); return v }
func (c *Ctx) Set(name string) bool          { _, ok := c.values["set:"+name]; return ok }

// Out prints v as JSON (json/ndjson) or calls table for humans.
func (c *Ctx) Out(v any, table func(w io.Writer)) error {
	if c.Format == "table" && table != nil {
		table(c.App.Stdout)
		return nil
	}
	enc := json.NewEncoder(c.App.Stdout)
	enc.SetEscapeHTML(false)
	if c.Format == "ndjson" {
		if rows, ok := v.([]any); ok {
			for _, r := range rows {
				if err := enc.Encode(r); err != nil {
					return err
				}
			}
			return nil
		}
		return enc.Encode(v)
	}
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// Info writes a human message to stderr (never stdout).
func (c *Ctx) Info(format string, a ...any) { fmt.Fprintf(c.App.Stderr, format+"\n", a...) }

// DryRun prints the planned changes and makes the command exit with ExitDryRun.
func (c *Ctx) DryRun(plan any) error {
	if err := c.Out(map[string]any{"dry_run": true, "plan": plan}, nil); err != nil {
		return err
	}
	return errDryRun
}

// Main runs the app and returns the process exit code.
func (a *App) Main(args []string) int {
	link(a.Root, nil)
	cmd, rest := a.resolve(args)
	format := a.defaultFormat()

	// help: -h | --help | help <cmd…>
	if len(rest) > 0 && rest[0] == "help" && cmd == a.Root {
		cmd, _ = a.resolve(rest[1:])
		a.printHelp(cmd)
		return ExitOK
	}
	for _, r := range rest {
		if r == "-h" || r == "--help" {
			a.printHelp(cmd)
			return ExitOK
		}
	}
	if cmd.Run == nil {
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			return a.fail(format, Usage(fmt.Sprintf("unknown command %q", strings.TrimSpace(cmd.path()+" "+rest[0])),
				"run: "+strings.TrimSpace(cmd.path()+" --help")+suggest(rest[0], childNames(cmd))))
		}
		a.printHelp(cmd)
		if cmd == a.Root {
			return ExitOK
		}
		return ExitUsage
	}
	ctx, err := a.parse(cmd, rest)
	if err != nil {
		return a.fail(format, err)
	}
	if err := cmd.Run(ctx); err != nil {
		if errors.Is(err, errDryRun) {
			return ExitDryRun
		}
		return a.fail(ctx.Format, err)
	}
	return ExitOK
}

func link(c, parent *Command) {
	c.parent = parent
	for _, ch := range c.Children {
		link(ch, c)
	}
}

func (c *Command) path() string {
	if c.parent == nil {
		return c.Name
	}
	return c.parent.path() + " " + c.Name
}

func (a *App) resolve(args []string) (*Command, []string) {
	cur := a.Root
	for i, arg := range args {
		next := (*Command)(nil)
		for _, ch := range cur.Children {
			if ch.Name == arg {
				next = ch
			}
		}
		if next == nil {
			return cur, args[i:]
		}
		cur = next
	}
	return cur, nil
}

func (a *App) defaultFormat() string {
	if a.IsTTY {
		return "table"
	}
	return "json"
}

func (a *App) allFlags(cmd *Command) []Flag {
	return append(append([]Flag(nil), cmd.Flags...), a.Global...)
}

func (a *App) parse(cmd *Command, args []string) (*Ctx, error) {
	flags := a.allFlags(cmd)
	if cmd.Effects {
		flags = append(flags, Flag{Name: "dry-run", Type: Bool, Desc: "show what would change, change nothing (exit 10)"})
	}
	byName := map[string]Flag{}
	var names []string
	for _, f := range flags {
		byName[f.Name] = f
		names = append(names, f.Name)
	}
	ctx := &Ctx{App: a, Cmd: cmd, values: map[string]any{}}
	set := func(f Flag, raw string) error {
		v, err := convert(f, raw)
		if err != nil {
			return &Error{Code: ExitUsage, Type: "usage", Message: err.Error(), Input: "--" + f.Name + " " + raw,
				Suggestion: "run: " + cmd.path() + " --help"}
		}
		if f.Type == List {
			prev, _ := ctx.values[f.Name].([]string)
			v = append(prev, raw)
		}
		ctx.values[f.Name] = v
		ctx.values["set:"+f.Name] = true
		return nil
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			return nil, &Error{Code: ExitUsage, Type: "usage", Input: arg,
				Message:    fmt.Sprintf("unexpected argument %q (this CLI takes --flags only)", arg),
				Suggestion: "run: " + cmd.path() + " --help"}
		}
		name, val, hasVal := strings.Cut(arg[2:], "=")
		f, ok := byName[name]
		if !ok && strings.HasPrefix(name, "no-") {
			if nf, ok2 := byName[name[3:]]; ok2 && nf.Type == Bool && !hasVal {
				ctx.values[nf.Name] = false
				ctx.values["set:"+nf.Name] = true
				continue
			}
		}
		if !ok {
			return nil, &Error{Code: ExitUsage, Type: "usage", Input: arg,
				Message:    fmt.Sprintf("unknown flag --%s for %q", name, cmd.path()),
				Suggestion: "run: " + cmd.path() + " --help" + suggest(name, names)}
		}
		if f.Type == Bool && !hasVal {
			val, hasVal = "true", true
		}
		if !hasVal {
			if i+1 >= len(args) {
				return nil, Usage(fmt.Sprintf("flag --%s needs a value", name), "run: "+cmd.path()+" --help")
			}
			i++
			val = args[i]
		}
		if err := set(f, val); err != nil {
			return nil, err
		}
	}
	for _, f := range flags {
		if _, ok := ctx.values[f.Name]; ok {
			continue
		}
		raw := f.Default
		if f.Env != "" && a.Getenv != nil {
			if e := a.Getenv(f.Env); e != "" {
				raw = e
			}
		}
		if raw == "" {
			if f.Required {
				return nil, Usage(fmt.Sprintf("missing required flag --%s", f.Name), "run: "+cmd.path()+" --help")
			}
			if f.Type == Bool {
				ctx.values[f.Name] = false
			}
			continue
		}
		v, err := convert(f, raw)
		if err != nil {
			return nil, Usage(fmt.Sprintf("--%s: %v", f.Name, err), "")
		}
		ctx.values[f.Name] = v
	}
	ctx.Format = ctx.Str("format")
	if ctx.Format == "" {
		ctx.Format = a.defaultFormat()
	}
	return ctx, nil
}

func convert(f Flag, raw string) (any, error) {
	switch f.Type {
	case Bool:
		return strconv.ParseBool(raw)
	case Int:
		return strconv.Atoi(raw)
	case Duration:
		return time.ParseDuration(raw)
	case Enum:
		for _, e := range f.Enum {
			if raw == e {
				return raw, nil
			}
		}
		return nil, fmt.Errorf("--%s must be one of %s, got %q", f.Name, strings.Join(f.Enum, "|"), raw)
	case List:
		return []string{raw}, nil
	}
	return raw, nil
}

func (a *App) fail(format string, err error) int {
	var e *Error
	if !errors.As(err, &e) {
		e = &Error{Code: ExitError, Type: "failed", Message: err.Error()}
	}
	if format == "table" {
		fmt.Fprintf(a.Stderr, "error: %s\n", e.Message)
		if e.Suggestion != "" {
			fmt.Fprintf(a.Stderr, "  %s\n", e.Suggestion)
		}
	} else {
		b, _ := json.Marshal(e)
		fmt.Fprintln(a.Stderr, string(b))
	}
	return e.Code
}

// ---- help & schema ----

func (a *App) printHelp(c *Command) {
	w := a.Stdout
	fmt.Fprintf(w, "%s — %s\n", c.path(), c.Summary)
	if c.Desc != "" {
		fmt.Fprintf(w, "\n%s\n", c.Desc)
	}
	if len(c.Examples) > 0 {
		fmt.Fprintln(w, "\nExamples:")
		for _, e := range c.Examples {
			fmt.Fprintf(w, "  %s\n", e)
		}
	}
	if len(c.Children) > 0 {
		fmt.Fprintln(w, "\nCommands:")
		for _, ch := range c.Children {
			fmt.Fprintf(w, "  %-10s %s\n", ch.Name, ch.Summary)
		}
		fmt.Fprintf(w, "\nRun '%s <command> --help' for details.\n", c.path())
	}
	if c.Run != nil {
		fmt.Fprintln(w, "\nFlags:")
		flags := c.Flags
		if c.Effects {
			flags = append(append([]Flag(nil), flags...), Flag{Name: "dry-run", Type: Bool, Desc: "show what would change, change nothing (exit 10)"})
		}
		for _, f := range append(flags, a.Global...) {
			fmt.Fprintf(w, "  --%-18s %s\n", f.Name+" "+typeHint(f), describe(f))
		}
	}
	if c.parent == nil {
		fmt.Fprintln(w, "\nExit codes: 0 ok · 1 error · 2 usage · 3 not found · 4 permission denied · 5 conflict · 10 dry-run passed")
	}
}

func typeHint(f Flag) string {
	switch f.Type {
	case Bool:
		return ""
	case Enum:
		return strings.Join(f.Enum, "|")
	case List:
		return "<value> (repeatable)"
	}
	return "<" + string(f.Type) + ">"
}

func describe(f Flag) string {
	d := f.Desc
	switch {
	case f.Required:
		d += " (required)"
	case f.Default != "":
		d += fmt.Sprintf(" (default: %s)", f.Default)
	}
	if f.Env != "" {
		d += fmt.Sprintf(" [env %s]", f.Env)
	}
	return d
}

// Schema describes a command subtree as data (for `schema`).
func (a *App) Schema(path []string) (any, error) {
	link(a.Root, nil)
	c, rest := a.resolve(path)
	if len(rest) > 0 {
		return nil, NotFound(fmt.Sprintf("no command %q", strings.Join(path, " ")), "run: "+a.Root.Name+" schema")
	}
	return a.schemaOf(c), nil
}

func (a *App) schemaOf(c *Command) map[string]any {
	m := map[string]any{"command": c.path(), "summary": c.Summary}
	if c.Desc != "" {
		m["description"] = c.Desc
	}
	if len(c.Examples) > 0 {
		m["examples"] = c.Examples
	}
	if c.Run != nil {
		flags := append([]Flag(nil), c.Flags...)
		if c.Effects {
			flags = append(flags, Flag{Name: "dry-run", Type: Bool, Desc: "show what would change, change nothing (exit 10)"})
		}
		m["flags"] = append(flags, a.Global...)
		m["side_effects"] = c.Effects
	}
	if len(c.Children) > 0 {
		var kids []any
		for _, ch := range c.Children {
			kids = append(kids, a.schemaOf(ch))
		}
		m["commands"] = kids
	}
	return m
}

func childNames(c *Command) []string {
	var n []string
	for _, ch := range c.Children {
		n = append(n, ch.Name)
	}
	return n
}

// suggest returns "; did you mean …?" for the closest candidate.
func suggest(got string, candidates []string) string {
	best, bestD := "", 4
	sort.Strings(candidates)
	for _, c := range candidates {
		if d := distance(got, c); d < bestD {
			best, bestD = c, d
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf("; did you mean %q?", best)
}

func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// StdoutIsTTY reports whether stdout is a terminal.
func StdoutIsTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
