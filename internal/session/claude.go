package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type ClaudeStore struct{ base string } // ~/.claude/projects

func NewClaudeStore(base string) *ClaudeStore {
	if base == "" {
		base = filepath.Join(homeDir(), ".claude", "projects")
	}
	return &ClaudeStore{base: base}
}

func (s *ClaudeStore) Agent() string { return "claudecode" }

type claudeLine struct {
	Type        string `json:"type"`
	Cwd         string `json:"cwd"`
	SessionID   string `json:"sessionId"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	AITitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func (s *ClaudeStore) List(limit int, keep func(string) bool) ([]Info, error) {
	paths, _ := filepath.Glob(filepath.Join(s.base, "*", "*.jsonl"))
	var files []fileStat
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			files = append(files, fileStat{p, st.ModTime()})
		}
	}
	newestFirst(files)
	var out []Info
	for _, f := range files {
		in, ok := s.parse(f.path, f.mod)
		if !ok || (keep != nil && !keep(in.Dir)) {
			continue
		}
		out = append(out, in)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *ClaudeStore) Find(id string) (Info, error) {
	if id == "" {
		return Info{}, fmt.Errorf("empty id")
	}
	paths, _ := filepath.Glob(filepath.Join(s.base, "*", id+".jsonl"))
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			if in, ok := s.parse(p, st.ModTime()); ok {
				return in, nil
			}
		}
	}
	return Info{}, fmt.Errorf("no claude session %q", id)
}

func (s *ClaudeStore) parse(path string, mod time.Time) (Info, bool) {
	in := Info{Agent: "claudecode", Path: path, Updated: mod}
	var firstUser, aiTitle, customTitle string
	sidechainOnly := true
	_ = eachLine(path, func(b []byte) {
		var l claudeLine
		if json.Unmarshal(b, &l) != nil {
			return
		}
		if in.ID == "" && l.SessionID != "" {
			in.ID = l.SessionID
		}
		if in.Dir == "" && l.Cwd != "" {
			in.Dir = l.Cwd
		}
		switch l.Type {
		case "ai-title":
			aiTitle = l.AITitle
		case "custom-title":
			customTitle = l.CustomTitle
		case "user", "assistant":
			if l.IsSidechain {
				return
			}
			sidechainOnly = false
			text := claudeText(l.Message.Content)
			if l.IsMeta || !isPrompt(text) {
				return
			}
			in.Messages++
			if l.Type == "user" && firstUser == "" {
				firstUser = text
			}
			in.Last = clip(text, 160)
		}
	})
	if in.ID == "" {
		in.ID = filepath.Base(path[:len(path)-len(".jsonl")])
	}
	if sidechainOnly || in.Messages == 0 {
		return in, false
	}
	in.Title = clip(firstNonEmpty(customTitle, aiTitle, firstUser), 60)
	return in, true
}

func (s *ClaudeStore) History(id string, limit int) ([]Entry, error) {
	in, err := s.Find(id)
	if err != nil {
		return nil, err
	}
	var out []Entry
	err = eachLine(in.Path, func(b []byte) {
		var l claudeLine
		if json.Unmarshal(b, &l) != nil || (l.Type != "user" && l.Type != "assistant") || l.IsSidechain || l.IsMeta {
			return
		}
		text := claudeText(l.Message.Content)
		if !isPrompt(text) {
			return
		}
		ts, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
		out = append(out, Entry{Role: l.Type, Text: text, Time: ts.Local()})
	})
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, err
}

// claudeText returns the text of a message: a plain string or the first
// text block (tool calls / results are skipped).
func claudeText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			return b.Text
		}
	}
	return ""
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
