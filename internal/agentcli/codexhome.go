package agentcli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// EnsureCodexHome prepares an isolated CODEX_HOME at dir: none of the owner's
// ~/.codex config, global AGENTS.md, plugins or MCP servers — only the login,
// shared with the owner's ~/.codex (symlink; hard link or copy where symlinks
// aren't allowed, e.g. Windows without developer mode).
func EnsureCodexHome(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	link := filepath.Join(dir, "auth.json")
	if _, err := os.Lstat(link); err == nil {
		return nil
	}
	src := ownerCodexAuth()
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("codex login not found at %s (run `codex login`)", src)
	}
	return shareFile(src, link)
}

// SyncCodexAuth runs after a Codex process used an isolated home. If Codex
// refreshed its tokens by replacing auth.json (so it is no longer shared),
// the newer login is copied back to the owner's ~/.codex — otherwise the
// owner could be left holding a revoked refresh token — and sharing is
// restored.
func SyncCodexAuth(dir string) {
	link := filepath.Join(dir, "auth.json")
	src := ownerCodexAuth()
	li, err := os.Lstat(link)
	if err != nil || li.Mode()&os.ModeSymlink != 0 {
		return
	}
	si, err := os.Stat(src)
	if err != nil || os.SameFile(li, si) {
		return // missing, or a hard link still shared
	}
	if li.ModTime().After(si.ModTime()) {
		if err := copyFile(link, src); err != nil {
			slog.Warn("codex: could not sync refreshed login back", "err", err)
			return
		}
		slog.Info("codex: synced refreshed login back to the owner's CODEX_HOME")
	}
	_ = os.Remove(link)
	_ = shareFile(src, link)
}

func ownerCodexAuth() string {
	src := os.Getenv("CODEX_HOME")
	if src == "" {
		h, _ := os.UserHomeDir()
		src = filepath.Join(h, ".codex")
	}
	return filepath.Join(src, "auth.json")
}

func shareFile(src, dst string) error {
	if err := os.Symlink(src, dst); err == nil {
		return nil
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
