package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/chenhg5/bot-connect/internal/audit"
	"github.com/chenhg5/bot-connect/internal/brain"
	"github.com/chenhg5/bot-connect/internal/cli"
	"github.com/chenhg5/bot-connect/internal/config"
	"github.com/chenhg5/bot-connect/internal/hub"
	"github.com/chenhg5/bot-connect/internal/identity"
	"github.com/chenhg5/bot-connect/internal/platform/console"
	"github.com/chenhg5/bot-connect/internal/platform/feishu"
	"github.com/chenhg5/bot-connect/internal/platform/larkcli"
	"github.com/chenhg5/bot-connect/internal/schedule"
	"github.com/chenhg5/bot-connect/internal/session"
	"github.com/chenhg5/bot-connect/internal/tools"
	"github.com/chenhg5/bot-connect/internal/toolserver"
	"github.com/chenhg5/bot-connect/internal/worker"
)

// Set at build time: -ldflags "-X main.version=… -X main.commit=… -X main.buildTime=…"
var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	os.Exit(newApp().Main(os.Args[1:]))
}

// runningBot is one bot's slice of the process.
type runningBot struct {
	cfg    config.BotConfig
	hub    *hub.Hub
	srv    *toolserver.Server
	policy *identity.StaticPolicy
}

type serveOpts struct {
	Console    bool     // chat from this terminal
	ConsoleBot string   // which bot the terminal talks to (default: first)
	Only       []string // run only these bots (default: all)
}

// serve runs the configured bots until interrupted.
func serve(cfg *config.Config, o serveOpts) error {
	useConsole, consoleBot := o.Console, o.ConsoleBot
	if len(o.Only) > 0 {
		keep := map[string]bool{}
		for _, n := range o.Only {
			keep[n] = true
		}
		var bots []config.BotConfig
		for _, b := range cfg.Bots {
			if keep[b.Name] {
				bots = append(bots, b)
				delete(keep, b.Name)
			}
		}
		for n := range keep {
			return cli.NotFound(fmt.Sprintf("no bot named %q", n), "run: bot-connect bot list")
		}
		cfg.Bots = bots
	}
	var err error
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(cfg.DataDir, "bot-connect.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	logOut := os.Stderr
	if useConsole {
		logOut = logFile // keep the chat readable; logs go to the file
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// Shared by all bots: the worker pool (one queue per session), the
	// session catalog, and the audit log.
	auditLog, err := audit.NewJSONL(filepath.Join(cfg.DataDir, "audit"))
	if err != nil {
		return err
	}
	defer auditLog.Close()
	workers, err := worker.NewManager(cfg.Workers, cfg.DataDir)
	if err != nil {
		return err
	}
	// The owner's sessions, plus those of isolated Codex workers.
	sessions := session.NewCatalog(session.NewClaudeStore(""), session.NewCodexStore(""), session.NewCodexStore(workers.CodexHome()))
	workers.Audit, workers.Sessions, workers.BusyWindow = auditLog, sessions, 90*time.Second
	if !worker.SandboxSupported() {
		for _, w := range cfg.Workers {
			if w.Agent == "claudecode" && w.Confine != nil && *w.Confine {
				slog.Warn("Claude Code has no OS sandbox on this platform: confine is not enforced; "+
					"deny_read only covers its file tools, not shell commands", "worker", w.Name)
			}
		}
	}

	// Scheduled jobs: shared store, delivered into the conversation (and bot)
	// that created them.
	jobs, err := schedule.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	if consoleBot == "" {
		consoleBot = cfg.Bots[0].Name
	}
	bots := map[string]*runningBot{}
	for _, bc := range cfg.Bots {
		withConsole := useConsole && bc.Name == consoleBot
		rb, err := setupBot(cfg, bc, workers, sessions, jobs, auditLog, withConsole)
		if err != nil {
			return fmt.Errorf("bot %s: %w", bc.Name, err)
		}
		bots[bc.Name] = rb
	}
	if useConsole && bots[consoleBot] == nil {
		return cli.NotFound(fmt.Sprintf("no bot named %q", consoleBot), "run: bot-connect bot list")
	}
	// Task results go back to the bot (and conversation) that asked.
	workers.OnFinish = func(t worker.Task) {
		rb := bots[t.Bot]
		if rb == nil && len(cfg.Bots) == 1 {
			rb = bots[cfg.Bots[0].Name] // tasks from before multi-bot
		}
		if rb != nil {
			rb.hub.PostTaskEvent(t)
		}
	}

	jobs.Authorize = func(j schedule.Job) bool {
		rb := bots[j.Bot]
		return rb != nil && rb.policy.Role(j.CreatedBy).Privileged()
	}
	jobs.Fire = func(j schedule.Job) bool {
		rb := bots[j.Bot]
		return rb != nil && rb.hub.PostScheduled(j.ConvKey, j.ID, firstNonEmpty(j.Description, j.Spec), j.Prompt, j.CreatedBy)
	}
	jobs.OnEvent = func(j schedule.Job, event string) {
		u := j.CreatedBy
		auditLog.Record(audit.Event{Type: audit.Schedule, Bot: j.Bot, Conv: j.ConvKey, User: &u, Status: event,
			Text: audit.Clip(j.Prompt, 500), Extra: map[string]any{"schedule": j.ID, "spec": j.Spec}})
		slog.Info("schedule", "id", j.ID, "event", event, "bot", j.Bot)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workers.Start(ctx)
	go jobs.Run(ctx.Done(), 15*time.Second)
	for _, bc := range cfg.Bots {
		if err := bots[bc.Name].hub.Start(ctx); err != nil {
			return fmt.Errorf("bot %s: %w", bc.Name, err)
		}
		slog.Info("bot running", "bot", bc.Name, "brain", bc.Brain.Agent, "dir", bc.Dir, "version", func() string { v, _, _ := versionInfo(); return v }())
	}
	if useConsole {
		fmt.Printf("bot-connect console — bot: %s。你是 owner；用 \"@名字 消息\" 以访客身份说话。日志：%s/bot-connect.log\n\n", consoleBot, cfg.DataDir)
	}
	<-ctx.Done()
	slog.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, rb := range bots {
		rb.srv.Stop(sctx)
	}
	return nil
}

func setupBot(cfg *config.Config, bc config.BotConfig, workers *worker.Manager, sessions *session.Catalog, jobs *schedule.Store, sink audit.Sink, withConsole bool) (*runningBot, error) {
	if err := os.MkdirAll(bc.Dir, 0o700); err != nil {
		return nil, err
	}
	sink = audit.ForBot{Bot: bc.Name, Sink: sink}
	scope := worker.Scope{AskRoles: map[identity.Role]bool{}}
	for _, r := range bc.AskRoles {
		scope.AskRoles[identity.Role(r)] = true
	}
	if len(bc.Workers) > 0 {
		scope.Names = map[string]bool{}
		for _, n := range bc.Workers {
			scope.Names[n] = true
		}
	}
	owners := append([]string(nil), bc.Owners...)
	if withConsole {
		owners = append(owners, console.OwnerID)
	}
	policy := identity.NewStaticPolicy(owners, bc.Admins, bc.Members...)
	prefix := ""
	if len(cfg.Bots) > 1 {
		prefix = bc.Name + "/"
	}
	h := hub.New(hub.Options{
		KeyPrefix:     prefix,
		PerUser:       bc.Isolation == "per_user",
		Scope:         scope,
		Identity:      identity.NewResolver(policy),
		Policy:        policy,
		Audit:         sink,
		MaxConcurrent: bc.MaxConcurrentTurns,
		Debounce:      bc.Debounce.Duration,
		TurnTimeout:   bc.TurnTimeout.Duration,
		HistoryLimit:  bc.HistoryLimit,
		DataDir:       bc.Dir,
		ScheduleDone:  jobs.Done,
	}, workers)

	reg := tools.New(tools.Env{Bot: bc.Name, Workers: workers, Sessions: sessions, Messenger: h, Scope: scope, Schedules: jobs})
	srv := toolserver.New(reg)
	srv.Audit = sink
	if err := srv.Start(cfg.Server.Listen); err != nil {
		return nil, fmt.Errorf("tool server: %w", err)
	}
	name := bc.Name
	b, err := brain.New(bc.Brain, h, workers, srv, reg.Names(), scope, name, bc.OwnerName)
	if err != nil {
		return nil, err
	}
	h.SetBrain(b)
	switch {
	case bc.Brain.Agent == "command":
		slog.Warn("a command brain can read whatever its shell can; worker/session limits apply to bot-connect tools only — isolate the process if that matters", "bot", name)
	case bc.Brain.Agent == "codex" && !*bc.Brain.Confine:
		slog.Warn("codex brain without confine: its read-only sandbox can read the whole disk", "bot", name)
	}

	switch {
	case bc.Feishu.LarkCLIProfile != "":
		h.AddPlatform(larkcli.New(larkcli.Config{Profile: bc.Feishu.LarkCLIProfile, Reaction: bc.Feishu.Reaction}))
	case bc.Feishu.AppID != "":
		h.AddPlatform(feishu.New(bc.Feishu))
	case !withConsole:
		return nil, fmt.Errorf("no channel: set feishu.app_id / feishu.larkcli_profile (or run it with --console)")
	}
	if withConsole {
		h.AddPlatform(console.New())
	}
	return &runningBot{cfg: bc, hub: h, srv: srv, policy: policy}, nil
}

// versionInfo prefers values injected by the Makefile and falls back to the
// module build info, so `go install …@vX.Y.Z` builds report their version too.
func versionInfo() (v, c, t string) {
	v, c, t = version, commit, buildTime
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if v == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		v = bi.Main.Version
	}
	for _, s := range bi.Settings {
		switch {
		case s.Key == "vcs.revision" && c == "none" && len(s.Value) >= 7:
			c = s.Value[:7]
		case s.Key == "vcs.time" && t == "unknown":
			t = s.Value
		}
	}
	return
}

func firstNonEmpty(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}
