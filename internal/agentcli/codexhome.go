package agentcli

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnsureCodexHome prepares an isolated CODEX_HOME at dir: none of the owner's
// ~/.codex config, global AGENTS.md, plugins or MCP servers — only the login,
// shared by symlink (not copied, so token refreshes stay in one place and
// never log the owner out).
func EnsureCodexHome(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	link := filepath.Join(dir, "auth.json")
	if _, err := os.Lstat(link); err == nil {
		return nil
	}
	src := os.Getenv("CODEX_HOME")
	if src == "" {
		h, _ := os.UserHomeDir()
		src = filepath.Join(h, ".codex")
	}
	auth := filepath.Join(src, "auth.json")
	if _, err := os.Stat(auth); err != nil {
		return fmt.Errorf("codex login not found at %s (run `codex login`)", auth)
	}
	return os.Symlink(auth, link)
}
