# bot-connect access-layer design (MVP)

cc-connect binds **one IM bot to one agent folder**. bot-connect binds **one IM bot to one person**:
the bot is a front agent that talks to people and dispatches work to coding-agent sessions underneath.

```
 Feishu / console ──► Hub (access layer) ──► Brain adapter ──► claude -p / codex exec / any CLI
        ▲                │   ▲                  │  (injects guide + <bot-connect-context>)
        │ reply          │   │ task events      │
        │                │   │                  ▼  MCP /mcp/{turn-token}  or  `bot-connect tool`
        └────────────────┘   │           Tool server ──► Tool registry (privilege checks)
                             │                                  │
                             └──────────── Worker manager ◄─────┘
                                     (1 session + FIFO queue per worker)
```

## 0. The brain is pluggable; bot-connect has no model loop

bot-connect does not ship an LLM loop. Each turn it starts the configured brain agent and gives it:

- **Tools**: one registry (`internal/tools`) served over MCP (`/mcp/{token}`) and over REST for the
  `bot-connect tool` CLI, so any agent can use it: MCP-capable ones natively, others from a shell.
- **A token per turn** that binds the conversation and the caller's privilege. Tool calls are checked
  against it in bot-connect, so visitor restrictions hold no matter which brain is plugged in. The token
  dies when the turn ends.
- **A guide** (role and rules), passed as a system prompt where the agent supports it, otherwise inlined.
- **Live context** at the top of every turn: caller, worker status table, open tasks; plus recent
  history when the brain is stateless or starting a fresh session.

Stateful brains (Claude Code, Codex) keep one session per conversation and resume it. `/reset` drops it.
The brain's final message is the reply; `send_message` can post extra messages mid-turn.

## 1. Who gets answered first — Hub

Each conversation (one IM chat) has a **lane**. Inbound messages and task reports go into lanes.

| Rule | Why |
|---|---|
| Only one brain turn per lane at a time | Replies in a chat stay in order; no two turns race on the same history |
| Messages arriving during a running turn are **coalesced** into the next turn | Chat-style bursts ("等下" / "还有…") get one coherent reply, not three |
| `debounce` (800ms) before a lane becomes eligible | Merges rapid-fire messages into the same turn |
| At most `max_concurrent_turns` turns run globally | Caps LLM spend and rate limits |
| When slots are scarce: **owner message < task report < visitor message**, then oldest first | The owner never waits behind strangers; finished work gets reported before new visitor chat |
| Every received message gets an emoji reaction immediately | The sender knows it was seen even while queued |

Slash commands (`/whoami /status /cancel /reset /help`) bypass the brain entirely.

## 2. What context the brain sees

The brain does **not** load worker transcripts. Each turn it gets:

1. **System prompt**: role, caller identity (owner/visitor), and a live **worker table**
   (name, agent, description, idle/busy, queue length, last task one-liner).
2. **Conversation memory**: the brain's own resumed session. For stateless or fresh brains, the last
   `history_limit` plain-text messages of this chat are inlined instead.
3. **This turn's items**: coalesced messages (with sender + role) and task reports.

It pulls detail on demand: `read_worker` returns the running/queued tasks and the last N results.
The worker's own long context lives in its agent session (`--resume` / `exec resume`).

## 3. Dispatching to workers, including busy ones

- One worker = one agent session in one directory, running one task at a time.
- `delegate` is **async**. It returns a task id and the number of tasks ahead immediately, and the
  brain replies "交给 xx 了" without waiting.
- Busy worker → the task **queues** (FIFO). `urgent` jumps the queue but never preempts a running task.
  `cancel_task` / `/cancel` kills queued or running tasks (whole process group).
- When a task ends, the result is posted back as a **task event into the lane that requested it**. That
  triggers a normal turn, so the brain reports the result in the context of that chat.
- Instructions must be self-contained (the worker can't see the chat). The prompt asks the worker to end
  with a short Chinese summary, which is what flows back.

## 4. Permissions (minimal but structural)

- Owner = sender id in `bot.owners`. Everyone else is a visitor.
- A turn runs with the **least privilege of its items**. One visitor message in the batch means the
  whole turn is a visitor turn.
- Visitor tool set = `list_workers` (public workers, status only) + `notify_owner` + `send_message`.
  Enforced in the tool registry (filtered list + call-time check), not only in the prompt.
- The brain's *own* capabilities are the brain's business. Claude Code brains run with built-in tools
  off by default, and Codex brains with a read-only sandbox. A `command` brain with shell access can do
  whatever that shell can, so put visitor-facing bots on a locked-down brain.
- Worker side-effects are bounded by the agent's own mode (`permission_mode` / `sandbox`).

## Not in MVP (next candidates)

- Parallel tasks in the same repo via git worktrees (currently: queue).
- Approval round-trip: worker permission request → Feishu card → owner approves.
- Progress streaming from running tasks; rolling per-worker summaries.
- Proactive digests / scheduled checks.
- More platforms (reuse cc-connect adapters).
