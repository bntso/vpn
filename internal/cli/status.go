package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"bntsovpn/internal/device"
	"bntsovpn/internal/netconfig"
	"bntsovpn/internal/state"
)

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show mesh nodes; adds live handshake/traffic when run on a live node",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus()
		},
	}
}

func runStatus() error {
	cfg, err := netconfig.Load(configPath())
	if err != nil {
		return err
	}
	st, err := state.Load(statePath())
	if err != nil {
		return err
	}

	// Live peer data, keyed by public key, when a wg0 interface exists.
	live := map[string]device.PeerStat{}
	if device.InterfaceExists("wg0") {
		if peers, err := device.LivePeers("wg0"); err == nil {
			for _, p := range peers {
				live[p.PublicKey.String()] = p
			}
		}
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
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

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t).Round(time.Second)
	return strings.TrimSpace(d.String()) + " ago"
}
