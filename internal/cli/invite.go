package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"

	wgrender "bntsovpn/internal/config"
	"bntsovpn/internal/invite"
	"bntsovpn/internal/netconfig"
	"bntsovpn/internal/state"
)

func inviteCmd() *cobra.Command {
	var qr bool
	cmd := &cobra.Command{
		Use:   "invite <node> [--qr]",
		Short: "Print a one-line join blob for a node (or a QR of its plain config)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInvite(args[0], qr)
		},
	}
	cmd.Flags().BoolVar(&qr, "qr", false, "print a terminal QR of the plain WireGuard config only (phones; no token)")
	return cmd
}

func runInvite(name string, qr bool) error {
	cfg, err := netconfig.Load(configPath())
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	node := cfg.NodeByName(name)
	if node == nil {
		return fmt.Errorf("unknown node %q", name)
	}
	st, err := state.Load(statePath())
	if err != nil {
		return err
	}
	ns := st.Nodes[name]
	if ns == nil {
		return fmt.Errorf("node %q has no state — run apply first", name)
	}
	conf, err := wgrender.Render(cfg, st, name)
	if err != nil {
		return err
	}

	if qr {
		// Phones: plain config only, no token, no state mutation.
		q, err := qrcode.New(conf, qrcode.Medium)
		if err != nil {
			return err
		}
		fmt.Print(q.ToSmallString(false))
		return nil
	}

	// The blob carries a plaintext token; rotate so each invite is a fresh
	// credential (previous tokens for this node stop working).
	token, err := st.RotateToken(name)
	if err != nil {
		return err
	}
	if err := st.Save(statePath()); err != nil {
		return err
	}

	adminState := st.Nodes[cfg.Admin]
	if adminState == nil {
		return fmt.Errorf("admin node %q has no state — run apply first", cfg.Admin)
	}
	apiAddr := cfg.APIURL
	if apiAddr == "" {
		apiAddr = fmt.Sprintf("http://%s:%d", adminState.Address, cfg.APIPort)
	}
	blob, err := invite.Encode(&invite.Blob{
		Node:       name,
		Role:       node.Role,
		Config:     conf,
		APIAddr:    apiAddr,
		Token:      token,
		PrivateKey: ns.PrivateKey,
		Hub:        node.Role == netconfig.RoleHub,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "warning: this blob contains secrets (the node's WireGuard private key and its API token). Treat it like a password.")
	fmt.Println(blob)
	return nil
}

// renderedPath returns the on-disk rendered config path for a node.
func renderedPath(name string) string {
	return filepath.Join(renderedDir(), name+".conf")
}
