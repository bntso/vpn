package cli

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"bntsovpn/internal/device"
	"bntsovpn/internal/netconfig"
	"bntsovpn/internal/server"
	"bntsovpn/internal/state"
)

func serveCmd() *cobra.Command {
	var addr string
	var allowPublic bool
	cmd := &cobra.Command{
		Use:   "serve [--addr ip:port]",
		Short: "Run the control-plane API (bound to the admin node's VPN address by default)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(addr, allowPublic)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "bind address (overrides api_bind; default: admin node's VPN address + api_port)")
	cmd.Flags().BoolVar(&allowPublic, "allow-public", false, "allow wildcard/public bind addresses (like api_public: true)")
	return cmd
}

func runServe(addrFlag string, allowPublicFlag bool) error {
	cfg, err := netconfig.Load(configPath())
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	// Bind resolution order: --addr flag > api_bind config > admin VPN
	// address + api_port.
	addr := addrFlag
	if addr == "" && cfg.APIBind != "" {
		addr = withDefaultPort(cfg.APIBind, cfg.APIPort)
	}
	if addr == "" {
		st, err := state.Load(statePath())
		if err != nil {
			return err
		}
		ns := st.Nodes[cfg.Admin]
		if ns == nil {
			return fmt.Errorf("admin node %q has no state — run apply first", cfg.Admin)
		}
		addr = net.JoinHostPort(ns.Address, strconv.Itoa(cfg.APIPort))
	}

	// Normalize empty-host binds (":8080") to an explicit wildcard so the
	// bind check below can't be bypassed.
	if strings.HasPrefix(addr, ":") {
		addr = "0.0.0.0" + addr
	}

	// Recreate wg0 from the rendered admin config on every daemon start so
	// addresses, routes, peers, and hooks left by an older configuration cannot
	// survive a restart. Best-effort — the freebind listener starts regardless
	// and the next apply retries.
	if isRoot() {
		conf := filepath.Join(renderedDir(), cfg.Admin+".conf")
		if _, err := os.Stat(conf); err == nil {
			if err := device.Recreate("wg0", conf); err != nil {
				fmt.Printf("warning: could not recreate wg0 from %s: %v\n", conf, err)
			} else {
				fmt.Println("recreated wg0 from the rendered admin config")
			}
		}
	}

	return server.Run(dir(), addr, allowPublicFlag || cfg.APIPublic)
}

// withDefaultPort appends :port when the bind string has no port.
func withDefaultPort(bind string, port int) string {
	if _, _, err := net.SplitHostPort(bind); err == nil {
		return bind
	}
	return net.JoinHostPort(bind, strconv.Itoa(port))
}
