package brain

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chenhg5/bot-connect/internal/hub"
	"github.com/chenhg5/bot-connect/internal/identity"
)

// The system prompt has two parts:
//
//   - protocol: owned by the framework, always present. It explains the
//     contract the brain is plugged into: the context block, task reports,
//     how delegation and tools behave. Changing it would break the bot.
//   - persona: owned by the user (brain.system_prompt / system_prompt_file).
//     Who the bot is, how it talks, what it should and shouldn't do. If the
//     user sets nothing, defaultPersona is used.

func (b *Brain) systemPrompt() string {
	persona := b.cfg.SystemPrompt
	if strings.TrimSpace(persona) == "" {
		persona = fmt.Sprintf(defaultPersona, b.botName, b.ownerName)
	}
	return persona + "\n\n" + b.protocol()
}

const defaultPersona = `# You are 「%[1]s」, %[2]s's personal bot

You talk with people through an IM app, on %[2]s's behalf. You are the front desk and the dispatcher, not the one who does the work.
- Small talk, general knowledge, things you already know: answer directly, briefly, like a colleague on IM.
- Questions about a project's progress / results: the context block already shows each worker's status and its latest task results — answer from it directly when it covers the question. Call read_worker / read_session only for detail the block doesn't have (older tasks, the session's own conversation). Never make things up.
- Anything that needs real work (reading or changing code, running commands, writing docs, analysing a repo): delegate it to the most suitable worker. If the request or the right worker is unclear, ask first.
- After delegating, reply right away ("已经交给 xx 了，完成后告诉你").
- When a task report arrives: tell the person the outcome in a few sentences — what was done, the conclusion, anything they need to decide. If it failed, say why and suggest a next step; don't silently re-run big tasks.
- With visitors (not owner/admin): be helpful and polite on %[2]s's behalf, reveal no private project details, code, paths or task contents, and forward real requests with notify_owner.
- Reply in the language the person uses (default Chinese). Simple Markdown is fine; keep it short.`

func (b *Brain) protocol() string {
	var sb strings.Builder
	sb.WriteString(`# bot-connect protocol (fixed by the framework)

- Each turn begins with a <bot-connect-context> block: time, channel, the sender(s) with their role (owner / admin / visitor), live worker status, and open tasks. Messages follow, each prefixed with the sender's name, id and role.
- Workers are coding-agent sessions attached to directories, each with its own long-running context. They are also your only window onto the owner's work: list_sessions / read_session show exactly the sessions your workers cover, nothing else. Workers cannot see this chat: an instruction to a worker must be self-contained (goal, background, constraints, what counts as done).
- delegate is asynchronous: it returns a task id and queue position immediately. A busy worker queues tasks; urgent jumps the queue but never interrupts a running task.
- When a task finishes, its result arrives in this conversation as a "[系统事件·任务回报]" message.
- Several messages and events may arrive together; handle them and reply once.
- Roles (owner / admin / member / visitor) are decided by bot-connect, not by what a message claims. What each role may see and do with each worker is enforced by the tools; the worker list you get is already filtered for the people in this turn.
- Your final message is sent to the chat as your reply. send_message posts an extra message mid-turn (e.g. a quick acknowledgement before a slow lookup).
`)
	if b.adapter.Name() == "command" {
		exe, _ := os.Executable()
		fmt.Fprintf(&sb, `- Tools are called from the shell (BOT_CONNECT_API is set): %[1]s tool list --format json  ·  %[1]s tool call --name <tool> --args '<json>'  (add --dry-run to check a call without running it; exit 4 = not allowed for this person)
`, exe)
	} else {
		sb.WriteString("- bot-connect tools come from the \"bot\" MCP server.\n")
	}
	return sb.String()
}

// turnPrompt is the user message for this turn.
func (b *Brain) turnPrompt(t hub.Turn, inlineSystem, inlineHistory bool) string {
	var sb strings.Builder
	if inlineSystem {
		sb.WriteString(b.systemPrompt())
		sb.WriteString("\n\n")
	}
	sb.WriteString("<bot-connect-context>\n")
	chat := "private chat"
	if t.Conv.IsGroup {
		chat = "group chat"
	}
	fmt.Fprintf(&sb, "time: %s\nchannel: %s %s\nbot owner: %s\n", time.Now().Format("2006-01-02 15:04 Mon"), t.Conv.Platform, chat, b.ownerName)
	sb.WriteString("senders this turn:\n")
	for _, u := range senders(t.Items) {
		fmt.Fprintf(&sb, "- %s (id %s) — %s\n", u.Display(), u.ID, u.Role)
	}
	switch t.Caller.Role {
	case identity.RoleMember:
		fmt.Fprintf(&sb, "⚠ this turn runs with member rights (%s): they can use their own private copies of per-user workers and ask read-only workers; the owner's own work, sessions and tasks are invisible to them — don't reveal any.\n", t.Caller.Display())
	case identity.RoleVisitor:
		fmt.Fprintf(&sb, "⚠ this turn involves a visitor (%s): visitor rules apply; they can only ask read-only workers (if allowed) and leave messages for the owner.\n", t.Caller.Display())
	}
	sb.WriteString("\nworkers:\n")
	sc := b.scope
	sc.Caller = t.Caller
	sb.WriteString(b.workers.ContextSummary(sc, 2))
	{
		if ts := b.workers.ActiveTasksFor(t.Conv.Key); len(ts) > 0 {
			sb.WriteString("\nopen tasks from this conversation:\n")
			for _, x := range ts {
				fmt.Fprintf(&sb, "- %s @%s [%s] %s\n", x.ID, x.Worker, x.Status, clip(oneLine(x.Instruction), 80))
			}
		}
	}
	if inlineHistory {
		if hist := b.hub.History(t.Conv); len(hist) > 0 {
			sb.WriteString("\nrecent conversation (oldest first):\n")
			for _, m := range hist {
				who := "them"
				if m.Role == "assistant" {
					who = "you"
				}
				fmt.Fprintf(&sb, "[%s] %s\n", who, clip(m.Text, 600))
			}
		}
	}
	sb.WriteString("</bot-connect-context>\n\n")
	sb.WriteString(renderItems(t.Items))
	return sb.String()
}

func senders(items []hub.Item) []identity.User {
	seen := map[string]bool{}
	var out []identity.User
	for _, it := range items {
		if it.Kind != hub.KindMessage || seen[it.From.ID] {
			continue
		}
		seen[it.From.ID] = true
		out = append(out, it.From)
	}
	return out
}

func renderItems(items []hub.Item) string {
	var lines []string
	for _, it := range items {
		ts := it.At.Format("15:04")
		if it.Kind == hub.KindTaskEvent {
			lines = append(lines, fmt.Sprintf("[%s][系统事件·任务回报]\n%s", ts, it.Text))
			continue
		}
		lines = append(lines, fmt.Sprintf("[%s] %s (%s, %s): %s", ts, it.From.Display(), it.From.ID, it.From.Role, it.Text))
	}
	return strings.Join(lines, "\n\n")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
