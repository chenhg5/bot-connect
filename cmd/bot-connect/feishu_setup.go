package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/mdp/qrterminal/v3"
)

// `bot-connect feishu setup`: create a Feishu/Lark bot by scanning a QR code
// (the same device-registration flow cc-connect uses), then write app_id,
// app_secret and the scanner's open_id (as owner) into the config.

const (
	accountsFeishu = "https://accounts.feishu.cn"
	accountsLark   = "https://accounts.larksuite.com"
)

func feishuCLI(args []string) int {
	if len(args) == 0 || args[0] != "setup" {
		fmt.Fprintln(os.Stderr, "usage: bot-connect feishu setup [-config config.toml] [-timeout 600]")
		return 2
	}
	fs := flag.NewFlagSet("feishu setup", flag.ExitOnError)
	cfgPath := fs.String("config", "config.toml", "config file to write (created from config.example.toml if missing)")
	timeout := fs.Int("timeout", 600, "seconds to wait for the QR scan")
	_ = fs.Parse(args[1:])

	res, err := registerBot(time.Duration(*timeout) * time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "feishu setup failed:", err)
		return 1
	}
	if err := writeFeishuConfig(*cfgPath, res); err != nil {
		fmt.Fprintln(os.Stderr, "write config:", err)
		return 1
	}
	fmt.Printf("\n✅ 机器人已创建：app_id=%s (%s)\n", res.appID, res.brand)
	if res.ownerOpenID != "" {
		fmt.Printf("   扫码人 %s 已设为 owner\n", res.ownerOpenID)
	}
	fmt.Printf("   已写入 %s，运行：bot-connect -config %s\n", *cfgPath, *cfgPath)
	return 0
}

type registration struct {
	appID, appSecret, ownerOpenID, brand string
}

type regClient struct {
	base string
	hc   *http.Client
}

func (c *regClient) call(action string, params map[string]string, out any) error {
	form := url.Values{"action": {action}}
	for k, v := range params {
		form.Set(k, v)
	}
	resp, err := c.hc.Post(c.base+"/oauth/v1/app/registration", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: decode response (http %d): %w", action, resp.StatusCode, err)
	}
	return nil
}

type regError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (e regError) err() error {
	if e.Error == "" {
		return nil
	}
	return fmt.Errorf("%s: %s", e.Error, e.ErrorDescription)
}

func registerBot(timeout time.Duration) (*registration, error) {
	c := &regClient{base: accountsFeishu, hc: &http.Client{Timeout: 15 * time.Second}}

	var initRes struct {
		SupportedAuthMethods []string `json:"supported_auth_methods"`
		regError
	}
	if err := c.call("init", nil, &initRes); err != nil {
		return nil, err
	}
	if err := initRes.err(); err != nil {
		return nil, err
	}

	var begin struct {
		DeviceCode string `json:"device_code"`
		URL        string `json:"verification_uri_complete"`
		Interval   int    `json:"interval"`
		ExpireIn   int    `json:"expire_in"`
		regError
	}
	err := c.call("begin", map[string]string{
		"archetype":         "PersonalAgent",
		"auth_method":       "client_secret",
		"request_user_info": "open_id",
	}, &begin)
	if err != nil {
		return nil, err
	}
	if err := begin.err(); err != nil {
		return nil, err
	}
	if begin.DeviceCode == "" || begin.URL == "" {
		return nil, fmt.Errorf("incomplete registration response")
	}

	fmt.Println("请用飞书 / Lark 手机 App 扫码，创建你的 bot 并授权：")
	fmt.Printf("URL: %s\n\n", begin.URL)
	qrterminal.GenerateWithConfig(begin.URL, qrterminal.Config{
		Level: qrterminal.M, Writer: os.Stdout, BlackChar: "██", WhiteChar: "  ", QuietZone: 2,
	})
	fmt.Println("\n等待扫码…")

	interval := begin.Interval
	if interval <= 0 {
		interval = 5
	}
	deadline := time.Now().Add(timeout)
	if begin.ExpireIn > 0 {
		if d := time.Now().Add(time.Duration(begin.ExpireIn) * time.Second); d.Before(deadline) {
			deadline = d
		}
	}
	brand := "feishu"
	for time.Now().Before(deadline) {
		var poll struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
			UserInfo     struct {
				OpenID      string `json:"open_id"`
				TenantBrand string `json:"tenant_brand"`
			} `json:"user_info"`
			regError
		}
		if err := c.call("poll", map[string]string{"device_code": begin.DeviceCode}, &poll); err != nil {
			return nil, err
		}
		// Lark tenants finish the flow on the Lark accounts domain.
		if strings.EqualFold(poll.UserInfo.TenantBrand, "lark") && c.base != accountsLark {
			brand, c.base = "lark", accountsLark
			continue
		}
		if poll.ClientID != "" && poll.ClientSecret != "" {
			return &registration{appID: poll.ClientID, appSecret: poll.ClientSecret, ownerOpenID: poll.UserInfo.OpenID, brand: brand}, nil
		}
		switch poll.Error {
		case "", "authorization_pending":
		case "slow_down":
			interval += 5
		case "access_denied":
			return nil, fmt.Errorf("authorization denied")
		case "expired_token":
			return nil, fmt.Errorf("QR code expired")
		default:
			return nil, poll.err()
		}
		time.Sleep(time.Duration(interval) * time.Second)
	}
	return nil, fmt.Errorf("timed out waiting for the scan")
}

// writeFeishuConfig sets [feishu] app_id/app_secret(/domain) and adds the
// owner to bot.owners, editing the TOML text in place to keep comments.
func writeFeishuConfig(path string, r *registration) error {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		raw, err = os.ReadFile("config.example.toml")
		if err != nil {
			return fmt.Errorf("%s not found and no config.example.toml to start from", path)
		}
	} else if err != nil {
		return err
	}
	s := string(raw)

	var cur struct {
		Bot struct {
			Owners []string `toml:"owners"`
		} `toml:"bot"`
	}
	_, _ = toml.Decode(s, &cur)

	s = setInSection(s, "feishu", "app_id", fmt.Sprintf("%q", r.appID))
	s = setInSection(s, "feishu", "app_secret", fmt.Sprintf("%q", r.appSecret))
	if r.brand == "lark" {
		s = setInSection(s, "feishu", "domain", `"https://open.larksuite.com"`)
	}
	if r.ownerOpenID != "" {
		owners := cur.Bot.Owners
		found := false
		for _, o := range owners {
			found = found || o == r.ownerOpenID
		}
		if !found {
			owners = append(owners, r.ownerOpenID)
		}
		quoted := make([]string, len(owners))
		for i, o := range owners {
			quoted[i] = fmt.Sprintf("%q", o)
		}
		s = setInSection(s, "bot", "owners", "["+strings.Join(quoted, ", ")+"]")
	}
	return os.WriteFile(path, []byte(s), 0o600)
}

// setInSection replaces `key = ...` inside [section] (or adds it right after
// the header, creating the section at the end if it doesn't exist).
func setInSection(doc, section, key, value string) string {
	lines := strings.Split(doc, "\n")
	header := regexp.MustCompile(`^\s*\[\[?([^\]]+)\]?\]\s*(#.*)?$`)
	keyRe := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	in, headerAt := false, -1
	for i, l := range lines {
		if m := header.FindStringSubmatch(l); m != nil {
			if in {
				break // left the section without finding the key
			}
			if strings.TrimSpace(m[1]) == section && !strings.HasPrefix(strings.TrimSpace(l), "[[") {
				in, headerAt = true, i
			}
			continue
		}
		if in && keyRe.MatchString(l) {
			lines[i] = key + " = " + value
			return strings.Join(lines, "\n")
		}
	}
	if headerAt >= 0 {
		lines = append(lines[:headerAt+1], append([]string{key + " = " + value}, lines[headerAt+1:]...)...)
		return strings.Join(lines, "\n")
	}
	return strings.TrimRight(doc, "\n") + fmt.Sprintf("\n\n[%s]\n%s = %s\n", section, key, value)
}
