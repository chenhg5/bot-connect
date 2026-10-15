package observe

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// The toolbox: small scripts that make a repeated way of looking (or of
// acting) one call — written by an agent, registered by the bot, approved
// by the principal for exactly the code they saw. Changing the code needs
// approval again. Look tools can be probed and watched; act tools go
// through act like any action.

type ToolKind string

const (
	ToolLook ToolKind = "look" // reads and reports; used by look / watch
	ToolAct  ToolKind = "act"  // changes something; used by act
)

// Interpreters a tool may run with.
var Interpreters = map[string]string{"sh": ".sh", "bash": ".sh", "python3": ".py"}

// ScriptLimit bounds a tool's code.
const ScriptLimit = 32 << 10

var toolName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)

// Tool is one script in the toolbox.
type Tool struct {
	Name        string    `json:"name"`
	Description string    `json:"description"` // what it does, its arguments, what it prints
	Kind        ToolKind  `json:"kind"`
	Interpreter string    `json:"interpreter"`
	Code        string    `json:"code"`
	Hash        string    `json:"hash"`
	Approved    string    `json:"approved,omitempty"` // the hash the principal approved
	ApprovedAt  time.Time `json:"approved_at,omitempty"`
	Project     ProjectID `json:"project,omitempty"` // what it was made for (it may serve others)
	By          Actor     `json:"by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Retired     bool      `json:"retired,omitempty"`
	Uses        int       `json:"uses,omitempty"`
	LastUsed    time.Time `json:"last_used,omitempty"`
}

func CodeHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:8])
}

// NewTool registers code; it can't run until approved.
func NewTool(name, desc string, kind ToolKind, interp, code string, pid ProjectID, now time.Time, by Actor) (Tool, error) {
	t := Tool{Name: name, Project: pid, By: by, CreatedAt: now}
	return t, t.Revise(desc, kind, interp, code, now)
}

// Revise replaces the code (or description); new code needs approval again.
func (t *Tool) Revise(desc string, kind ToolKind, interp, code string, now time.Time) error {
	if !toolName.MatchString(t.Name) {
		return Invalid("tool name: lowercase letters, digits and dashes (e.g. pr-backlog)")
	}
	if strings.TrimSpace(desc) == "" {
		return Invalid("describe the tool: what it does, its arguments, what it prints")
	}
	if kind != ToolLook && kind != ToolAct {
		return Invalid("tool kind: look (reads) or act (changes something)")
	}
	if _, ok := Interpreters[interp]; !ok {
		return Invalid("interpreter: sh, bash or python3")
	}
	if strings.TrimSpace(code) == "" || len(code) > ScriptLimit {
		return Invalid("code: 1 to %d bytes", ScriptLimit)
	}
	t.Description, t.Kind, t.Interpreter, t.Code = strings.TrimSpace(desc), kind, interp, code
	t.Hash, t.UpdatedAt, t.Retired = CodeHash(code), now, false
	return nil
}

// Runnable: approved for exactly this code and not retired.
func (t Tool) Runnable() bool { return !t.Retired && t.Approved != "" && t.Approved == t.Hash }

// Approve marks this code as approved by the principal.
func (t *Tool) Approve(hash string, now time.Time) error {
	if hash != "" && hash != t.Hash {
		return Conflict("the code changed since (now %s): look at it again before approving", t.Hash)
	}
	t.Approved, t.ApprovedAt = t.Hash, now
	return nil
}

func (t Tool) Status() string {
	switch {
	case t.Retired:
		return "retired"
	case t.Runnable():
		return "approved"
	case t.Approved != "":
		return "changed, needs approval again"
	}
	return "waiting for approval"
}
