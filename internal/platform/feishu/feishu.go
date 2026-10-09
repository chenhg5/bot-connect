// Package feishu connects the hub to Feishu/Lark via the WebSocket long
// connection (no public callback URL needed).
package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/hub"
	"github.com/chenhg5/bot-connect/internal/identity"
)

type Platform struct {
	cfg       config.Feishu
	client    *lark.Client
	botOpenID string
}

func New(cfg config.Feishu) *Platform {
	var opts []lark.ClientOptionFunc
	if cfg.Domain != "" {
		opts = append(opts, lark.WithOpenBaseUrl(cfg.Domain))
	}
	return &Platform{cfg: cfg, client: lark.NewClient(cfg.AppID, cfg.AppSecret, opts...)}
}

func (p *Platform) Name() string { return "feishu" }

func (p *Platform) Start(ctx context.Context, onMessage func(hub.Inbound)) error {
	id, err := p.fetchBotOpenID(ctx)
	if err != nil {
		return fmt.Errorf("fetch bot info (check app_id/app_secret and that the bot capability is enabled): %w", err)
	}
	p.botOpenID = id
	slog.Info("feishu bot ready", "bot_open_id", id)

	handler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, ev *larkim.P2MessageReceiveV1) error {
			if in, ok := p.parse(ev); ok {
				onMessage(in)
			}
			return nil
		}).
		OnP2MessageReadV1(func(ctx context.Context, ev *larkim.P2MessageReadV1) error { return nil }).
		OnP2MessageReactionCreatedV1(func(ctx context.Context, ev *larkim.P2MessageReactionCreatedV1) error { return nil })

	wsOpts := []larkws.ClientOption{larkws.WithEventHandler(handler), larkws.WithLogLevel(larkcore.LogLevelInfo)}
	if p.cfg.Domain != "" {
		wsOpts = append(wsOpts, larkws.WithDomain(p.cfg.Domain))
	}
	ws := larkws.NewClient(p.cfg.AppID, p.cfg.AppSecret, wsOpts...)
	go func() {
		if err := ws.Start(ctx); err != nil {
			slog.Error("feishu websocket stopped", "err", err)
		}
	}()
	return nil
}

var mentionRe = regexp.MustCompile(`@_user_\d+\s*`)

func (p *Platform) parse(ev *larkim.P2MessageReceiveV1) (hub.Inbound, bool) {
	if ev.Event == nil || ev.Event.Message == nil || ev.Event.Sender == nil {
		return hub.Inbound{}, false
	}
	m, s := ev.Event.Message, ev.Event.Sender
	if deref(s.SenderType) == "app" {
		return hub.Inbound{}, false
	}
	isGroup := deref(m.ChatType) == "group"
	if isGroup && !p.mentioned(m.Mentions) {
		return hub.Inbound{}, false
	}
	senderID, unionID := "", ""
	if s.SenderId != nil {
		senderID, unionID = deref(s.SenderId.OpenId), deref(s.SenderId.UnionId)
	}
	text := extractText(deref(m.MessageType), deref(m.Content))
	text = strings.TrimSpace(mentionRe.ReplaceAllString(text, ""))
	return hub.Inbound{Platform: "feishu", ChatID: deref(m.ChatId), MessageID: deref(m.MessageId),
		SenderID: senderID, UnionID: unionID, Text: text, IsGroup: isGroup}, true
}

func (p *Platform) mentioned(ms []*larkim.MentionEvent) bool {
	for _, m := range ms {
		if m.Id != nil && deref(m.Id.OpenId) == p.botOpenID {
			return true
		}
	}
	return false
}

func extractText(msgType, content string) string {
	switch msgType {
	case "text":
		var c struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal([]byte(content), &c)
		return c.Text
	case "post":
		// {"title":"..","content":[[{"tag":"text","text":".."},...],...]}
		var c struct {
			Title   string `json:"title"`
			Content [][]struct {
				Tag  string `json:"tag"`
				Text string `json:"text"`
				Href string `json:"href"`
			} `json:"content"`
		}
		_ = json.Unmarshal([]byte(content), &c)
		var lines []string
		if c.Title != "" {
			lines = append(lines, c.Title)
		}
		for _, para := range c.Content {
			var sb strings.Builder
			for _, el := range para {
				sb.WriteString(el.Text)
				if el.Tag == "a" && el.Href != "" {
					sb.WriteString(" (" + el.Href + ")")
				}
			}
			lines = append(lines, sb.String())
		}
		return strings.Join(lines, "\n")
	}
	return fmt.Sprintf("[%s 消息，暂不支持解析]", msgType)
}

// Send posts a markdown card so replies render nicely.
func (p *Platform) Send(ctx context.Context, chatID, text string) error {
	card := map[string]any{
		"config":   map[string]any{"wide_screen_mode": true},
		"elements": []any{map[string]any{"tag": "markdown", "content": text}},
	}
	b, _ := json.Marshal(card)
	resp, err := p.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).MsgType(larkim.MsgTypeInteractive).Content(string(b)).Build()).
		Build())
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu send code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// Ack adds a reaction so the sender knows the message was received even if
// the turn is queued behind others.
func (p *Platform) Ack(ctx context.Context, messageID string) {
	if p.cfg.Reaction == "none" {
		return
	}
	emoji := p.cfg.Reaction
	resp, err := p.client.Im.MessageReaction.Create(ctx, larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(larkim.NewCreateMessageReactionReqBodyBuilder().ReactionType(&larkim.Emoji{EmojiType: &emoji}).Build()).
		Build())
	if err != nil || !resp.Success() {
		slog.Debug("feishu reaction failed", "err", err)
	}
}

// LookupUser implements identity.Directory via the contact API (needs the
// contact:user.base:readonly scope for names).
func (p *Platform) LookupUser(ctx context.Context, id string) (identity.User, error) {
	resp, err := p.client.Get(ctx, "/open-apis/contact/v3/users/"+id+"?user_id_type=open_id", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return identity.User{}, err
	}
	var r struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			User struct {
				Name    string `json:"name"`
				UnionID string `json:"union_id"`
				Email   string `json:"enterprise_email"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.RawBody, &r); err != nil {
		return identity.User{}, err
	}
	if r.Code != 0 {
		return identity.User{}, fmt.Errorf("contact api code=%d msg=%s", r.Code, r.Msg)
	}
	u := r.Data.User
	return identity.User{Platform: "feishu", ID: id, Name: u.Name, UnionID: u.UnionID, Email: u.Email}, nil
}

// DefaultOwners implements identity.OwnerSource: the Feishu app's owner
// (or creator), so the person who set the app up owns the bot by default.
func (p *Platform) DefaultOwners(ctx context.Context) []string {
	resp, err := p.client.Get(ctx, "/open-apis/application/v6/applications/"+p.cfg.AppID+"?lang=zh_cn&user_id_type=open_id",
		nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		slog.Warn("feishu: cannot read app owner", "err", err)
		return nil
	}
	var r struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			App struct {
				CreatorID string `json:"creator_id"`
				Owner     struct {
					OwnerID string `json:"owner_id"`
				} `json:"owner"`
			} `json:"app"`
		} `json:"data"`
	}
	if json.Unmarshal(resp.RawBody, &r) != nil || r.Code != 0 {
		slog.Warn("feishu: cannot read app owner", "code", r.Code, "msg", r.Msg)
		return nil
	}
	for _, id := range []string{r.Data.App.Owner.OwnerID, r.Data.App.CreatorID} {
		if id != "" {
			return []string{id}
		}
	}
	return nil
}

func (p *Platform) fetchBotOpenID(ctx context.Context) (string, error) {
	resp, err := p.client.Get(ctx, "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return "", err
	}
	var r struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(resp.RawBody, &r); err != nil {
		return "", err
	}
	if r.Code != 0 {
		return "", fmt.Errorf("code=%d msg=%s", r.Code, r.Msg)
	}
	return r.Bot.OpenID, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
