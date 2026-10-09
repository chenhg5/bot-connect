// Package session reads coding-agent sessions from the agents' own storage —
// the sessions you use in the terminal or IDE, not just ones bot-connect
// started. Claude Code: ~/.claude/projects/<key>/<id>.jsonl. Codex:
// $CODEX_HOME/sessions/**/rollout-*-<id>.jsonl. (Parsing follows cc-connect.)
package session

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

type Info struct {
	Agent    string    `json:"agent"` // claudecode | codex
	ID       string    `json:"id"`
	Dir      string    `json:"dir"`
	Title    string    `json:"title"`
	Last     string    `json:"last,omitempty"` // excerpt of the last message
	Messages int       `json:"messages"`
	Updated  time.Time `json:"updated"`
	Path     string    `json:"-"`
}

type Entry struct {
	Role string    `json:"role"` // user | assistant
	Text string    `json:"text"`
	Time time.Time `json:"time"`
}

// Store reads one agent's sessions.
type Store interface {
	Agent() string
	// List returns sessions newest first, at most limit, keeping only those
	// whose directory passes keep (nil = all).
	List(limit int, keep func(dir string) bool) ([]Info, error)
	Find(id string) (Info, error)
	History(id string, limit int) ([]Entry, error)
}

// Catalog merges the stores of all supported agents.
type Catalog struct{ stores []Store }

func NewCatalog(stores ...Store) *Catalog { return &Catalog{stores: stores} }

// Default returns a catalog over the local Claude Code and Codex stores.
func Default() *Catalog { return NewCatalog(NewClaudeStore(""), NewCodexStore("")) }

func (c *Catalog) List(agent string, limit int, keep func(string) bool) ([]Info, error) {
	var all []Info
	for _, s := range c.stores {
		if agent != "" && s.Agent() != agent {
			continue
		}
		l, err := s.List(limit, keep)
		if err != nil {
			continue
		}
		all = append(all, l...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Updated.After(all[j].Updated) })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (c *Catalog) Find(id string) (Info, error) {
	for _, s := range c.stores {
		if in, err := s.Find(id); err == nil {
			return in, nil
		}
	}
	return Info{}, fmt.Errorf("no session %q", id)
}

func (c *Catalog) History(agent, id string, limit int) ([]Entry, error) {
	for _, s := range c.stores {
		if agent == "" || s.Agent() == agent {
			if h, err := s.History(id, limit); err == nil {
				return h, nil
			}
		}
	}
	return nil, fmt.Errorf("no session %q", id)
}

// ---- helpers ----

type fileStat struct {
	path string
	mod  time.Time
}

func newestFirst(files []fileStat) {
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
}

// eachLine calls fn for every line of a JSONL file, however long.
func eachLine(path string, fn func([]byte)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 256*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			fn(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// isPrompt filters out injected context (XML-ish blocks, AGENTS.md, command echoes).
func isPrompt(text string) bool {
	t := strings.TrimSpace(text)
	return t != "" && !strings.HasPrefix(t, "<") && !strings.HasPrefix(t, "# AGENTS.md") &&
		!strings.HasPrefix(t, "Caveat: The messages below")
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// InDirs returns a keep func accepting dirs under any of the prefixes and
// rejecting those under any of the excluded prefixes. Empty dirs = all.
func InDirs(dirs, exclude []string) func(string) bool {
	under := func(d, p string) bool {
		p = strings.TrimRight(p, "/")
		return d == p || strings.HasPrefix(d, p+"/")
	}
	return func(d string) bool {
		for _, x := range exclude {
			if under(d, x) {
				return false
			}
		}
		if len(dirs) == 0 {
			return true
		}
		for _, p := range dirs {
			if under(d, p) {
				return true
			}
		}
		return false
	}
}
