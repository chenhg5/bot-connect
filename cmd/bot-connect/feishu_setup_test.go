package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestWriteFeishuConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	in := `[bot]
name = "b"
owners = ["ou_old"]   # keep me

[feishu]
app_id = ""
app_secret = ""

[[workers]]
name = "w"
`
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFeishuConfig(path, &registration{appID: "cli_x", appSecret: "sec", ownerOpenID: "ou_new", brand: "feishu"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var c struct {
		Bot struct {
			Owners []string `toml:"owners"`
		} `toml:"bot"`
		Feishu struct {
			AppID     string `toml:"app_id"`
			AppSecret string `toml:"app_secret"`
		} `toml:"feishu"`
		Workers []map[string]any `toml:"workers"`
	}
	if _, err := toml.Decode(string(b), &c); err != nil {
		t.Fatalf("result is not valid TOML: %v\n%s", err, b)
	}
	if c.Feishu.AppID != "cli_x" || c.Feishu.AppSecret != "sec" || strings.Join(c.Bot.Owners, ",") != "ou_old,ou_new" || len(c.Workers) != 1 {
		t.Fatalf("unexpected result:\n%s", b)
	}
}
