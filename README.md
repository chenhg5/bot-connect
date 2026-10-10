# bot-connect

One IM bot per person. You chat with your bot; it answers directly or dispatches work to the
coding-agent sessions (Claude Code / Codex) it manages, and reports back when they finish.
Visitors can talk to your bot too, with restricted rights.

bot-connect has **no model loop of its own**. The bot's "brain" is a pluggable agent
(Claude Code, Codex, or any CLI agent such as pi / cursor-agent). bot-connect provides the
access layer, the worker sessions, and a tool set (over MCP and a CLI), and injects the
role prompt and live context into whichever brain you choose.

**Setting it up with an AI agent?** Give it [INSTALL.md](INSTALL.md).

Design: [docs/design.md](docs/design.md) · Framework vs. configuration, interfaces, audit: [docs/framework.md](docs/framework.md) · Roles, access levels, isolation: [docs/permissions.md](docs/permissions.md)

## Quick start

```bash
curl -fsSL https://raw.githubusercontent.com/chenhg5/bot-connect/main/install.sh | sh
# Windows (PowerShell): irm https://raw.githubusercontent.com/chenhg5/bot-connect/main/install.ps1 | iex
# or: npm install -g bot-connect     or: go install github.com/chenhg5/bot-connect/cmd/bot-connect@latest
cp config.example.toml config.toml   # fill [brain], [feishu], [[workers]]
bot-connect config init                    # ~/.bot-connect/config.toml — edit it
bot-connect config validate
bot-connect bot run                        # Feishu
bot-connect bot run --console              # chat from the terminal (you are owner; "@jack hi" = visitor)
```

Requires `claude` and/or `codex` CLIs on PATH, logged in.

## Brains

| `brain.agent` | Tools reach it via | Prompt injection | Session |
|---|---|---|---|
| `claudecode` | `--mcp-config` (strict), built-ins off via `--tools` | `--append-system-prompt` | one per conversation, `--resume` |
| `codex` | `-c mcp_servers.bot.url=…` (tools pre-approved), read-only sandbox | inlined at session start | one per conversation, `exec resume` |
| `pi` | a bundled pi extension registers bot-connect's tools; `--tools` allows only those (no shell, no files) | `--append-system-prompt` | one per conversation, `--session` |
| `command` | `bot-connect tool call --name <tool> --args '<json>'` (env `BOT_CONNECT_API`) | inlined every turn (+ recent history) | stateless |

Brains and workers are always invoked as their real CLIs (`claude -p …`, `codex exec …`, or your
`command` argv), never through wrapper scripts. Claude Code processes can be pointed at a model
provider (`provider = "minimax"`, defined in `[[providers]]`), the same env injection
cc-connect uses. Set `BOT_CONNECT_TRACE_DIR=/some/dir` to dump each agent's raw event stream.

Inside a brain turn, `bot-connect tool list` lists the tools and `bot-connect tool call --name delegate --args '{"worker":"x","instruction":"..."}'` calls one (`--dry-run` checks without running).

## Feishu setup

Pick one. Either way the bot is owned by whoever owns the Feishu app (leave `owners` empty), and it uses
the WebSocket long connection: no public callback URL, no Verification Token / Encrypt Key.

**A. Scan a QR code (no other tools):**

```bash
bot-connect feishu setup
```

Creates a bot, writes `app_id` / `app_secret` into the config, and sets the scanner as owner.

**B. Use an app held by lark-cli (credentials never leave lark-cli):**

```bash
lark-cli config init --new --name bot-connect
```

then in the config: `[feishu] larkcli_profile = "bot-connect"`.

**C. An existing app:** fill `app_id` / `app_secret`. The app needs the Bot capability, the event
`im.message.receive_v1` on the long connection, and these scopes (then publish a version):

| Scope | Why |
|---|---|
| `im:message.p2p_msg:readonly` | receive private messages |
| `im:message.group_at_msg:readonly` | receive @-mentions in groups |
| `im:message:send_as_bot` | reply |
| `im:message.reactions:write_only` | optional: the "received" emoji |
| `contact:user.base:readonly` | optional: senders' names (otherwise ids only) |
| `application:application:self_manage` | optional: read the app owner to default `owners` |

**Check it works:** the log shows `feishu bot ready` (or `lark-cli bot ready`), then `owner defaults to
the app owner`. DM the bot `/whoami`: it should say `role: owner`. No reply? Check the version is
published, the bot is in the app's availability scope, and no other process (e.g. cc-connect) holds the
same app's long connection. In groups the bot only answers when @-mentioned.

## CLI

Noun-verb commands, long flags, `--format json|table` (JSON when piped), errors as JSON on stderr,
stable exit codes (0 ok · 1 error · 2 usage · 3 not found · 4 permission denied · 5 conflict · 10 dry-run),
`--dry-run` on everything that changes something, and `bot-connect schema` to read the whole command tree
as JSON. Follows the [Agent CLI Guide](https://github.com/Johnixr/agent-cli-guide).

```
bot-connect config  init | validate | show
bot-connect bot     list | run
bot-connect worker  list | get
bot-connect session list | get
bot-connect task    list | get
bot-connect schedule list | get | pause | resume | delete
bot-connect audit   list
bot-connect feishu  setup
bot-connect tool    list | call        # for brains running as shell agents
bot-connect schema | version
```

## Scheduled jobs

Ask the bot in chat — "每个工作日 9 点看一下 CI，红了告诉我", "两小时后提醒我发周报". The brain creates a
job (`schedule_create`); when it's due, the instruction is posted back into that conversation and the brain
carries it out (answers, or delegates to a worker). Cron, `@every 2h`, `@daily`, `TZ=…` prefixes and
one-shot `@once 2h` / `@once 15:00` are supported; minimum interval 5 minutes; a run is skipped while the
previous one is still being handled. Only owners/admins can create jobs, the creator's role is re-checked at
every run, and every run is in the audit log. Inspect and control them with `bot-connect schedule …`.

## Chat commands

`/whoami` · `/status` · `/cancel <task>` · `/reset` · `/help`

## Layout

```
cmd/bot-connect      entry
internal/hub         access layer: lanes, priority, coalescing, least-privilege turns, history
internal/identity    who sent this: User/Role, Directory, OwnerSource, Policy, cached Resolver
internal/audit       structured event log (Sink interface, JSONL default)
internal/brain       brain adapters (claudecode / codex / command) + prompt & context injection
internal/worker      worker sessions, task queue, claude/codex runners
internal/tools       the tool set + privilege checks (one definition)
internal/toolserver  serves tools to the brain: MCP (/mcp/{token}) and REST for `bot-connect tool`
internal/agentcli    headless runners for claude / codex / any command
internal/platform    feishu (SDK), larkcli (Feishu via lark-cli), console
```

## License

MIT — see [LICENSE](LICENSE).
