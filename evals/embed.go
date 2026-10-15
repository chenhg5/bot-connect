// Package evals holds bot-connect's golden cases (see docs/dev/goals.md).
package evals

import "embed"

// Golden is the built-in golden case set (golden/*.toml).
//
//go:embed golden/*.toml
var Golden embed.FS

// Jev holds the labelled data for the L1 judgment benchmarks (jev/*.jsonl).
//
//go:embed jev/*.jsonl
var Jev embed.FS

// Long is the long-horizon scenario set (long/*.toml).
//
//go:embed long/*.toml
var Long embed.FS

// Fixtures are project folders in different formats for onboarding cases
// (fixtures/<name>/…), copied into a temporary directory per case.
//
//go:embed fixtures
var Fixtures embed.FS
