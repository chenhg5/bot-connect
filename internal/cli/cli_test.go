package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func testApp() (*App, *bytes.Buffer, *bytes.Buffer, *Ctx) {
	var got Ctx
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	a := &App{
		Stdout: out, Stderr: errb,
		Global: []Flag{{Name: "format", Type: Enum, Enum: []string{"json", "table"}, Desc: "output format"}},
		Root: &Command{Name: "tool", Summary: "test", Children: []*Command{
			{Name: "thing", Summary: "things", Children: []*Command{
				{Name: "create", Summary: "create one", Effects: true,
					Flags: []Flag{
						{Name: "name", Type: String, Required: true, Desc: "name"},
						{Name: "kind", Type: Enum, Enum: []string{"a", "b"}, Default: "a", Desc: "kind"},
						{Name: "force", Type: Bool, Desc: "force"},
					},
					Run: func(c *Ctx) error {
						got = *c
						if c.Bool("dry-run") {
							return c.DryRun(map[string]string{"create": c.Str("name")})
						}
						if c.Str("name") == "exists" {
							return Conflict("thing exists", "use --force")
						}
						return c.Out(map[string]string{"created": c.Str("name")}, nil)
					}},
			}},
		}},
	}
	return a, out, errb, &got
}

func TestExitCodesAndParsing(t *testing.T) {
	cases := []struct {
		args []string
		code int
		in   string // substring expected in stdout or stderr
	}{
		{[]string{"thing", "create", "--name", "x"}, ExitOK, `"created": "x"`},
		{[]string{"thing", "create", "--name=x", "--kind", "b", "--force"}, ExitOK, `"created": "x"`},
		{[]string{"thing", "create"}, ExitUsage, `missing required flag --name`},
		{[]string{"thing", "create", "--name", "x", "--kind", "c"}, ExitUsage, `one of a|b`},
		{[]string{"thing", "create", "--nmae", "x"}, ExitUsage, `did you mean \"name\"`},
		{[]string{"thing", "create", "x"}, ExitUsage, `takes --flags only`},
		{[]string{"thing", "create", "--name", "exists"}, ExitConflict, `"error":"conflict"`},
		{[]string{"thing", "create", "--name", "x", "--dry-run"}, ExitDryRun, `"dry_run": true`},
		{[]string{"thng"}, ExitUsage, `did you mean \"thing\"`},
		{[]string{"thing", "--help"}, ExitOK, "Commands:"},
		{[]string{"help", "thing", "create"}, ExitOK, "--name <string>"},
	}
	for _, c := range cases {
		a, out, errb, _ := testApp()
		code := a.Main(c.args)
		all := out.String() + errb.String()
		if code != c.code || !strings.Contains(all, c.in) {
			t.Errorf("%v: code %d (want %d), output:\n%s", c.args, code, c.code, all)
		}
	}
}

func TestDataOnStdoutErrorsOnStderr(t *testing.T) {
	a, out, errb, _ := testApp()
	a.Main([]string{"thing", "create", "--name", "exists"})
	if out.Len() != 0 {
		t.Fatalf("errors must not touch stdout: %q", out.String())
	}
	var e Error
	if err := json.Unmarshal(errb.Bytes(), &e); err != nil || e.Type != "conflict" || e.Suggestion == "" {
		t.Fatalf("non-TTY errors must be JSON with a suggestion: %q", errb.String())
	}
}

func TestNoFlagAndDefaults(t *testing.T) {
	a, _, _, got := testApp()
	a.Main([]string{"thing", "create", "--name", "x", "--no-force"})
	if got.Bool("force") || got.Str("kind") != "a" || got.Format != "json" {
		t.Fatalf("got force=%v kind=%q format=%q", got.Bool("force"), got.Str("kind"), got.Format)
	}
}

func TestSchema(t *testing.T) {
	a, _, _, _ := testApp()
	s, err := a.Schema([]string{"thing", "create"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	for _, want := range []string{`"command":"tool thing create"`, `"side_effects":true`, `"name":"dry-run"`, `"enum":["a","b"]`, `"required":true`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("schema lacks %s: %s", want, b)
		}
	}
	if _, err := a.Schema([]string{"nope"}); err == nil {
		t.Fatal("unknown command schema must fail")
	}
}
