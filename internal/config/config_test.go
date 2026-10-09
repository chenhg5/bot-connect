package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestMultiBot(t *testing.T) {
	dir := t.TempDir()
	c, err := load(t, `data_dir = "`+dir+`"
[[workers]]
name = "a"
work_dir = "`+dir+`"

[[bots]]
name = "alice"
owners = ["ou_1"]
workers = ["a"]
[bots.brain]
system_prompt = "be brief"
[bots.feishu]
larkcli_profile = "p1"

[[bots]]
name = "jack"
[bots.feishu]
app_id = "cli_x"
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Bots) != 2 || c.Bots[0].Brain.SystemPrompt != "be brief" || c.Bots[0].Workers[0] != "a" || c.Bots[1].Feishu.AppID != "cli_x" {
		t.Fatalf("bots not parsed: %+v", c.Bots)
	}
	if c.Bots[0].Dir == c.Bots[1].Dir || !strings.HasPrefix(c.Bots[1].Brain.WorkDir, filepath.Join(dir, "bots", "jack")) {
		t.Fatalf("bots must get separate dirs: %q %q", c.Bots[0].Dir, c.Bots[1].Brain.WorkDir)
	}
}

func TestSingleBotShorthand(t *testing.T) {
	dir := t.TempDir()
	c, err := load(t, "[bot]\nname = \"solo\"\ndata_dir = \""+dir+"\"\n[feishu]\nlarkcli_profile = \"p\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Bots) != 1 || c.Bots[0].Name != "solo" || c.Bots[0].Dir != dir {
		t.Fatalf("shorthand: %+v", c.Bots)
	}
}

func TestRejectsSharedApp(t *testing.T) {
	_, err := load(t, "[[bots]]\nname = \"a\"\n[bots.feishu]\nlarkcli_profile = \"p\"\n[[bots]]\nname = \"b\"\n[bots.feishu]\nlarkcli_profile = \"p\"\n")
	if err == nil || !strings.Contains(err.Error(), "same Feishu app") {
		t.Fatalf("two bots on one app must be rejected, got %v", err)
	}
}
