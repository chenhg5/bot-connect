// Package sim runs golden cases: a real brain inside a simulated world —
// a virtual clock, scripted people, fake agent workers, an in-memory store —
// driven by steps (someone says something, time passes, an agent delivers)
// and checked by code (state, messages, tool calls). It is how bot-connect's
// development checks that a change of prompt, model or rules still does the
// right thing.
package sim

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Case is one golden case.
type Case struct {
	ID        string `toml:"id"`
	Title     string `toml:"title"`
	Critical  bool   `toml:"critical"`
	Milestone string `toml:"milestone"`
	Notes     string `toml:"notes"`
	Now       string `toml:"now"` // virtual start time, "2026-10-10 10:00" (local)

	People      []PersonSpec  `toml:"people"`
	Agents      []AgentSpec   `toml:"agents"`
	Projects    []ProjectSpec `toml:"projects"`
	Policies    []PolicySpec  `toml:"policies"`
	Items       []ItemSpec    `toml:"items"`
	Assignments []AssignSpec  `toml:"assignments"`

	Steps  []Step  `toml:"steps"`
	Expect []Check `toml:"expect"` // after all steps

	file string
}

type PersonSpec struct {
	ID          string   `toml:"id"`
	Name        string   `toml:"name"`
	Description string   `toml:"description"`
	Skills      []string `toml:"skills"`
	Consent     bool     `toml:"consent"`
	Away        string   `toml:"away"`       // declared absence ("休假到周五")
	WorkHours   string   `toml:"work_hours"` // "09:00-19:00"
	Authority   []string `toml:"authority"`  // actions they may approve
	Physical    *bool    `toml:"physical"`
}

type AgentSpec struct {
	ID           string   `toml:"id"`
	Name         string   `toml:"name"`
	Description  string   `toml:"description"`
	Capabilities []string `toml:"capabilities"`
}

type MemberSpec struct {
	Worker string   `toml:"worker"`
	Role   string   `toml:"role"`
	Duties []string `toml:"duties"`
	From   string   `toml:"from"`
	Until  string   `toml:"until"`
}

type ProjectSpec struct {
	ID        string       `toml:"id"` // "org" for the company
	Title     string       `toml:"title"`
	Priority  string       `toml:"priority"`
	From      string       `toml:"from"`
	Until     string       `toml:"until"`
	Objective string       `toml:"objective"`
	Members   []MemberSpec `toml:"members"`
}

type PolicySpec struct {
	Project   string   `toml:"project"` // default org
	Action    string   `toml:"action"`
	Scope     string   `toml:"scope"`
	Approvers []string `toml:"approvers"`
}

type ItemSpec struct {
	Ref       string   `toml:"ref"` // name later entries use
	Project   string   `toml:"project"`
	Title     string   `toml:"title"`
	Due       string   `toml:"due"`
	Estimate  string   `toml:"estimate"`
	Done      []string `toml:"done"`
	Needs     []string `toml:"needs"`
	DependsOn []string `toml:"depends_on"` // refs
	Status    string   `toml:"status"`     // "done" to start completed
	Started   string   `toml:"started"`    // with an assignment: when work began ("-3h")
}

type AssignSpec struct {
	Item    string   `toml:"item"` // ref
	Worker  string   `toml:"worker"`
	Kind    string   `toml:"kind"`
	Why     string   `toml:"why"`
	Options []string `toml:"options"`
	Goal    string   `toml:"goal"`
	Status  string   `toml:"status"` // offered (default) | accepted | delivered
	Result  string   `toml:"result"` // delivered: what was handed in
}

// Step is one thing that happens.
type Step struct {
	Say     string `toml:"say"`
	From    string `toml:"from"`  // "owner" (default) or a person id
	Group   string `toml:"group"` // say it in this group chat (a mention)
	Advance string `toml:"advance"`
	Deliver string `toml:"deliver"` // an agent delivers its open assignment: agent id
	Result  string `toml:"result"`
	Timeout string `toml:"timeout"`

	Expect []Check `toml:"expect"`
}

// Check is one assertion. Type decides which fields matter.
//
//	reply          messages to To ("owner", a person id, "group:<id>") since the step began
//	               must contain one of ContainsAny / none of NotContains (Min: at least that many messages)
//	no_message     nothing sent to To ("" = to anyone)
//	assignment     an assignment exists matching Worker / Kind / Status / Title / Due (Count: exactly that many)
//	no_assignment  none matching
//	item           an item matching Title / Status / Due
//	tool           Called were all used, NotCalled none, during the step (or the case)
//	unchanged      no item or assignment changed status during the step
//	latency        the first message after the step began came within Max
type Check struct {
	Type        string   `toml:"type"`
	To          string   `toml:"to"`
	ContainsAny []string `toml:"contains_any"`
	NotContains []string `toml:"not_contains"`
	Min         int      `toml:"min"`
	Worker      string   `toml:"worker"`
	Kind        string   `toml:"kind"`
	Status      string   `toml:"status"`
	Title       string   `toml:"title"`
	Due         string   `toml:"due"`
	Count       *int     `toml:"count"`
	Called      []string `toml:"called"`
	NotCalled   []string `toml:"not_called"`
	Max         string   `toml:"max"`
	Why         string   `toml:"why"` // what the check protects, shown when it fails
}

// LoadDir reads every *.toml case in a directory, sorted by id.
func LoadDir(dir string) ([]Case, error) { return LoadFS(os.DirFS(dir), ".") }

// LoadFS reads every *.toml case in dir of fsys, sorted by id.
func LoadFS(fsys fs.FS, dir string) ([]Case, error) {
	paths, err := fs.Glob(fsys, path.Join(dir, "*.toml"))
	if err != nil {
		return nil, err
	}
	var out []Case
	for _, p := range paths {
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil, err
		}
		c, err := Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		c.file = p
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return caseOrder(out[i].ID) < caseOrder(out[j].ID) })
	return out, nil
}

func caseOrder(id string) string {
	// G2 before G10
	n := strings.TrimLeft(id, "ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	return id[:len(id)-len(n)] + fmt.Sprintf("%04s", n)
}

// Parse reads one case.
func Parse(b []byte) (Case, error) {
	var c Case
	md, err := toml.Decode(string(b), &c)
	if err != nil {
		return c, err
	}
	if u := md.Undecoded(); len(u) > 0 {
		return c, fmt.Errorf("unknown fields: %v", u)
	}
	if c.ID == "" || len(c.Steps) == 0 {
		return c, fmt.Errorf("a case needs an id and steps")
	}
	return c, nil
}

// parseTime reads "2026-10-14 18:00" in local time, or an offset from base ("-3h", "+2d").
func parseTime(s string, base time.Time) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	if s[0] == '-' || s[0] == '+' {
		v := s[1:]
		var d time.Duration
		if strings.HasSuffix(v, "d") {
			var n int
			if _, err := fmt.Sscanf(strings.TrimSuffix(v, "d"), "%d", &n); err != nil {
				return nil, fmt.Errorf("bad offset %q", s)
			}
			d = time.Duration(n) * 24 * time.Hour
		} else {
			var err error
			if d, err = time.ParseDuration(v); err != nil {
				return nil, fmt.Errorf("bad offset %q", s)
			}
		}
		if s[0] == '-' {
			d = -d
		}
		t := base.Add(d)
		return &t, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			if layout == "2006-01-02" {
				t = t.Add(18 * time.Hour)
			}
			return &t, nil
		}
	}
	return nil, fmt.Errorf("bad time %q (use 2026-10-14 18:00 or -3h / +2d)", s)
}
