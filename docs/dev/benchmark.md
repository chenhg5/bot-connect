# Brain benchmark

How we know whether the brain can actually do its job — triage, planning, exploring, staffing, risk,
keeping state, running projects, keeping boundaries — rather than whether it passed one case once.

## Problems with a handful of golden cases

- One or two cases per capability: passing says little about the capability.
- One run, pass/fail: the brain is not deterministic; a single result is mostly noise.
- Capabilities mixed up: a failure doesn't say what went wrong.
- Mostly single reactions: keeping goals and projects on track only shows over days and weeks.
- Tuning against the same cases we judge by: we end up fitting the questions.

## Capabilities

| capability | what it means |
|---|---|
| triage | what kind of thing is this; sorting a pile; re-placing what turned out to be another kind |
| planning | decomposition, real dependencies, parallelism, people only where needed, feasibility first |
| explore | loops not paths; showing the principal something concrete each round; recording what was learned; converging |
| delegation | agents first; the right person by role and availability; a reason for asking a person |
| risk | seeing it early, saying it with options, no misses, no noise, saying "can't be done" |
| state | commitments never dropped, plan matches reality, deliveries verified, memory across time |
| pm | follow-ups, counter-proposals, rework, cadence |
| boundaries | privacy, injection, honesty (never claiming what didn't happen) |
| communication | speed, brevity, silence when nothing is worth saying |
| judgment | feasibility calls, ask vs act, fitting the principal's style |
| observe | taking over a project in any format: looking, writing a dossier, choosing watches; judging what a watch found |

Every golden case declares `capabilities = [...]`; results are reported per capability.

## Four layers

1. **Rules** — unit tests of the deterministic parts (plan analysis, date parsing, detectors, gates). `make check`.
2. **Judgments** — labelled datasets for each L1 (Jev) judgment, scored for accuracy, confusion and calibration.
   `bot-connect eval jev`. Cheap and nearly deterministic; most of triage lives here.
3. **Decision points** — a frozen world, one event, what the brain does. Golden cases (`evals/golden`), run k times.
   `bot-connect eval run --repeat 3`.
4. **Long-horizon scenarios** (to build) — a 2–4 week project in virtual time with events injected (someone away,
   scope change, incomplete delivery, a deadline at risk, an exploration pivoting), scored by outcomes: finished on time,
   risks missed silently, time to surface a risk, interruptions of the principal, false alarms.

## Scoring

- **Pass rate and reliability.** Each case runs k times. A capability reports the share of runs passed and the share
  of cases that passed every run. For a bot that acts on its own, reliability matters more than the average.
- **State before prose.** Checks read state (assignments, plans, signals) first; replies are judged with narrow Jev
  questions. The Jev judge itself gets calibrated against a small human-labelled set (agreement rate).
- **Ablations.** The same suite with another model, another prompt, Jev off, a tool removed — that is how we learn what
  each part contributes.
- **Held out.** A share of cases and labelled items is not looked at while tuning prompts and questions; it is only
  used to accept a change.
- **Variants.** Each decision-point case gets paraphrases and different names/domains, so passing means the capability,
  not the wording.

## Status

| layer | now | next |
|---|---|---|
| rules | unit tests across domain / app / tools | — |
| judgments | `scope.jsonl` (47 items): scope 89%, plan gate 98%, impossible 3/3, confidence 0.91 right / 0.63 wrong | datasets for intent, reply action, pile buckets, leak guard; a held-out half |
| decision points | 21 cases tagged by capability; `--repeat`; per-capability report | 10–20 per capability with variants; held-out set |
| long-horizon | — | scenario format with scripted people and injected events; outcome metrics |
