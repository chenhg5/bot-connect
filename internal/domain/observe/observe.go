// Package observe is how the bot follows work it doesn't run: looking at
// wherever the truth of a project lives (files, command-line tools, web
// pages), keeping a dossier of how to follow each project, and watching
// sources so that only a change — or a long silence — needs its attention.
//
// Nothing here knows any particular system (a project tool, a tracker, a
// forge). Those appear only as data: a probe's target and arguments, and
// the dossier's text.
package observe

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// Kind of probe.
type Kind string

const (
	File Kind = "file" // read a file
	Dir  Kind = "dir"  // list a directory tree (names, sizes, modification times)
	Cmd  Kind = "cmd"  // run an allowed read-only command
	HTTP Kind = "http" // GET a page or an API
	// ToolProbe runs an approved look tool from the toolbox (target = its name).
	ToolProbe Kind = "tool"
)

// Probe is one way to look at something.
type Probe struct {
	Kind   Kind     `json:"kind"`
	Target string   `json:"target"`         // path, URL, or the command's program
	Args   []string `json:"args,omitempty"` // command arguments
	Dir    string   `json:"dir,omitempty"`  // command working directory
	Depth  int      `json:"depth,omitempty"`
	// Filled in when a tool probe is resolved, never stored.
	Interpreter string `json:"-"`
	Code        string `json:"-"`
}

func (p Probe) Validate() error {
	switch p.Kind {
	case File, Dir, HTTP:
		if strings.TrimSpace(p.Target) == "" {
			return Invalid("a %s probe needs a target", p.Kind)
		}
	case Cmd:
		if strings.TrimSpace(p.Target) == "" {
			return Invalid("a command probe needs the program (target) and its args")
		}
	case ToolProbe:
		if strings.TrimSpace(p.Target) == "" {
			return Invalid("a tool probe needs the tool's name (target)")
		}
	default:
		return Invalid("unknown probe kind %q (file, dir, cmd, http, tool)", p.Kind)
	}
	return nil
}

func (p Probe) String() string {
	switch p.Kind {
	case ToolProbe:
		return strings.TrimSpace("工具 " + p.Target + " " + strings.Join(p.Args, " "))
	case Cmd:
		s := p.Target + " " + strings.Join(p.Args, " ")
		if p.Dir != "" {
			s += "（在 " + p.Dir + "）"
		}
		return strings.TrimSpace(s)
	default:
		return string(p.Kind) + " " + p.Target
	}
}

// Source is a place the truth of a project lives, and how to read it.
type Source struct {
	Name  string `json:"name"`
	What  string `json:"what,omitempty"` // what it tells you
	Probe Probe  `json:"probe"`
}

// Dossier is the bot's own handbook for following one project: what the
// goals are and when they're done, where the truth lives and how to read
// it, who does the work and how to reach them, the rhythm, what counts as a
// risk. Written by the brain, kept small, shown whenever it works on the
// project.
type Dossier struct {
	Project   ProjectID `json:"project"`
	Text      string    `json:"text"`
	Sources   []Source  `json:"sources,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DossierLimit bounds the text so it always fits in a wake-up.
const DossierLimit = 6000

func (d *Dossier) Write(text string, sources []Source, now time.Time) error {
	if n := utf8.RuneCountInString(text); n > DossierLimit {
		return Invalid("dossier is %d characters; keep it under %d (it's a handbook, not a dump)", n, DossierLimit)
	}
	for _, s := range sources {
		if err := s.Probe.Validate(); err != nil {
			return fmt.Errorf("source %q: %w", s.Name, err)
		}
	}
	d.Text, d.UpdatedAt = strings.TrimSpace(text), now
	if sources != nil {
		d.Sources = sources
	}
	return nil
}

// Observation is what a probe saw once.
type Observation struct {
	At      time.Time `json:"at"`
	Hash    string    `json:"hash,omitempty"`
	Text    string    `json:"text,omitempty"` // kept for the next diff (capped)
	Err     string    `json:"err,omitempty"`
	Changed bool      `json:"changed,omitempty"`
}

// KeepText caps what a watch remembers of its last observation.
const KeepText = 8000

func Observe(text string, err error, at time.Time) Observation {
	o := Observation{At: at}
	if err != nil {
		o.Err = err.Error()
		return o
	}
	sum := sha256.Sum256([]byte(text))
	o.Hash = hex.EncodeToString(sum[:8])
	if r := []rune(text); len(r) > KeepText {
		text = string(r[:KeepText])
	}
	o.Text = text
	return o
}

// Watch looks at a source on a rhythm. Code compares what it sees; the
// brain hears about it only when it changed, kept failing, or stayed the
// same longer than it should (no progress).
type Watch struct {
	ID         string        `json:"id"`
	Project    ProjectID     `json:"project"`
	Name       string        `json:"name"`
	Probe      Probe         `json:"probe"`
	Every      time.Duration `json:"every"`
	StaleAfter time.Duration `json:"stale_after,omitempty"` // no change for this long is news
	Expect     string        `json:"expect,omitempty"`      // what progress looks like here, for the brain
	Paused     bool          `json:"paused,omitempty"`
	Last       Observation   `json:"last"`
	ChangedAt  time.Time     `json:"changed_at,omitempty"` // last time the content changed
	Failures   int           `json:"failures,omitempty"`
	StaleSent  time.Time     `json:"stale_sent,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
	By         Actor         `json:"by"`
}

// MinEvery keeps watches from hammering anything.
const MinEvery = 15 * time.Minute

func NewWatch(id string, project ProjectID, name string, p Probe, every, stale time.Duration, expect string, now time.Time, by Actor) (Watch, error) {
	if err := p.Validate(); err != nil {
		return Watch{}, err
	}
	if strings.TrimSpace(name) == "" {
		return Watch{}, Invalid("a watch needs a name (what it keeps an eye on)")
	}
	if every < MinEvery {
		return Watch{}, Invalid("watch every %s at least", MinEvery)
	}
	if stale != 0 && stale < every {
		return Watch{}, Invalid("stale_after must be longer than every")
	}
	return Watch{ID: id, Project: project, Name: name, Probe: p, Every: every, StaleAfter: stale, Expect: expect, CreatedAt: now, ChangedAt: now, By: by}, nil
}

func (w Watch) Due(now time.Time) bool {
	return !w.Paused && (w.Last.At.IsZero() || !now.Before(w.Last.At.Add(w.Every)))
}

// Finding is what a new observation means.
type Finding string

const (
	Nothing Finding = ""
	First   Finding = "first"   // the baseline
	Changed Finding = "changed" // content differs from last time
	Failing Finding = "failing" // the probe failed several times in a row
	Stale   Finding = "stale"   // unchanged for longer than StaleAfter
)

// FailAfter consecutive failures are news.
const FailAfter = 3

// Record takes a new observation and says what it means. The previous one
// is returned for diffing.
func (w *Watch) Record(o Observation) (Finding, Observation) {
	prev := w.Last
	if o.Err != "" {
		w.Failures++
		w.Last.At = o.At // retry on the next rhythm, keep the last good content
		if w.Failures == FailAfter {
			return Failing, prev
		}
		return Nothing, prev
	}
	w.Failures = 0
	w.Last = o
	switch {
	case prev.Hash == "":
		w.ChangedAt = o.At
		return First, prev
	case prev.Hash != o.Hash:
		w.Last.Changed, w.ChangedAt, w.StaleSent = true, o.At, time.Time{}
		return Changed, prev
	}
	if w.StaleAfter > 0 && o.At.Sub(w.ChangedAt) >= w.StaleAfter && (w.StaleSent.IsZero() || o.At.Sub(w.StaleSent) >= w.StaleAfter) {
		w.StaleSent = o.At
		return Stale, prev
	}
	return Nothing, prev
}

// Diff is a short line diff: lines that went away and lines that appeared.
func Diff(before, after string, max int) string {
	if max <= 0 {
		max = 20
	}
	count := func(s string) map[string]int {
		m := map[string]int{}
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimRight(l, " \t\r"); l != "" {
				m[l]++
			}
		}
		return m
	}
	a, b := count(before), count(after)
	var gone, added []string
	for _, l := range strings.Split(before, "\n") {
		l = strings.TrimRight(l, " \t\r")
		if l != "" && b[l] < a[l] {
			gone = append(gone, "- "+l)
			a[l]--
		}
	}
	a = count(before)
	for _, l := range strings.Split(after, "\n") {
		l = strings.TrimRight(l, " \t\r")
		if l != "" && a[l] < b[l] {
			added = append(added, "+ "+l)
			b[l]--
		}
	}
	lines := append(gone, added...)
	more := 0
	if len(lines) > max {
		more, lines = len(lines)-max, lines[:max]
	}
	out := strings.Join(lines, "\n")
	if more > 0 {
		out += fmt.Sprintf("\n…还有 %d 行变化", more)
	}
	return out
}
