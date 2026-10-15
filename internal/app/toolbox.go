package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/chenhg5/bot-connect/internal/domain/observe"
	. "github.com/chenhg5/bot-connect/internal/domain/shared"
)

// AddTool registers a script in the toolbox (or revises one). It can't run
// until the principal approves this exact code.
func (a *App) AddTool(ctx context.Context, by Actor, pid ProjectID, name, desc string, kind observe.ToolKind, interp, code string) (observe.Tool, error) {
	if err := requirePrivileged(by, "add to the toolbox"); err != nil {
		return observe.Tool{}, err
	}
	var out observe.Tool
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		t, ok := s.Tools[name]
		var err error
		if ok {
			err = t.Revise(desc, kind, interp, code, a.now())
		} else {
			t, err = observe.NewTool(name, desc, kind, interp, code, pid, a.now(), by)
		}
		if err != nil {
			return nil, err
		}
		s.Tools[name] = t
		out = t
		return []Event{NewEvent("tool.added", "tool:"+name, a.now(), by, "hash", t.Hash, "kind", string(kind))}, nil
	})
	return out, err
}

// ApproveTool approves a tool's current code (hash: the version the
// principal looked at; empty = current).
func (a *App) ApproveTool(ctx context.Context, by Actor, name, hash string) (observe.Tool, error) {
	if by.Role != RolePrincipal {
		return observe.Tool{}, Forbidden("only the principal approves tools")
	}
	var out observe.Tool
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		t, ok := s.Tools[name]
		if !ok {
			return nil, NotFound("no tool %s", name)
		}
		if err := t.Approve(hash, a.now()); err != nil {
			return nil, err
		}
		s.Tools[name] = t
		out = t
		return []Event{NewEvent("tool.approved", "tool:"+name, a.now(), by, "hash", t.Hash)}, nil
	})
	return out, err
}

// RetireTool takes a tool out of use.
func (a *App) RetireTool(ctx context.Context, by Actor, name string) error {
	if err := requirePrivileged(by, "retire a tool"); err != nil {
		return err
	}
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		t, ok := s.Tools[name]
		if !ok {
			return nil, NotFound("no tool %s", name)
		}
		t.Retired = true
		s.Tools[name] = t
		return []Event{NewEvent("tool.retired", "tool:"+name, a.now(), by)}, nil
	})
	return err
}

// Tools lists the toolbox.
func (a *App) Tools() []observe.Tool {
	var out []observe.Tool
	a.Store.Read(func(s *State) {
		for _, t := range s.Tools {
			out = append(out, t)
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Tool returns one tool.
func (a *App) Tool(name string) (observe.Tool, bool) {
	var t observe.Tool
	var ok bool
	a.Store.Read(func(s *State) { t, ok = s.Tools[name] })
	return t, ok
}

// DescribeTool shows a tool with its code, for the principal to approve.
func DescribeTool(t observe.Tool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "工具 %s（%s，%s，版本 %s）：%s\n状态：%s；用过 %d 次\n", t.Name, t.Kind, t.Interpreter, t.Hash, t.Description, t.Status(), t.Uses)
	b.WriteString("代码：\n```\n" + t.Code + "\n```")
	return b.String()
}

// resolveTool turns a tool's name into its approved code, counting the use.
func (a *App) resolveTool(ctx context.Context, name string, want observe.ToolKind) (observe.Tool, error) {
	var t observe.Tool
	_, err := a.commit(ctx, func(s *State) ([]Event, error) {
		x, ok := s.Tools[name]
		if !ok {
			return nil, NotFound("no tool %s in the toolbox", name)
		}
		if x.Kind != want {
			return nil, Invalid("%s is a %s tool; use it with %s", name, x.Kind, map[observe.ToolKind]string{observe.ToolLook: "look / watch", observe.ToolAct: "act"}[x.Kind])
		}
		if !x.Runnable() {
			return nil, Forbidden("tool %s is %s: show the principal its code (toolbox show) and record their approval", name, x.Status())
		}
		x.Uses++
		x.LastUsed = a.now()
		s.Tools[name] = x
		t = x
		return nil, nil
	})
	return t, err
}

func (a *App) resolveProbe(ctx context.Context, p observe.Probe) (observe.Probe, error) {
	if p.Kind != observe.ToolProbe {
		return p, nil
	}
	t, err := a.resolveTool(ctx, p.Target, observe.ToolLook)
	if err != nil {
		return p, err
	}
	p.Interpreter, p.Code = t.Interpreter, t.Code
	return p, nil
}

func (a *App) resolveAction(ctx context.Context, act observe.Action) (observe.Action, error) {
	if act.Kind != observe.ActTool {
		return act, nil
	}
	t, err := a.resolveTool(ctx, act.Program, observe.ToolAct)
	if err != nil {
		return act, err
	}
	act.Interpreter, act.Code = t.Interpreter, t.Code
	return act, nil
}
