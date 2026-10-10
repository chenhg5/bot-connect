## Unreleased

### New

- **Worker templates — the brain manages its own worker pool.** Define `[[templates]]` (what jobs a kind of
  worker suits, agent, access level, and where its directory comes from: a fresh `dir`, a new git
  `worktree` + branch of a repo, or an `existing` dir under `roots`). The brain creates workers from them
  (`worker_create`, or `delegate` with `template=`), changes their purpose (`worker_update`) and retires
  them (`worker_retire`; automatically after `idle_ttl`). Permissions always come from the template;
  `max_instances` caps each; per-bot `templates = [...]` allowlist; owner/admin only. The protocol tells
  the brain how to route: continuing work → the same worker, new work → an idle fitting worker or a new one
  from the least-privileged fitting template, retire when done. CLI: `bot-connect template list`;
  `worker list` shows each worker's template.

- **Scheduled jobs.** Ask in chat ("每个工作日 9 点看一下 CI", "两小时后提醒我"); the brain creates a job and,
  when due, the instruction is posted back into that conversation and carried out (answer or delegate).
  Cron / `@every` / `@daily` / `TZ=` / one-shot `@once 2h|15:00|2026-10-12T09:00`; minimum interval 5 min;
  no overlapping runs. Owner/admin only, creator re-checked at each run, audit events `type: schedule`.
  CLI: `bot-connect schedule list|get|pause|resume|delete` (a running bot picks up changes).
- **pi brain** (`brain.agent = "pi"`): the fastest brain (~1.5 s vs ~3.2 s for Claude Code on the same
  model). A bundled extension gives pi only bot-connect's tools — no shell, no file access; no context
  files, extensions or skills; isolated config whose only model is the configured provider.
- Brain protocol: only tool calls do things — no claimed-but-not-done actions, no invented output;
  forwarding requires `notify_owner`.

### Changed

- `create_worker` tool removed (replaced by templates: the brain can no longer point a new worker at an
  arbitrary directory with default permissions).
- Fixed: provider credentials of members' per-user instances are restored after a restart.
