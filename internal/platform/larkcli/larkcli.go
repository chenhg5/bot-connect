// Package larkcli connects the hub to Feishu/Lark through lark-cli, using an
// app that lark-cli already holds (e.g. one created with
// `lark-cli config init --new --name <profile>`). Credentials never leave
// lark-cli: messages are received via `lark-cli event consume` (NDJSON on
// stdout) and sent via `lark-cli api`.
package larkcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/chenhg5/bot-connect/internal/hub"
	"github.com/chenhg5/bot-connect/internal/identity"
)

type Config struct {
	Profile  string // lark-cli profile name
	Bin      string // lark-cli binary, default "lark-cli"
	Reaction string // emoji added on receipt; "none" disables
}

type Platform struct {
	cfg       Config
	botOpenID string
}

func New(cfg Config) *Platform {
	if cfg.Bin == "" {
		cfg.Bin = "lark-cli"
	}
	if cfg.Reaction == "" {
		cfg.Reaction = "OnIt"
	}
	return &Platform{cfg: cfg}
}

func (p *Platform) Name() string { return "feishu" }

func (p *Platform) Start(ctx context.Context, onMessage func(hub.Inbound)) error {
	out, err := p.api(ctx, "GET", "/open-apis/bot/v3/info", "", "")
	if err != nil {
		return fmt.Errorf("lark-cli bot info (profile %s): %w", p.cfg.Profile, err)
	}
	var info struct {
		Data struct {
			OpenID  string `json:"open_id"`
			AppName string `json:"app_name"`
		} `json:"data"`
	}
	_ = json.Unmarshal(out, &info)
	p.botOpenID = info.Data.OpenID
	slog.Info("lark-cli bot ready", "profile", p.cfg.Profile, "app", info.Data.AppName, "bot_open_id", p.botOpenID)
	go p.consumeLoop(ctx, onMessage)
	return nil
}

// consumeLoop keeps a `lark-cli event consume` subprocess running.
func (p *Platform) consumeLoop(ctx context.Context, onMessage func(hub.Inbound)) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := p.consume(ctx, onMessage)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		slog.Warn("lark-cli event consume exited; restarting", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

type event struct {
	ChatID      string `json:"chat_id"`
	ChatType    string `json:"chat_type"`
	Content     string `json:"content"`
	MessageID   string `json:"message_id"`
	MessageType string `json:"message_type"`
	SenderID    string `json:"sender_id"`
	SenderType  string `json:"sender_type"`
	Mentions    []struct {
		ID   string `json:"id"`
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"mentions"`
}

func (p *Platform) consume(ctx context.Context, onMessage func(hub.Inbound)) error {
	cmd := exec.CommandContext(ctx, p.cfg.Bin, "event", "consume", "im.message.receive_v1",
		"--profile", p.cfg.Profile, "--as", "bot")
	// consume treats stdin EOF as "stop": hold stdin open for the process's
	// lifetime, and stop it with SIGTERM so it cleans up its subscription.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer stdin.Close()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	slog.Info("lark-cli event consume started", "profile", p.cfg.Profile)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	for sc.Scan() {
		var ev event
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.MessageID == "" {
			continue
		}
		if in, ok := p.parse(ev); ok {
			onMessage(in)
		}
	}
	err = cmd.Wait()
	if s := strings.TrimSpace(stderr.String()); s != "" && err != nil {
		return fmt.Errorf("%v: %s", err, tail(s, 400))
	}
	return err
}

var mentionKeyRe = regexp.MustCompile(`@_user_\d+\s*`)

func (p *Platform) parse(ev event) (hub.Inbound, bool) {
	if ev.SenderType == "app" {
		return hub.Inbound{}, false
	}
	isGroup := ev.ChatType == "group"
	text := ev.Content
	mentioned := false
	for _, m := range ev.Mentions {
		if m.ID == p.botOpenID {
			mentioned = true
			// content is pre-rendered: strip both the placeholder and "@BotName"
			if m.Key != "" {
				text = strings.ReplaceAll(text, m.Key, "")
			}
			if m.Name != "" {
				text = strings.ReplaceAll(text, "@"+m.Name, "")
			}
		}
	}
	if isGroup && !mentioned {
		return hub.Inbound{}, false
	}
	text = strings.TrimSpace(mentionKeyRe.ReplaceAllString(text, ""))
	return hub.Inbound{Platform: "feishu", ChatID: ev.ChatID, MessageID: ev.MessageID,
		SenderID: ev.SenderID, Text: text, IsGroup: isGroup}, true
}

// LookupUser implements identity.Directory via the contact API. Without the
// contact:user.base:readonly scope Feishu returns ids only (no name).
func (p *Platform) LookupUser(ctx context.Context, id string) (identity.User, error) {
	out, err := p.api(ctx, "GET", "/open-apis/contact/v3/users/"+id, `{"user_id_type":"open_id"}`, "")
	if err != nil {
		return identity.User{}, err
	}
	var r struct {
		Data struct {
			User struct {
				Name    string `json:"name"`
				UnionID string `json:"union_id"`
				Email   string `json:"enterprise_email"`
				Email2  string `json:"email"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return identity.User{}, err
	}
	u := r.Data.User
	email := u.Email
	if email == "" {
		email = u.Email2
	}
	return identity.User{Platform: "feishu", ID: id, Name: u.Name, UnionID: u.UnionID, Email: email}, nil
}

// DefaultOwners returns the app's owner (in this app's open_id space), so a
// bot is owned by whoever owns the Feishu app unless bot.owners says otherwise.
func (p *Platform) DefaultOwners(ctx context.Context) []string {
	appID, err := p.appID(ctx)
	if err != nil {
		slog.Warn("lark-cli: cannot resolve app id", "err", err)
		return nil
	}
	out, err := p.api(ctx, "GET", "/open-apis/application/v6/applications/"+appID, `{"lang":"zh_cn","user_id_type":"open_id"}`, "")
	if err != nil {
		slog.Warn("lark-cli: cannot read app owner", "err", err)
		return nil
	}
	var r struct {
		Data struct {
			App struct {
				CreatorID string `json:"creator_id"`
				Owner     struct {
					OwnerID string `json:"owner_id"`
				} `json:"owner"`
			} `json:"app"`
		} `json:"data"`
	}
	_ = json.Unmarshal(out, &r)
	if id := r.Data.App.Owner.OwnerID; id != "" {
		return []string{id}
	}
	if id := r.Data.App.CreatorID; id != "" {
		return []string{id}
	}
	return nil
}

func (p *Platform) appID(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p.cfg.Bin, "config", "show", "--profile", p.cfg.Profile).Output()
	if err != nil {
		return "", err
	}
	var c struct {
		AppID string `json:"appId"`
	}
	if i := bytes.IndexByte(out, '{'); i >= 0 {
		if j := bytes.LastIndexByte(out, '}'); j > i {
			_ = json.Unmarshal(out[i:j+1], &c)
		}
	}
	if c.AppID == "" {
		return "", fmt.Errorf("no appId in lark-cli config show")
	}
	return c.AppID, nil
}

func (p *Platform) Send(ctx context.Context, chatID, text string) error {
	card, _ := json.Marshal(map[string]any{
		"config":   map[string]any{"wide_screen_mode": true},
		"elements": []any{map[string]any{"tag": "markdown", "content": text}},
	})
	body, _ := json.Marshal(map[string]any{"receive_id": chatID, "msg_type": "interactive", "content": string(card)})
	_, err := p.api(ctx, "POST", "/open-apis/im/v1/messages", `{"receive_id_type":"chat_id"}`, string(body))
	return err
}

func (p *Platform) Ack(ctx context.Context, messageID string) {
	if p.cfg.Reaction == "none" {
		return
	}
	body := fmt.Sprintf(`{"reaction_type":{"emoji_type":%q}}`, p.cfg.Reaction)
	if _, err := p.api(ctx, "POST", "/open-apis/im/v1/messages/"+messageID+"/reactions", "", body); err != nil {
		slog.Debug("lark-cli reaction failed", "err", err)
	}
}

// api runs `lark-cli api` as the bot and returns stdout; a non-ok JSON
// response is turned into an error.
func (p *Platform) api(ctx context.Context, method, path, params, data string) ([]byte, error) {
	args := []string{"api", method, path, "--profile", p.cfg.Profile, "--as", "bot"}
	if params != "" {
		args = append(args, "--params", params)
	}
	if data != "" {
		args = append(args, "--data", "-")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.cfg.Bin, args...)
	if data != "" {
		cmd.Stdin = strings.NewReader(data)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("lark-cli api %s %s: %v: %s %s", method, path, err, tail(string(out), 300), tail(stderr.String(), 300))
	}
	var r struct {
		OK    *bool           `json:"ok"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(out, &r) == nil && r.OK != nil && !*r.OK {
		return out, fmt.Errorf("lark-cli api %s %s: %s", method, path, tail(string(out), 400))
	}
	return out, nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
