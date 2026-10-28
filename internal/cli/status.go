package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"bntsovpn/internal/agent"
	"bntsovpn/internal/device"
	"bntsovpn/internal/netconfig"
	"bntsovpn/internal/state"
)

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show control-plane mesh nodes or local agent status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus()
		},
	}
}

func runStatus() error {
	return runStatusWithDevice(os.Stdout, device.InterfaceExists, device.LivePeers)
}

func runStatusWithDevice(out io.Writer, interfaceExists func(string) bool, getLivePeers func(string) ([]device.PeerStat, error)) error {
	cfg, err := netconfig.Load(configPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, statErr := os.Stat(agentConfPath()); statErr == nil || !os.IsNotExist(statErr) {
				return runAgentStatus(out, interfaceExists, getLivePeers)
			}
		}
		return err
	}
	st, err := state.Load(statePath())
	if err != nil {
		return err
	}

	live := livePeerMap("wg0", interfaceExists, getLivePeers)
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if len(live) > 0 {
		fmt.Fprintln(tw, "NAME\tROLE\tADDRESS\tENDPOINT\tVIA\tHANDSHAKE\tRX\tTX")
	} else {
		fmt.Fprintln(tw, "NAME\tROLE\tADDRESS\tENDPOINT\tVIA")
	}
	for _, n := range cfg.Nodes {
		ns := st.Nodes[n.Name]
		address, via := "-", "-"
		if ns != nil {
			address = ns.Address
		}
		if n.Via != "" {
			via = n.Via
		}
		endpoint := n.Endpoint
		if endpoint == "" {
			endpoint = "-"
		}
		if len(live) > 0 {
			handshake, rx, tx := "-", "-", "-"
			if ns != nil {
				if p, ok := live[ns.PublicKey]; ok {
					handshake = ago(p.LastHandshake)
					rx = fmt.Sprintf("%d B", p.ReceiveBytes)
					tx = fmt.Sprintf("%d B", p.TransmitBytes)
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", n.Name, n.Role, address, endpoint, via, handshake, rx, tx)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", n.Name, n.Role, address, endpoint, via)
		}
	}
	return tw.Flush()
}

func runAgentStatus(out io.Writer, interfaceExists func(string) bool, getLivePeers func(string) ([]device.PeerStat, error)) error {
	cfg, err := agent.LoadConfig(agentConfPath())
	if err != nil {
		return err
	}
	if cfg.ConfigPath == "" {
		return fmt.Errorf("agent config %s has no managed config path", agentConfPath())
	}
	fc, err := device.ParseFile(cfg.ConfigPath)
	if err != nil {
		return fmt.Errorf("load staged config %s: %w", cfg.ConfigPath, err)
	}
	iface := cfg.Interface
	if iface == "" {
		iface = "wg0"
	}
	interfaceUp := interfaceExists(iface)
	live := livePeerMap(iface, interfaceExists, getLivePeers)

	address := fc.Address
	if address == "" {
		address = "-"
	}
	state := "down"
	if interfaceUp {
		state = "up"
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "NODE\t%s\nINTERFACE\t%s (%s)\nADDRESS\t%s\n\n", cfg.Node, iface, state, address)
	if len(live) > 0 {
		fmt.Fprintln(tw, "PEER\tENDPOINT\tALLOWED IPS\tHANDSHAKE\tRX\tTX")
	} else {
		fmt.Fprintln(tw, "PEER\tENDPOINT\tALLOWED IPS")
	}
	for i, peer := range fc.Wg.Peers {
		name := fmt.Sprintf("peer %d", i+1)
		if i < len(fc.PeerComments) && fc.PeerComments[i] != "" {
			name = fc.PeerComments[i]
		}
		endpoint := "-"
		if peer.Endpoint != nil {
			endpoint = peer.Endpoint.String()
		}
		allowed := make([]string, 0, len(peer.AllowedIPs))
		for _, prefix := range peer.AllowedIPs {
			allowed = append(allowed, prefix.String())
		}
		allowedIPs := strings.Join(allowed, ", ")
		if allowedIPs == "" {
			allowedIPs = "-"
		}
		if len(live) > 0 {
			handshake, rx, tx := "-", "-", "-"
			if p, ok := live[peer.PublicKey.String()]; ok {
				handshake = ago(p.LastHandshake)
				rx = fmt.Sprintf("%d B", p.ReceiveBytes)
				tx = fmt.Sprintf("%d B", p.TransmitBytes)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", name, endpoint, allowedIPs, handshake, rx, tx)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", name, endpoint, allowedIPs)
		}
	}
	return tw.Flush()
}

func livePeerMap(iface string, interfaceExists func(string) bool, getLivePeers func(string) ([]device.PeerStat, error)) map[string]device.PeerStat {
	live := map[string]device.PeerStat{}
	if interfaceExists(iface) {
		if peers, err := getLivePeers(iface); err == nil {
			for _, p := range peers {
				live[p.PublicKey.String()] = p
			}
		}
	}
	return live
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t).Round(time.Second)
	return strings.TrimSpace(d.String()) + " ago"
}
