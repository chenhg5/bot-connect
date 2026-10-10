// Package evals holds bot-connect's golden cases (see docs/dev/goals.md).
package evals

import "embed"

// Golden is the built-in golden case set (golden/*.toml).
//
//go:embed golden/*.toml
var Golden embed.FS
