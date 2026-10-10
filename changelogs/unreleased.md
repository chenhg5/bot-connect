## Unreleased

### Breaking: new CLI

The command line was rebuilt to follow the [Agent CLI Guide](https://github.com/Johnixr/agent-cli-guide).
Old invocations are gone:

| Before | Now |
|---|---|
| `bot-connect -config c.toml` | `bot-connect bot run --config c.toml` |
| `bot-connect -config c.toml -console -bot X` | `bot-connect bot run --console --console-bot X` |
| `bot-connect feishu setup -config c.toml -timeout 600` | `bot-connect feishu setup --config c.toml --timeout 10m` |
| `bot-connect tool` / `bot-connect tool <name> '<json>'` | `bot-connect tool list` / `bot-connect tool call --name <name> --args '<json>'` |

New:

- Noun-verb commands: `config init|validate|show`, `bot list|run`, `worker list|get`, `session list|get`,
  `task list|get`, `audit list`, `feishu setup`, `tool list|call`, `schema`, `version`.
- `--format json|table|ndjson` everywhere; JSON by default when stdout isn't a terminal. Data on stdout,
  logs and errors on stderr; errors are JSON (`error`, `message`, `suggestion`, `retryable`, `input`).
- Exit codes: 0 ok · 1 error · 2 usage · 3 not found · 4 permission denied · 5 conflict · 10 dry-run passed.
- `--dry-run` on commands that change something (`config init`, `feishu setup`, `tool call`).
- `bot-connect schema [--command "…"]`: the command tree as JSON (flags, types, enums, defaults, examples).
- Config lookup: `--config`, then `$BOT_CONNECT_CONFIG`, then `./config.toml`, then `~/.bot-connect/config.toml`.
- `bot run --only <bot>` runs a subset of the configured bots.
- The starter config is embedded: `config init` works offline.
