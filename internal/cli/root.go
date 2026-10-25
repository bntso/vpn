// Package cli wires the cobra command tree: init, apply, invite, join,
// serve, agent, status.
package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const defaultDir = "/etc/bntsovpn"

var dirFlag string

func dir() string {
	if dirFlag != "" {
		return dirFlag
	}
	if env := os.Getenv("BNTSOVPN_DIR"); env != "" {
		return env
	}
	return defaultDir
}

func configPath() string    { return filepath.Join(dir(), "config.yaml") }
func statePath() string     { return filepath.Join(dir(), "state.yaml") }
func renderedDir() string   { return filepath.Join(dir(), "rendered") }
func agentConfPath() string { return filepath.Join(dir(), "agent.yaml") }

func isRoot() bool { return os.Geteuid() == 0 }

// Execute runs the root command.
func Execute() {
	root := &cobra.Command{
		Use:          "bntsovpn",
		Short:        "Config-driven WireGuard hub-and-spoke mesh manager",
		SilenceUsage: true,
	}
	root.PersistentFlags().StringVar(&dirFlag, "dir", "", "config/state directory (env BNTSOVPN_DIR, default "+defaultDir+")")

	root.AddCommand(
		initCmd(),
		applyCmd(),
		inviteCmd(),
		joinCmd(),
		serveCmd(),
		agentCmd(),
		statusCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
