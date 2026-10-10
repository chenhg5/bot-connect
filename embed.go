// Package botconnect holds files embedded into the bot-connect binary.
package botconnect

import _ "embed"

// ConfigExample is config.example.toml, written by `bot-connect config init`.
//
//go:embed config.example.toml
var ConfigExample []byte
