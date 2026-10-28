package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"bntsovpn/internal/agent"
	"bntsovpn/internal/device"
	"bntsovpn/internal/invite"
	"bntsovpn/systemd"
)

func joinCmd() *cobra.Command {
	var dryRun bool
	var confPath string
	cmd := &cobra.Command{
		Use:   "join <blob>",
		Short: "Onboard this node from an invite blob",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJoin(args[0], dryRun, confPath)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be done without touching the system")
	cmd.Flags().StringVar(&confPath, "config", "/etc/wireguard/wg0.conf", "where to write the WireGuard config")
	return cmd
}

func runJoin(blobStr string, dryRun bool, confPath string) error {
	blob, err := invite.Decode(blobStr)
	if err != nil {
		return err
	}

	if dryRun {
		fmt.Printf("would join as node %q (role %s)\n", blob.Node, blob.Role)
		fmt.Printf("would write %s (0600):\n\n%s\n", confPath, blob.Config)
		if blob.Hub {
			fmt.Println("would enable IP forwarding (node is a hub)")
		}
		fmt.Printf("would write %s (0600): node name, API address %s, token, private key\n", agentConfPath(), blob.APIAddr)
		fmt.Println("would bring up the wg0 interface directly (netlink, no wg-quick)")
		fmt.Println("would install bntsovpn.service and start bntsovpn")
		return nil
	}

	if !isRoot() {
		return fmt.Errorf("join requires root (re-run with sudo, or use --dry-run to preview)")
	}

	if strings.Contains(blob.Config, "PostUp") {
		if _, err := exec.LookPath("iptables"); err != nil {
			fmt.Println("warning: this node's config carries NAT rules but iptables is not installed — masquerade will fail (install iptables, e.g. apt install -y iptables)")
		}
	}

	if err := os.MkdirAll(filepath.Dir(confPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(confPath, []byte(blob.Config), 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", confPath)

	if blob.Hub {
		dropin := "net.ipv4.ip_forward=1\nnet.ipv6.conf.all.forwarding=1\n"
		if err := os.WriteFile("/etc/sysctl.d/90-bntsovpn.conf", []byte(dropin), 0o644); err != nil {
			fmt.Printf("warning: write sysctl dropin: %v\n", err)
		} else if out, err := exec.Command("sysctl", "-p", "/etc/sysctl.d/90-bntsovpn.conf").CombinedOutput(); err != nil {
			fmt.Printf("warning: sysctl -p failed: %v (%s)\n", err, out)
		}
	}

	ac := &agent.Config{
		Node:       blob.Node,
		Token:      blob.Token,
		API:        blob.APIAddr,
		ConfigPath: confPath,
		Interface:  "wg0",
		PrivateKey: blob.PrivateKey,
	}
	if err := ac.SaveConfig(agentConfPath()); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", agentConfPath())

	// Bring the tunnel up right away; the interface is managed directly by
	// the tool from here on. A failure is reported at the end but does not
	// skip the agent install (the agent retries the apply on every poll).
	upErr := device.Up("wg0", confPath, "")
	if upErr != nil {
		fmt.Printf("warning: could not bring up wg0: %v\n", upErr)
	} else {
		fmt.Println("brought wg0 up (interface managed directly, no wg-quick)")
	}

	if err := installService(systemd.Agent); err != nil {
		return fmt.Errorf("could not install bntsovpn service: %w", err)
	}
	fmt.Println("enabled bntsovpn")
	return upErr
}
