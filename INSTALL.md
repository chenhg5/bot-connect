# bot-connect Installation & Configuration Guide

> **This document is written for AI coding agents (Claude Code, Codex, Cursor, …) that are helping a user
> install and configure bot-connect.** Feed this file to your agent and ask it to set the bot up.

## For the agent: how to use this guide

- **Ask, don't guess.** Every choice below (which channel, which brain, which project folders become
  workers, who may use the bot) belongs to the user. Use your interactive question tool (e.g.
  AskUserQuestion) and offer the recommended option first. The questions are listed in Step 3.
- **Secrets.** App secrets and API keys go into `config.toml` (or env vars), never into chat summaries,
  commit messages or logs. `config.toml` must stay out of git (`.gitignore` already lists it).
- **Verify each step** with the commands given before moving on, and show the user the expected log lines.
- **Don't widen permissions on your own.** Defaults are conservative; only change `access`, `members`,
  `admins` or `isolate` when the user asks, and say what the change allows.

## What is bot-connect?

One IM bot per person. People chat with the bot in Feishu (more channels later); the bot answers directly
or dispatches work to **workers** — coding-agent sessions (Claude Code / Codex) attached to project
folders — and reports back when they finish. Colleagues can talk to the bot too, with restricted rights.

```
 Feishu ──► bot-connect ──► brain (a Claude Code / Codex / any CLI agent, run per turn)
                 │              │ bot-connect tools (MCP or `bot-connect tool`)
                 └── workers ◄──┘   one queue per session, each confined to its folder
```

- **brain**: the front agent. It never edits code itself; it answers, looks things up, and delegates.
- **worker**: a coding-agent session in a folder. The bot can only see sessions its workers cover.
- **roles**: `owner` (the user), `admin`, `member`, `visitor` — see [docs/permissions.md](docs/permissions.md).

## Step 1: Check prerequisites

```bash
go version          # Go 1.25+ to build bot-connect
git --version
claude --version    # Claude Code, if it will be a brain or worker (and logged in: `claude` once)
codex --version     # Codex, if it will be a brain or worker (and logged in: `codex login`)
lark-cli --version  # optional: only for Feishu option B
```

At least one of `claude` / `codex` must be installed and logged in.

> If the user's shell has an alias like `codex='codex --yolo'` or `claude='claude --dangerously-skip-permissions'`,
> that only affects their terminal. bot-connect runs the binaries directly; sandboxes still apply.

## Step 2: Install bot-connect

Pick one (macOS / Linux, x64 / arm64):

```bash
# a) install script — downloads the newest release, verifies its checksum, installs to ~/.local/bin
curl -fsSL https://raw.githubusercontent.com/chenhg5/bot-connect/main/install.sh | sh

# b) npm — the user already has Node if they use Claude Code / Codex
npm install -g bot-connect

# c) Go toolchain
go install github.com/chenhg5/bot-connect/cmd/bot-connect@latest
```

Check: `bot-connect version`. If the install script says the directory isn't on `PATH`, add it
(e.g. `export PATH="$HOME/.local/bin:$PATH"` in the shell profile) — a `command` brain calls
`bot-connect tool`, so the binary must be on `PATH`.

From source, for development: `git clone https://github.com/chenhg5/bot-connect && cd bot-connect && make build`
(binary in `bin/`). The config example lives at
https://github.com/chenhg5/bot-connect/blob/main/config.example.toml.

## Step 3: Ask the user these questions

| # | Question | Options (recommended first) | Goes into |
|---|---|---|---|
| 1 | What should the bot be called, and what's your name? | free text | `[bot] name`, `owner_name` |
| 2 | How should it connect to Feishu? | **A** scan a QR code (creates a new bot) · **B** an app held by lark-cli · **C** an existing app (App ID/Secret) | Step 5 |
| 3 | Which agent should be the brain? | **Claude Code** (best isolation) · Codex · another CLI agent | `[brain] agent` |
| 4 | Which model account should the brain use? | **an API provider** (base URL + key + a *fast* model) · the Claude/Codex login | `[[providers]]`, `[brain] provider/model` |
| 5 | Which project folders should it be able to work in? For each: agent, one-line description | paths | `[[workers]]` |
| 6 | For each worker: may it change files and run commands, or only read? | **workspace** · readonly · full | `access` |
| 7 | Should the bot see your existing sessions in that folder (terminal/IDE ones)? | **no** (only its own) · all sessions in the folder · specific session ids | `dir_sessions`, `sessions` |
| 8 | Who besides you may use it? | **only me** · named colleagues as admins · everyone as members (private workspaces) | `admins`, `members`, `per_user` |

Notes for the agent:
- Q3/Q4: the brain only routes and summarizes, so a fast model matters more than a big one (replies are
  dominated by model latency). With Claude Code as the brain and an API provider, the brain runs fully
  isolated (`--bare`); with only the Claude login it uses `--setting-sources ""` instead.
- Q7: say plainly that "all sessions in the folder" lets the bot read those conversations.
- Q8: "members" get **their own copy** of a worker marked `per_user` (own session, own folder or git
  worktree) and can never see the owner's workers or each other's. Visitors can only chat and leave messages.

## Step 4: Write config.toml

Pick a directory for it (e.g. `~/.bot-connect/config.toml`), start from the example or write it from the
answers, and keep it private:

```bash
mkdir -p ~/.bot-connect && curl -fsSL -o ~/.bot-connect/config.toml \
  https://raw.githubusercontent.com/chenhg5/bot-connect/main/config.example.toml && chmod 600 ~/.bot-connect/config.toml
```

**Minimal personal bot** (Claude Code brain on an API provider, one project):

```toml
[bot]
name = "alice bot"
owner_name = "alice"
owners = []                    # empty = the Feishu app's owner

[brain]
agent = "claudecode"
provider = "fast"
model = "MiniMax-M2.7-highspeed"

[[providers]]
name = "fast"
base_url = "https://api.minimaxi.com/anthropic"   # any Anthropic-compatible endpoint
api_key_env = "BOT_BRAIN_API_KEY"                 # or api_key = "…" (config.toml is git-ignored)
model = "MiniMax-M2.7-highspeed"

[feishu]
larkcli_profile = "bot-connect"   # option B; for A/C use app_id / app_secret instead

[[workers]]
name = "myapp"
agent = "claudecode"
work_dir = "~/code/myapp"
description = "the myapp web service (Go + React)"
access = "workspace"
```

**Team bot** (colleagues get private workspaces; a read-only docs worker anyone can ask):

```toml
[bot]
name = "team bot"
owner_name = "alice"
members = ["*"]                # everyone in the tenant
ask_roles = ["member"]
isolation = "per_user"         # in groups, each person has their own conversation

[brain]
agent = "claudecode"
provider = "fast"

[[providers]]
name = "fast"
base_url = "https://api.minimaxi.com/anthropic"
api_key_env = "BOT_BRAIN_API_KEY"
model = "MiniMax-M2.7-highspeed"

[feishu]
app_id = "cli_xxx"
app_secret = "xxx"

[[workers]]
name = "service"
agent = "claudecode"
work_dir = "~/code/service"
description = "the service repo; each member works in their own git worktree"
per_user = "worktree"

[[workers]]
name = "handbook"
agent = "claudecode"
work_dir = "~/docs/handbook"
description = "team handbook, answers questions only"
access = "readonly"
```

Several bots in one process: use `[[bots]]` blocks (each with `[bots.brain]`, `[bots.feishu]` and a
`workers = [...]` list) instead of `[bot]` / `[brain]` / `[feishu]` — see `config.example.toml`.

All options: [config.example.toml](config.example.toml). Permissions: [docs/permissions.md](docs/permissions.md).

## Step 5: Connect Feishu

All options use Feishu's WebSocket long connection: **no public IP, no callback URL, no Verification
Token / Encrypt Key.** Whoever owns the Feishu app is the bot's owner unless `owners` says otherwise.

### Option A — scan a QR code (simplest)

```bash
bot-connect feishu setup -config ~/.bot-connect/config.toml
```

It prints a QR code and a URL. The user scans it with the Feishu mobile app and confirms. bot-connect
writes `app_id` / `app_secret` into `[feishu]` and adds the scanner to `owners`. Feishu pre-configures
the bot capability, permissions and the message event.

### Option B — an app held by lark-cli (credentials never leave lark-cli)

```bash
lark-cli config init --new --name bot-connect --brand feishu
```

Run it in the background and give the user the printed URL; it returns once they confirm in the browser.
Then set `[feishu] larkcli_profile = "bot-connect"`. Check it's ready:

```bash
lark-cli event consume im.message.receive_v1 --profile bot-connect --as bot --dry-run
```

All three preconditions should be `ok`.

### Option C — an existing Feishu app

In https://open.feishu.cn → the app:
1. App capabilities → enable **Bot**.
2. Events & callbacks → subscription mode **long connection** → add `im.message.receive_v1`.
3. Permissions → add the scopes below → **create and publish a version** (an admin may need to approve).
4. Copy App ID / App Secret into `[feishu] app_id` / `app_secret`.

| Scope | Needed for |
|---|---|
| `im:message.p2p_msg:readonly` | private messages |
| `im:message.group_at_msg:readonly` | @-mentions in groups |
| `im:message:send_as_bot` | replying |
| `im:message.reactions:write_only` | optional: "received" emoji |
| `contact:user.base:readonly` | optional: senders' names (otherwise ids only) |
| `application:application:self_manage` | optional: read the app owner to default `owners` |

Don't connect the same app from two processes (e.g. bot-connect and cc-connect): Feishu splits events
between them.

## Step 6: Run and verify

Try it locally first, without Feishu (the user is owner; `@name text` speaks as a visitor):

```bash
bot-connect -config ~/.bot-connect/config.toml -console
```

Then for real:

```bash
bot-connect -config ~/.bot-connect/config.toml
```

Expected log lines:

```
level=INFO msg="owner defaults to the app owner" platform=feishu id=ou_…
level=INFO msg="lark-cli bot ready" …          # or: msg="feishu bot ready"
level=INFO msg="bot running" bot="alice bot" brain=claudecode …
```

Then ask the user to DM the bot:
1. `/whoami` → should show `role: owner`.
2. "What workers do you have?" → lists the workers.
3. A small read-only task for one worker → an acknowledgement, then a task report a minute or two later.

What happened is recorded in `<data_dir>/audit/YYYY-MM-DD.jsonl` (default `~/.bot-connect/audit/`):
every message with the resolved sender, every turn, tool call and task.

Keep it running (deployment is the user's choice), e.g.:

```bash
nohup bot-connect -config ~/.bot-connect/config.toml > ~/.bot-connect/run.log 2>&1 &
```

or a `tmux` session, `launchd` / `systemd` unit, or a server that stays online.

## Step 7: Chat commands

| Command | Effect |
|---|---|
| `/whoami` | your name, id and role |
| `/status` | workers you may see, and open tasks of this chat |
| `/cancel <task>` | cancel a task (owner/admin) |
| `/reset` | forget this conversation (new brain session) |
| `/help` | this list |

Everything else goes to the brain. In groups the bot only answers when @-mentioned.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| No reply, no `inbound` log line | app version not published; bot not in the app's availability scope; another process holds the long connection; group message without @ |
| `inbound` logged but `turn failed … Not logged in` | the brain/worker's Claude Code has no credentials: log in with `claude`, or configure a provider |
| Replies take 10 s+ | model latency; use a fast model for the brain (`[brain] model`) |
| Sender shows as an id, no name | add `contact:user.base:readonly` and publish |
| `role: visitor` for the user | `owners` doesn't match: set it to the id shown by `/whoami`, or leave it empty to use the app owner |
| WARN `brain settings changed; dropping old brain session` | expected once after changing brain isolation: old memory is discarded on purpose |
| Task note `waiting: session in use locally` | someone wrote to that session in the last 90 s (e.g. the user in their terminal); it starts when idle |
| A worker can't run commands | `access = "readonly"`, or an explicit `permission_mode = "acceptEdits"`; remove it to use the `workspace` level |
| Codex sandbox seems ignored when testing by hand | a shell alias adds `--yolo`; use `command codex …` |

## More

- [README.md](README.md) — overview
- [docs/permissions.md](docs/permissions.md) — roles, access levels, isolation, what is verified
- [docs/framework.md](docs/framework.md) — framework vs configuration, interfaces (adapters, channels, audit)
- [docs/design.md](docs/design.md) — scheduling, context, delegation
