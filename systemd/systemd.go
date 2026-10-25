// Package systemd embeds the unit files shipped in this directory so the
// CLI can install them during `apply` (serve unit) and `join` (agent unit).
package systemd

import _ "embed"

//go:embed bntsovpn-serve.service
var ServeUnit string

//go:embed bntsovpn-agent.service
var AgentUnit string
