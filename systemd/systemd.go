// Package systemd embeds the unit template installed by apply and join.
package systemd

import (
	_ "embed"
	"fmt"
)

type Role string

const (
	Agent Role = "agent"
	Serve Role = "serve"
)

//go:embed bntsovpn.service
var unitTemplate string

func Unit(role Role) (string, error) {
	var description, restart string
	var restartSec int
	switch role {
	case Agent:
		description = "bntsovpn agent (pulls WireGuard config from control plane)"
		restart = "always"
		restartSec = 10
	case Serve:
		description = "bntsovpn control plane (config API and private DNS over WireGuard)"
		restart = "on-failure"
		restartSec = 5
	default:
		return "", fmt.Errorf("unknown systemd role %q", role)
	}
	return fmt.Sprintf(unitTemplate, description, role, restart, restartSec), nil
}
