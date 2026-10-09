# What the framework owns vs. what you configure

bot-connect is the **contract around** a bot: who is talking, what may happen, in what order, and a
record of all of it. What the bot *is* (which agent, what it says, which tools it has) is configuration
or a plugin.

| Concern | Framework (fixed) | User-defined |
|---|---|---|
| Brain agent | `Adapter` interface; per-turn `Request` → `Response`; session resume; fallback to a fresh session | `brain.agent` (claudecode / codex / command / your registered adapter), model, provider, extra args, env |
| Brain prompt | **protocol** section: context block, task reports, delegation semantics, roles | **persona**: `brain.system_prompt` / `system_prompt_file` (replaces the default persona) |
| Brain tools | bot-connect tool set, served over MCP + CLI, bound to the turn's conversation and role | which bot tools are enabled (`brain.tools`), extra MCP servers (`brain.mcp_servers`), the agent's own built-ins (`builtin_tools`, sandbox) |
| Identity | every inbound message resolved to a `User` (platform, id, union id, name, email, role); `Directory` / `OwnerSource` / `Policy` interfaces | `bot.owners`, `bot.admins` (match id / union id / email); custom `Policy` in code |
| Permissions | role checks inside tools; a turn runs at the least privilege of its senders | who is owner/admin; what workers may do (`permission_mode`, `sandbox`) |
| Scheduling | lanes per chat, coalescing, debounce, priority (owner/admin > task report > visitor), global concurrency cap | the numbers (`max_concurrent_turns`, `debounce`, `turn_timeout`) |
| Workers & sessions | one queue per session (shared by all bots); reading agents' own session stores; sessions reachable **only** through a worker's allowlist; waits while a session is in use locally | which workers exist; which sessions each covers (`session_id`, `sessions`, `dir_sessions`); `deny_read`; which bots may use which workers |
| Bots | many bots per process, each with its own state dir, tool server and conversations | `[[bots]]`: channel, brain, persona, owners/admins, `workers` |
| Observability | `audit.Sink` interface; events: inbound, turn_start/end, tool_call, task, outbound | where they go (default: `<data_dir>/audit/YYYY-MM-DD.jsonl`; add sinks in code) |
| IM channel | `hub.Platform` (+ optional `identity.Directory`, `identity.OwnerSource`) | which channels: Feishu via SDK (`app_id`) or via lark-cli (`larkcli_profile`), console |

## Interfaces

```go
// brain: drive any agent
type Adapter interface {
    Name() string
    Caps() Caps                                   // Sessions, SystemPrompt
    Run(ctx, Request) (Response, error)
}
type Request  struct { SessionID, SystemPrompt, Prompt string; Tools ToolAccess; Env []string }
type Response struct { Text, SessionID string }
brain.Register("myagent", factory)

// hub: any IM channel
type Platform interface {
    Name() string
    Start(ctx, onMessage func(Inbound)) error     // Inbound carries the raw sender id
    Send(ctx, chatID, text string) error
    Ack(ctx, messageID string)
}

// identity: who is this, what may they do
type Directory   interface { LookupUser(ctx, id string) (identity.User, error) }   // optional, per platform
type OwnerSource interface { DefaultOwners(ctx) []string }                        // optional, per platform
type Policy      interface { Role(identity.User) identity.Role }                  // owner | admin | visitor

// audit: where the record goes
type Sink interface { Record(audit.Event) }
```

A new channel (DingTalk, Slack…) implements `Platform`, and `Directory` if it can look users up.
Identity, roles, scheduling, tools and audit then work unchanged.

## Audit events

One JSON object per line. `user` is always the resolved sender / caller / requester.

```json
{"type":"inbound","platform":"feishu","conv":"feishu:oc_…","chat_type":"group","message_id":"om_…",
 "user":{"platform":"feishu","id":"ou_…","union_id":"on_…","name":"Jack","role":"visitor","resolved":true},
 "text":"@alice bot 帮我看下…"}
{"type":"tool_call","conv":"feishu:oc_…","user":{…},"tool":"delegate","args":{…},"error":"permission denied: …"}
{"type":"task","task_id":"t3","worker":"cc-connect","status":"succeeded","duration":"1m31s","user":{…}}
```

Feishu only resolves **names** if the app has `contact:user.base:readonly`; without it the record still
carries open_id and union_id, and `resolved` is false.

## Sessions and privacy boundaries

A worker is the unit of permission. The brain sees the machine's agent sessions only through
`list_sessions` / `read_session` / `delegate`, and those return **only** sessions some worker of this
bot covers: the worker's own sessions (started by bot-connect), `sessions = [...]`, or, with
`dir_sessions = true`, every session of that agent in the worker's directory. Anything else is reported
as non-existent. Continuing another covered session runs on a sub-worker (`<worker>#<id8>`) that
inherits the worker's settings, so each session still has exactly one queue.

What enforces what:

| Layer | Enforced by | Strength |
|---|---|---|
| Which sessions the brain can read or drive | bot-connect tools (allowlist) | hard, in code |
| What the brain is told at start-up | `claudecode` brain runs `--bare` by default: no `~/.claude/CLAUDE.md`, auto-memory, hooks or plugins — only the framework prompt and the user's persona | hard for claudecode |
| The brain reading files on its own | `claudecode` brain: built-in tools off by default, strict MCP; if file tools are enabled they read only `work_dir` + `read_dirs`, never `deny_read` | hard for claudecode; **not** for codex (read-only sandbox still reads the disk) or shell-capable `command` brains |
| A worker reading outside its directory | the worker agent: Claude Code in `acceptEdits` cannot read outside its dir or run shell unattended; `deny_read` rules block its file tools even in `bypassPermissions` | good for Claude Code defaults; Codex sandboxes restrict writes, not reads |
| Everything | OS isolation (separate user, container, VM) | the only hard boundary for workers with shell access |
