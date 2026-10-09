package brain

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/chenhg5/bot-connect/internal/config"
)

// Adapter drives one kind of brain agent. The framework hands it a Request
// for each turn and takes back the reply; everything agent-specific (CLI
// flags, how tools and prompts are injected, how sessions resume) lives in
// the adapter. Register new ones with Register.
type Adapter interface {
	Name() string
	Caps() Caps
	Run(ctx context.Context, req Request) (Response, error)
}

// Caps tells the framework what the agent handles natively; whatever it
// doesn't, the framework compensates for in the prompt.
type Caps struct {
	// Sessions: the agent keeps its own conversation memory and can resume a
	// session id. Otherwise the framework inlines recent history every turn.
	Sessions bool
	// SystemPrompt: Request.SystemPrompt reaches the agent as a system
	// prompt. Otherwise the framework inlines it into the prompt.
	SystemPrompt bool
}

type Request struct {
	SessionID    string // session to resume; "" starts a new one
	SystemPrompt string // framework protocol + the user's / default persona
	Prompt       string // this turn: context block + new messages
	Tools        ToolAccess
	Env          []string // extra KEY=VALUE for the agent process
}

// ToolAccess is how the agent reaches tools during this turn.
type ToolAccess struct {
	MCPURL string             // bot-connect's MCP endpoint for this turn
	APIURL string             // same tools over REST, for `bot-connect tool` (env BOT_CONNECT_API)
	Extra  []config.MCPServer // the user's own MCP servers for the brain
}

type Response struct {
	Text      string // the reply to send
	SessionID string // session to resume next turn (if Caps.Sessions)
}

// Factory builds an adapter from the [brain] config.
type Factory func(cfg config.Brain) (Adapter, error)

var (
	regMu    sync.Mutex
	registry = map[string]Factory{}
)

// Register makes an adapter available as brain.agent = name.
func Register(name string, f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	registry[name] = f
}

func newAdapter(cfg config.Brain) (Adapter, error) {
	regMu.Lock()
	f := registry[cfg.Agent]
	var names []string
	for n := range registry {
		names = append(names, n)
	}
	regMu.Unlock()
	if f == nil {
		sort.Strings(names)
		return nil, fmt.Errorf("unknown brain agent %q (available: %v)", cfg.Agent, names)
	}
	return f(cfg)
}
