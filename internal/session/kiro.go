package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type KiroStore struct{ base string } // ~/.kiro/sessions

func NewKiroStore(base string) *KiroStore {
	if base == "" {
		base = filepath.Join(homeDir(), ".kiro", "sessions")
	}
	return &KiroStore{base: base}
}

func (s *KiroStore) Agent() string { return "kiro" }

// Kiro session format: each session is a directory with messages.jsonl
type kiroMessage struct {
	Type       string          `json:"type"`      // "user", "assistant", "session_start", etc.
	Role       string          `json:"role"`      // "user" or "assistant"
	Content    json.RawMessage `json:"content"`   // text content or content blocks
	Timestamp  string          `json:"timestamp"` // ISO 8601 timestamp
	SessionID  string          `json:"session_id"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
}

func (s *KiroStore) List(limit int, keep func(string) bool) ([]Info, error) {
	entries, err := os.ReadDir(s.base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []fileStat
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Each session is a directory named sess_<uuid>
		if !strings.HasPrefix(e.Name(), "sess_") {
			continue
		}
		sessionDir := filepath.Join(s.base, e.Name())
		messagesFile := filepath.Join(sessionDir, "messages.jsonl")
		if st, err := os.Stat(messagesFile); err == nil {
			files = append(files, fileStat{messagesFile, st.ModTime()})
		}
	}

	newestFirst(files)

	var out []Info
	for _, f := range files {
		in, ok := s.parse(f.path, f.mod)
		if !ok {
			continue
		}
		if keep != nil {
			// Extract workspace/dir from session metadata
			// For now, we'll check if it should be kept after parsing
			if in.Dir != "" && !keep(in.Dir) {
				continue
			}
		}
		out = append(out, in)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *KiroStore) Find(id string) (Info, error) {
	if id == "" {
		return Info{}, fmt.Errorf("empty id")
	}

	// Session directory is sess_<id> or just <id> if it already has sess_ prefix
	sessionID := id
	if !strings.HasPrefix(id, "sess_") {
		sessionID = "sess_" + id
	}

	sessionDir := filepath.Join(s.base, sessionID)
	messagesFile := filepath.Join(sessionDir, "messages.jsonl")

	if st, err := os.Stat(messagesFile); err == nil {
		if in, ok := s.parse(messagesFile, st.ModTime()); ok {
			return in, nil
		}
	}

	return Info{}, fmt.Errorf("no kiro session %q", id)
}

func (s *KiroStore) parse(path string, mod time.Time) (Info, bool) {
	in := Info{Agent: "kiro", Path: path, Updated: mod}

	// Extract session ID from path: .../sessions/sess_<uuid>/messages.jsonl
	dir := filepath.Dir(path)
	sessionName := filepath.Base(dir)
	if strings.HasPrefix(sessionName, "sess_") {
		in.ID = sessionName // Keep full sess_<uuid> format
	}

	var firstUser string
	var workspaceDir string

	_ = eachLine(path, func(b []byte) {
		var msg kiroMessage
		if json.Unmarshal(b, &msg) != nil {
			return
		}

		// Extract session ID if not yet set
		if in.ID == "" && msg.SessionID != "" {
			in.ID = msg.SessionID
		}

		// Try to extract workspace from session metadata
		// NOTE: Workspace detection is opportunistic and depends on session_start or
		// session_metadata events containing "workspace" or "cwd" fields. If these
		// events are missing or use a different structure, workspace filtering will
		// not work for this session. This is a known limitation of kiro-acp's current
		// session format.
		if msg.Type == "session_start" || msg.Type == "session_metadata" {
			// Parse metadata for workspace information
			var meta map[string]any
			if json.Unmarshal(b, &meta) == nil {
				if ws, ok := meta["workspace"].(string); ok {
					workspaceDir = ws
				} else if cwd, ok := meta["cwd"].(string); ok {
					workspaceDir = cwd
				}
			}
		}

		// Count user/assistant messages
		if msg.Role == "user" || msg.Role == "assistant" {
			text := kiroText(msg.Content)
			if text == "" || !isPrompt(text) {
				return
			}
			in.Messages++
			if msg.Role == "user" && firstUser == "" {
				firstUser = text
			}
			in.Last = clip(text, 160)
		}
	})

	if in.ID == "" {
		// Fallback: use directory name as ID
		in.ID = sessionName
	}

	if in.Messages == 0 {
		return in, false
	}

	in.Dir = workspaceDir
	in.Title = clip(firstUser, 60)
	return in, true
}

func (s *KiroStore) History(id string, limit int) ([]Entry, error) {
	in, err := s.Find(id)
	if err != nil {
		return nil, err
	}

	var out []Entry
	err = eachLine(in.Path, func(b []byte) {
		var msg kiroMessage
		if json.Unmarshal(b, &msg) != nil {
			return
		}

		if msg.Role != "user" && msg.Role != "assistant" {
			return
		}

		text := kiroText(msg.Content)
		if !isPrompt(text) {
			return
		}

		ts, _ := time.Parse(time.RFC3339Nano, msg.Timestamp)
		if ts.IsZero() {
			// Try alternate formats
			ts, _ = time.Parse(time.RFC3339, msg.Timestamp)
		}
		out = append(out, Entry{Role: msg.Role, Text: text, Time: ts.Local()})
	})

	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, err
}

// kiroText extracts text from Kiro message content, which can be:
// - A plain string
// - An array of content blocks with {"type": "text", "text": "..."}
func kiroText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try plain string first
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	// Try content blocks array
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}

	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
