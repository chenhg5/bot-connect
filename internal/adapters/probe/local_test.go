package probe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/bot-connect/internal/domain/observe"
)

func TestLocalProbe(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "proj", "goals"), 0o755)
	os.WriteFile(filepath.Join(root, "proj", "goals", "g.md"), []byte("目标：发版\napi_key = \"apikey_abcdefghijklmnop\"\n"), 0o644)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644)
	os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "proj", "link.txt"))
	l := &Local{Roots: []string{root}}
	ctx := context.Background()

	out, err := l.Probe(ctx, observe.Probe{Kind: observe.File, Target: filepath.Join(root, "proj", "goals", "g.md")})
	if err != nil || !strings.Contains(out, "目标：发版") || strings.Contains(out, "abcdefghijklmnop") {
		t.Fatalf("file read with secrets masked: %v %q", err, out)
	}
	if _, err := l.Probe(ctx, observe.Probe{Kind: observe.File, Target: filepath.Join(outside, "secret.txt")}); err == nil {
		t.Fatal("outside the roots")
	}
	if _, err := l.Probe(ctx, observe.Probe{Kind: observe.File, Target: filepath.Join(root, "proj", "link.txt")}); err == nil {
		t.Fatal("a symlink out of the roots")
	}
	out, err = l.Probe(ctx, observe.Probe{Kind: observe.Dir, Target: filepath.Join(root, "proj")})
	if err != nil || !strings.Contains(out, "goals/g.md") || !strings.Contains(out, "最近改动") {
		t.Fatalf("dir: %v %q", err, out)
	}
	for _, c := range [][]string{{"rm", "-rf", "x"}, {"git", "push"}, {"git", "log", "--output=x"}, {"git", "-c", "core.pager=sh", "log"}, {"/bin/ls"}, {"gh", "api", "-X", "POST", "x"}} {
		if err := l.Allowed(c[0], c[1:]); err == nil {
			t.Fatalf("%v must not be allowed", c)
		}
	}
	if err := l.Allowed("git", []string{"log", "--oneline", "-5"}); err != nil {
		t.Fatal(err)
	}
	l.Extra = func() []string { return []string{"hp * get"} }
	if err := l.Allowed("hp", []string{"state", "get", "--project", "."}); err != nil {
		t.Fatalf("a pattern allowed at run time: %v", err)
	}
	if err := l.Allowed("hp", []string{"state", "update"}); err == nil {
		t.Fatal("only what was allowed")
	}
	if _, err := l.Probe(ctx, observe.Probe{Kind: observe.HTTP, Target: "https://example.com"}); err == nil {
		t.Fatal("hosts must be allowed")
	}
}

func TestRedact(t *testing.T) {
	in := "token: ghp_abcdefghijklmnopqrstuvwxyz123\nAuthorization: Bearer abcdefghijklmnopqrstu\nsk-cp-abcdefghijklmnopqrstuv"
	out := Redact(in)
	for _, leak := range []string{"ghp_abc", "abcdefghijklmnopqrstu", "sk-cp-abc"} {
		if strings.Contains(out, leak) {
			t.Fatalf("leaked %q: %s", leak, out)
		}
	}
}

func TestLocalDo(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.MkdirAll(filepath.Join(root, "p"), 0o755)
	os.Symlink(outside, filepath.Join(root, "p", "out"))
	l := &Local{Roots: []string{root}, Sessions: map[string][]string{"echo": {"echo", "got:", "{message}", "for", "{session}"}}}
	ctx := context.Background()
	if _, err := l.Do(ctx, observe.Action{Kind: observe.ActWrite, Path: filepath.Join(root, "p", "handoffs", "h.md"), Content: "交接"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Do(ctx, observe.Action{Kind: observe.ActWrite, Path: filepath.Join(root, "p", "handoffs", "h.md"), Content: "\n补充", Append: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "p", "handoffs", "h.md")); string(b) != "交接\n补充" {
		t.Fatalf("write + append: %q", b)
	}
	if _, err := l.Do(ctx, observe.Action{Kind: observe.ActWrite, Path: filepath.Join(root, "p", "out", "x.md"), Content: "x"}); err == nil {
		t.Fatal("writing through a symlink out of the roots")
	}
	if _, err := l.Do(ctx, observe.Action{Kind: observe.ActWrite, Path: filepath.Join(outside, "x.md"), Content: "x"}); err == nil {
		t.Fatal("writing outside the roots")
	}
	out, err := l.Do(ctx, observe.Action{Kind: observe.ActSession, Harness: "echo", Session: "S1", Message: "继续做发版清单", Dir: root})
	if err != nil || !strings.Contains(out, "got: 继续做发版清单 for S1") {
		t.Fatalf("session template: %v %q", err, out)
	}
	if _, err := l.Do(ctx, observe.Action{Kind: observe.ActSession, Harness: "nope", Session: "S", Message: "m"}); err == nil {
		t.Fatal("unknown harness")
	}
}
