# Permissions, roles and isolation

bot-connect exposes **one permission vocabulary** and translates it per agent. You don't need to know
Claude Code's permission modes or Codex's sandbox settings; agent-native knobs remain as an escape hatch.

## Roles (who is talking)

| Role | Who | Can |
|---|---|---|
| owner | `owners` (default: the IM app's owner) | everything |
| admin | `admins` | like owner; reads members' private workers but doesn't drive them |
| member | `members` (`"*"` = everyone) | private copies of `per_user` workers; ask `readonly` workers (if `ask_roles` has member) |
| visitor | everyone else | chat, leave messages for the owner; ask `readonly` workers only if `ask_roles` has visitor |

Members never see the owner's workers, sessions or tasks, and members never see each other's.

## Worker access levels (what a worker may do)

| `access` | Guarantee | Claude Code | Codex |
|---|---|---|---|
| `readonly` | reads `work_dir` (+`read_dirs`); no writes, no side effects | permission mode `default` (nothing approved unattended), tools `Read,Grep,Glob`, OS sandbox | permission profile: minimal system reads + `work_dir`/`read_dirs` read |
| `workspace` (default) | writes only `work_dir` (+`write_dirs`); shell runs sandboxed; `deny_read` holds for scripts too | `bypassPermissions` inside the OS sandbox + deny rules | permission profile: minimal system reads + `work_dir`/`write_dirs` write, `read_dirs` read |
| `full` | no sandbox | `bypassPermissions`, deny rules on file tools only | `danger-full-access` |

**Windows:** Claude Code has no OS sandbox there, so `confine` cannot be enforced for Claude workers
(bot-connect logs a warning); `deny_read` still covers their file tools, not shell commands. Codex
permission profiles use Codex's own Windows sandbox.

`confine = false` falls back to Codex's legacy sandboxes (which read anywhere) / no Claude sandbox.
At run time every non-`full` worker is also denied bot-connect's own state (audit log, conversations,
isolated agent homes) and every other member's private workspace.

Knobs on top: `read_dirs`, `write_dirs`, `deny_read` (default: agents' session stores, `~/.ssh`, `~/.aws`, …),
`isolate` (don't load the owner's `~/.claude` setup — default on for `per_user` and `readonly` workers).
Escape hatch (wins over `access`): `permission_mode`, `tools`, `confine`, `sandbox`.

## Brain

| Setting | Effect |
|---|---|
| `tools` | which bot-connect tools it may call (default all; per-role filtering still applies) |
| `builtin_tools` | agent built-ins it keeps (default none) |
| `read_dirs`, `deny_read` | where its file tools may read (Claude Code) |
| `mcp_servers` | your extra MCP servers |
| `isolate` (default on) | Claude: `--bare` / `--setting-sources ""`; Codex: own `CODEX_HOME` (no `~/.codex` config, global AGENTS.md, plugins; login symlinked) and no project AGENTS.md |
| pi brain | only bot-connect's tools (registered by a bundled extension, enforced with `--tools`); no context files, extensions, skills or templates; isolated config dir whose only model is the configured provider |
| `confine` (Codex, default on) | permission profile: minimal system reads + `work_dir` + `read_dirs` |

## Scheduled jobs

Schedules are a persistence channel (an instruction that fires later, unattended), so: only owners/admins
can create, list or delete them (`schedule_*` tools are hidden from members and visitors); the creator's
role is re-checked at every run and a job whose creator lost it is disabled; jobs run in the conversation
that created them, with the creator's rights; fires, skips and disables are audit events (`type: schedule`);
`bot-connect schedule list|pause|delete` works on a running bot.

## Isolation between people

- Private chats are separate conversations, each with its own brain session.
- `isolation = "per_user"` (bot): in group chats too, each sender has their own conversation.
- `per_user = "dir" | "worktree"` (worker): each member who uses it gets `<worker>@<user>`, with its own
  session and its own directory (an empty dir, or a git worktree of `work_dir` on its own branch).

## What has been verified on a real machine

| Claim | Status |
|---|---|
| Claude brain `--bare` / `--setting-sources ""` keeps `~/.claude/CLAUDE.md` out | verified |
| Claude brain file tools limited to `work_dir` + `read_dirs` | verified |
| Claude worker: deny rules alone are bypassed by scripts; with the sandbox they are not | verified |
| Claude worker `readonly`: reads, cannot write | verified |
| Member isolation, Claude: bob's instance cannot read alice's workspace (Read, cat, python, perl, dd, cp, ls all refused) | verified |
| Codex permission profile (`codex exec`): reads outside allowed paths refused, shell included | verified |
| Codex isolated `CODEX_HOME` keeps the global `~/.codex/AGENTS.md` out; login shared by symlink | verified |
| Codex brain (confined + isolated) drives bot-connect tools over MCP; Codex worker confined to its dir | verified end to end |
| Claude subscription login (no API key) with `--setting-sources ""` | not verified on the test machine |

Note when testing by hand: a shell alias such as `codex='codex --yolo'` silently disables every Codex
sandbox. bot-connect runs the binary directly and is not affected; use `command codex` in a terminal.
