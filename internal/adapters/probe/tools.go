package probe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/chenhg5/bot-connect/internal/domain/observe"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// runScript runs a toolbox script: written once under ToolDir by its code
// hash, run with its interpreter in a directory inside the roots, with the
// same time, output and secret limits as everything else.
func (l *Local) runScript(ctx context.Context, interp, code string, args []string, dir string, timeout time.Duration) (string, error) {
	if code == "" {
		return "", Invalid("the tool isn't approved for its current code")
	}
	ext, ok := observe.Interpreters[interp]
	if !ok {
		return "", Invalid("interpreter %q not allowed", interp)
	}
	if l.ToolDir == "" {
		return "", Forbidden("no toolbox directory configured")
	}
	if err := os.MkdirAll(l.ToolDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(l.ToolDir, observe.CodeHash(code)+ext)
	if b, err := os.ReadFile(path); err != nil || string(b) != code {
		if err := os.WriteFile(path, []byte(code), 0o600); err != nil {
			return "", err
		}
	}
	bin, err := exec.LookPath(interp)
	if err != nil {
		return "", NotFound("%s is not installed", interp)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return l.exec(ctx, bin, append([]string{path}, args...), dir)
}
