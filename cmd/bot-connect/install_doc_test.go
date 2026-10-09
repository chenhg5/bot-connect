package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/chenhg5/bot-connect/internal/config"
)

// Every complete config in INSTALL.md must load, so agents following the
// guide never write an invalid config.
func TestInstallDocConfigsLoad(t *testing.T) {
	doc, err := os.ReadFile("../../INSTALL.md")
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile("(?s)```toml\n(.*?)```").FindAllStringSubmatch(string(doc), -1)
	n := 0
	for _, b := range blocks {
		body := b[1]
		if !strings.Contains(body, "[bot]") { // fragments, not whole configs
			continue
		}
		n++
		p := filepath.Join(t.TempDir(), "c.toml")
		_ = os.WriteFile(p, []byte("data_dir = \""+t.TempDir()+"\"\n"+body), 0o600)
		t.Setenv("BOT_BRAIN_API_KEY", "test")
		if _, err := config.Load(p); err != nil {
			t.Errorf("config block %d does not load: %v\n%s", n, err, body)
		}
	}
	if n < 2 {
		t.Fatalf("expected the example configs in INSTALL.md, found %d", n)
	}
}
