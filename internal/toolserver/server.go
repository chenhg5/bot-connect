// Package toolserver exposes the tool registry to brain processes on
// localhost, two ways:
//
//	POST /mcp/{token}               MCP (streamable HTTP, JSON responses)
//	GET  /api/{token}/tools         list tools  (for the `bot-connect tool` CLI)
//	POST /api/{token}/tools/{name}  call a tool with a JSON object body
//
// A token is minted per brain turn and bound to that turn's TurnContext, so a
// brain can only act for the conversation and privilege it was started with.
package toolserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/bot-connect/internal/audit"
	"github.com/chenhg5/bot-connect/internal/tools"
)

type Server struct {
	Audit  audit.Sink
	reg    *tools.Registry
	mu     sync.Mutex
	turns  map[string]tools.TurnContext
	base   string
	server *http.Server
}

func New(reg *tools.Registry) *Server {
	return &Server{reg: reg, turns: map[string]tools.TurnContext{}, Audit: audit.Nop{}}
}

// call runs a tool and records it.
func (s *Server) call(ctx context.Context, tc tools.TurnContext, via, name string, args map[string]any) (string, error) {
	start := time.Now()
	out, err := s.reg.Call(ctx, tc, name, args)
	slog.Info("tool call", "via", via, "conv", tc.ConvKey, "tool", name, "caller", tc.Caller.ID, "err", err)
	caller := tc.Caller
	e := audit.Event{Type: audit.ToolCall, Platform: tc.Platform, Conv: tc.ConvKey, User: &caller, Tool: name, Args: args,
		Text: audit.Clip(out, 1000), Duration: time.Since(start).Round(time.Millisecond).String(), Extra: map[string]any{"via": via}}
	if err != nil {
		e.Error = err.Error()
	}
	s.Audit.Record(e)
	return out, err
}

// Start listens on addr (e.g. "127.0.0.1:0" for a random port).
func (s *Server) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.base = "http://" + ln.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp/{token}", s.handleMCP)
	mux.HandleFunc("GET /api/{token}/tools", s.handleList)
	mux.HandleFunc("POST /api/{token}/tools/{name}", s.handleCall)
	s.server = &http.Server{Handler: mux}
	go func() {
		if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("tool server stopped", "err", err)
		}
	}()
	slog.Info("tool server listening", "addr", s.base)
	return nil
}

func (s *Server) Stop(ctx context.Context) { _ = s.server.Shutdown(ctx) }

// Open mints a token for one brain turn. Call the returned func when the turn ends.
func (s *Server) Open(tc tools.TurnContext) (token string, closeFn func()) {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token = hex.EncodeToString(b)
	s.mu.Lock()
	s.turns[token] = tc
	s.mu.Unlock()
	return token, func() {
		s.mu.Lock()
		delete(s.turns, token)
		s.mu.Unlock()
	}
}

func (s *Server) MCPURL(token string) string { return s.base + "/mcp/" + token }
func (s *Server) APIURL(token string) string { return s.base + "/api/" + token }

func (s *Server) turn(r *http.Request) (tools.TurnContext, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tc, ok := s.turns[r.PathValue("token")]
	return tc, ok
}

// ---------- REST (CLI) ----------

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	tc, ok := s.turn(r)
	if !ok {
		http.Error(w, "unknown or expired token", http.StatusUnauthorized)
		return
	}
	var out []map[string]any
	for _, t := range s.reg.List(tc) {
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Schema})
	}
	writeJSON(w, out)
}

func (s *Server) handleCall(w http.ResponseWriter, r *http.Request) {
	tc, ok := s.turn(r)
	if !ok {
		http.Error(w, "unknown or expired token", http.StatusUnauthorized)
		return
	}
	var args map[string]any
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &args); err != nil {
			http.Error(w, "body must be a JSON object: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	name := r.PathValue("name")
	if args == nil {
		args = map[string]any{}
	}
	if r.URL.Query().Get("dry_run") == "1" {
		if _, err := s.reg.Check(tc, name, args); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "error_type": tools.Kind(err)})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "dry_run": true, "tool": name, "args": args})
		return
	}
	out, err := s.call(r.Context(), tc, "cli", name, args)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "error_type": tools.Kind(err)})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "result": out})
}

// ---------- MCP ----------

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK)
		return
	default: // no server-initiated SSE stream
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	tc, ok := s.turn(r)
	if !ok {
		http.Error(w, "unknown or expired token", http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	var req rpcReq
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcErr{-32700, "parse error"}})
		return
	}
	if len(req.ID) == 0 { // notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rerr := s.dispatch(r.Context(), tc, req)
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rerr != nil {
		resp["error"] = rerr
	} else {
		resp["result"] = result
	}
	writeJSON(w, resp)
}

func (s *Server) dispatch(ctx context.Context, tc tools.TurnContext, req rpcReq) (any, *rpcErr) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		return map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "bot-connect", "version": "0.1.0"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		var list []map[string]any
		for _, t := range s.reg.List(tc) {
			list = append(list, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Schema})
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcErr{-32602, "invalid params"}
		}
		out, err := s.call(ctx, tc, "mcp", p.Name, p.Arguments)
		if err != nil {
			return map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}, nil
		}
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": out}}}, nil
	}
	return nil, &rpcErr{-32601, fmt.Sprintf("method not found: %s", req.Method)}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
