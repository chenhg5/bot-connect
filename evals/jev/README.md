# Jev component benchmarks

Labelled data for the L1 judgments, run with `bot-connect eval jev`.

- `scope.jsonl` — what kind of request a principal's message is (`handoff`, `program`, `goal`, `explore`, `other`), and whether it is obviously impossible (`impossible: true`). It drives the planning gate and the hints the brain gets.

Labels were written by hand; review and extend them. Keep a held-out share you don't look at while tuning questions.
