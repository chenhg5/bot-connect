// Package brain runs the front agent. bot-connect has no model loop: the
// brain is an existing agent driven through an Adapter (Claude Code, Codex,
// any CLI, or one you register). Per turn the framework gives it
//
//   - tools: bot-connect's MCP server / CLI, bound to this turn's
//     conversation and caller role, plus the user's own MCP servers;
//   - a system prompt: the user's persona + the framework protocol;
//   - live context: senders with roles, worker status, open tasks.
//
// and takes back the reply. What the brain is, says and may use is
// configuration; the contract around it is the framework's.
package brain

import (
	"context"
	"fmt"
	"github.com/chenhg5/bot-connect/internal/app"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/hub"
	"github.com/chenhg5/bot-connect/internal/tools"
	"github.com/chenhg5/bot-connect/internal/toolserver"
	"github.com/chenhg5/bot-connect/internal/worker"
)

type Brain struct {
	cfg       config.Brain
	adapter   Adapter
	hub       *hub.Hub
	workers   *worker.Manager
	server    *toolserver.Server
	allow     map[string]bool
	toolNames []string
	scope     worker.Scope
	botName   string
	ownerName string
	pm        *app.App
}

// now is the brain's clock: the PM core's (virtual in simulations), else real time.
func (b *Brain) now() time.Time {
	if b.pm != nil {
		return b.pm.Clock.Now()
	}
	return time.Now()
}

// SetPM gives the brain the project-management core: its briefing goes into
// every privileged turn, a worker's own assignments into theirs.
func (b *Brain) SetPM(a *app.App) { b.pm = a }

func New(cfg config.Brain, h *hub.Hub, w *worker.Manager, srv *toolserver.Server, toolNames []string, scope worker.Scope, botName, ownerName string) (*Brain, error) {
	if err := os.MkdirAll(cfg.WorkDir, 0o700); err != nil {
		return nil, err
	}
	ad, err := newAdapter(cfg)
	if err != nil {
		return nil, err
	}
	var allow map[string]bool
	if len(cfg.Tools) > 0 {
		known := map[string]bool{}
		for _, n := range toolNames {
			known[n] = true
		}
		allow = map[string]bool{}
		for _, n := range cfg.Tools {
			if !known[n] {
				return nil, &unknownToolError{n, toolNames}
			}
			allow[n] = true
		}
	}
	names := toolNames
	if len(allow) > 0 {
		names = cfg.Tools
	}
	return &Brain{cfg: cfg, adapter: ad, hub: h, workers: w, server: srv, allow: allow, toolNames: names, scope: scope, botName: botName, ownerName: ownerName}, nil
}

// sessionKind identifies the settings a brain session was created under.
func (b *Brain) sessionKind() string {
	iso := b.cfg.Isolate == nil || *b.cfg.Isolate
	return fmt.Sprintf("%s|isolate=%v", b.adapter.Name(), iso)
}

type unknownToolError struct {
	name  string
	known []string
}

func (e *unknownToolError) Error() string {
	return "brain.tools: unknown tool " + e.name + " (known: " + strings.Join(e.known, ", ") + ")"
}

func (b *Brain) HandleTurn(ctx context.Context, t hub.Turn) (string, error) {
	tc := tools.TurnContext{ConvKey: t.Conv.Key, Platform: t.Conv.Platform, Caller: t.Caller, Allow: b.allow}
	for _, it := range t.Items {
		tc.Focus = append(tc.Focus, it.SignalIDs...)
	}
	token, done := b.server.Open(tc)
	defer done()

	caps := b.adapter.Caps()
	kind := b.sessionKind()
	// A session (and the chat history) created under different isolation
	// settings may hold what the brain must no longer see: start clean.
	if prev := b.hub.BrainKind(t.Conv); prev != "" && prev != kind {
		slog.Warn("brain settings changed; dropping old brain session and history", "conv", t.Conv.Key, "was", prev, "now", kind)
		b.hub.ResetConversation(t.Conv)
	}
	sid := ""
	if caps.Sessions {
		sid = b.hub.BrainSession(t.Conv, kind)
	}
	run := func(sid string) (Response, error) {
		fresh := sid == ""
		return b.adapter.Run(ctx, Request{
			SessionID:    sid,
			SystemPrompt: b.systemPrompt(),
			Prompt:       b.turnPrompt(t, !caps.SystemPrompt && (fresh || !caps.Sessions), fresh || !caps.Sessions),
			Tools:        ToolAccess{MCPURL: b.server.MCPURL(token), APIURL: b.server.APIURL(token), Extra: b.cfg.MCPServers},
			Env:          b.cfg.Env,
			ToolNames:    b.toolNames,
		})
	}
	res, err := run(sid)
	if err != nil && sid != "" && ctx.Err() == nil {
		// The saved session may be gone or broken; start a fresh one once.
		slog.Warn("brain resume failed, starting fresh session", "conv", t.Conv.Key, "err", err)
		res, err = run("")
	}
	if err != nil {
		return "", err
	}
	if caps.Sessions && res.SessionID != "" {
		b.hub.SetBrainSession(t.Conv, kind, res.SessionID)
	}
	reply := strings.TrimSpace(res.Text)
	b.hub.AppendHistory(t.Conv, renderItems(t.Items), reply)
	return reply, nil
}
