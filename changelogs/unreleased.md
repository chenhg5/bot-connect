## Unreleased

### New

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
