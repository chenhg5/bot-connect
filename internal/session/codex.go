package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type CodexStore struct{ home string } // $CODEX_HOME or ~/.codex

func NewCodexStore(home string) *CodexStore {
	if home == "" {
		home = os.Getenv("CODEX_HOME")
	}
	if home == "" {
		home = filepath.Join(homeDir(), ".codex")
	}
	return &CodexStore{home: home}
}

func (s *CodexStore) Agent() string { return "codex" }

func (s *CodexStore) files() []fileStat {
	var files []fileStat
	_ = filepath.Walk(filepath.Join(s.home, "sessions"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".jsonl") {
			files = append(files, fileStat{p, info.ModTime()})
		}
		return nil
	})
	newestFirst(files)
	return files
}

// titles reads the names Codex shows in its own session picker.
func (s *CodexStore) titles() map[string]string {
	t := map[string]string{}
	_ = eachLine(filepath.Join(s.home, "session_index.jsonl"), func(b []byte) {
		var e struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(b, &e) == nil && e.ID != "" && strings.TrimSpace(e.ThreadName) != "" {
			t[e.ID] = e.ThreadName
		}
	})
	return t
}

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexItem struct {
	ID      string          `json:"id"`
	Cwd     string          `json:"cwd"`
	Source  json.RawMessage `json:"source"`
	Role    string          `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func (s *CodexStore) List(limit int, keep func(string) bool) ([]Info, error) {
	titles := s.titles()
	var out []Info
	for _, f := range s.files() {
		in, ok := s.parse(f.path, f.mod)
		if !ok || (keep != nil && !keep(in.Dir)) {
			continue
		}
		if t := titles[in.ID]; t != "" {
			in.Title = clip(t, 60)
		}
		out = append(out, in)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *CodexStore) Find(id string) (Info, error) {
	if id == "" {
		return Info{}, fmt.Errorf("empty id")
	}
	for _, f := range s.files() {
		if strings.Contains(filepath.Base(f.path), id) {
			if in, ok := s.parse(f.path, f.mod); ok {
				if t := s.titles()[in.ID]; t != "" {
					in.Title = clip(t, 60)
				}
				return in, nil
			}
		}
	}
	return Info{}, fmt.Errorf("no codex session %q", id)
}

func (s *CodexStore) parse(path string, mod time.Time) (Info, bool) {
	in := Info{Agent: "codex", Path: path, Updated: mod}
	var title string
	subagent := false
	_ = eachLine(path, func(b []byte) {
		var l codexLine
		if json.Unmarshal(b, &l) != nil {
			return
		}
		var it codexItem
		if json.Unmarshal(l.Payload, &it) != nil {
			return
		}
		switch l.Type {
		case "session_meta":
			if in.ID == "" {
				in.ID, in.Dir = it.ID, it.Cwd
				var src map[string]json.RawMessage
				if json.Unmarshal(it.Source, &src) == nil {
					_, subagent = src["subagent"]
				}
			}
		case "response_item":
			for _, c := range it.Content {
				switch {
				case it.Role == "user" && c.Type == "input_text" && isPrompt(c.Text):
					in.Messages++
					if title == "" {
						title = c.Text
					}
					in.Last = clip(c.Text, 160)
				case it.Role == "assistant" && c.Type == "output_text" && c.Text != "":
					in.Messages++
					in.Last = clip(c.Text, 160)
				}
			}
		}
	})
	if in.ID == "" || subagent || in.Messages == 0 {
		return in, false
	}
	in.Title = clip(title, 60)
	return in, true
}

func (s *CodexStore) History(id string, limit int) ([]Entry, error) {
	in, err := s.Find(id)
	if err != nil {
		return nil, err
	}
	var out []Entry
	err = eachLine(in.Path, func(b []byte) {
		var l codexLine
		if json.Unmarshal(b, &l) != nil || l.Type != "response_item" {
			return
		}
		var it codexItem
		if json.Unmarshal(l.Payload, &it) != nil {
			return
		}
		ts, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
		for _, c := range it.Content {
			if (it.Role == "user" && c.Type == "input_text" && isPrompt(c.Text)) ||
				(it.Role == "assistant" && c.Type == "output_text" && c.Text != "") {
				out = append(out, Entry{Role: it.Role, Text: c.Text, Time: ts.Local()})
			}
		}
	})
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, err
}
