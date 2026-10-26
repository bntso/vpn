package cli

import (
	"fmt"
	"net/netip"
	"os"

	"github.com/spf13/cobra"

	"bntsovpn/internal/netconfig"
)

func initCmd() *cobra.Command {
	var subnet string
	cmd := &cobra.Command{
		Use:   "init --subnet <cidr>",
		Short: "Scaffold config.yaml and state.yaml in the config directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if subnet == "" {
				return fmt.Errorf("--subnet is required (e.g. 10.13.13.0/24 or fd7a:3b9c:1a2b::/64)")
			}
			p, err := netip.ParsePrefix(subnet)
			if err != nil {
				return fmt.Errorf("invalid --subnet: %w", err)
			}
			if p != p.Masked() {
				return fmt.Errorf("--subnet %s is not a network prefix (did you mean %s?)", p, p.Masked())
			}
			if _, err := os.Stat(configPath()); err == nil {
				return fmt.Errorf("%s already exists: refusing to init twice", configPath())
			}
			if err := os.MkdirAll(dir(), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(configPath(), []byte(templateFor(p)), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(statePath(), []byte("# bntsovpn state — tool-managed, do not edit.\nnodes: {}\n"), 0o600); err != nil {
				return err
			}
			fmt.Printf("initialized %s (family IPv%d)\n", dir(), map[bool]int{true: 4, false: 6}[p.Addr().Is4()])
			fmt.Printf("edit %s, then run `bntsovpn apply`\n", configPath())
			return nil
		},
	}
	cmd.Flags().StringVar(&subnet, "subnet", "", "mesh subnet CIDR; its family decides the network family")
	return cmd
}

// templateFor renders the commented config.yaml scaffold for the given subnet.
func templateFor(p netip.Prefix) string {
	addr := p.Addr().Next().String()
	second := p.Addr().Next().Next().String()
	third := p.Addr().Next().Next().Next().String()
	return fmt.Sprintf(`# bntsovpn configuration — user-managed, git-safe, no secrets.
# Secrets (keys, token hashes, allocated addresses) live in state.yaml.
# IPv6 recommendation: use a /64 from a private, randomly generated ULA /48.
# Example allocation:
#   organization: fd7a:3b9c:1a2b::/48
#   this VPN:     fd7a:3b9c:1a2b::/64
#   another VPN:  fd7a:3b9c:1a2b:1::/64
name: bntso
subnet: %s                  # IPv4 or IPv6 network; node addresses must use the same family
admin: home                    # hub node that runs `+"`bntsovpn serve`"+`
api_port: %d                   # control plane port on the admin node's VPN IP (TCP); default 51821
# api_bind: 0.0.0.0            # what serve binds (host or host:port); empty = admin VPN IP + api_port
# api_public: false            # opt-in: allow binding a wildcard/public address (e.g. api_bind 0.0.0.0).
#                              # Safe-ish: config payloads are sealed to each node's WireGuard key and
#                              # bearer tokens are still required. A public bind ALSO needs a TCP
#                              # port-forward for api_port on the router.
# api_url: ""                  # api_addr written into join blobs; empty = http://<admin VPN IP>:<api_port>.
#                              # Set this to the public URL (e.g. http://home.example.com:%%d) when the
#                              # API is reached without the tunnel (recovery from broken configs).
nodes:
  - name: home
    role: hub                        # hub or spoke
    address: %s                  # optional; omit to auto-allocate an address inside subnet
    endpoint: home.example.com:51820 # hubs only; required, publicly reachable IP/DNS host:port
    listen_port: 51820               # hubs only; default 51820
    # expose: [192.168.1.0/24]       # hub prefixes reachable through the VPN
    # nat_iface: eth0                # hubs with IPv4 expose entries; default eth0
    # dns: 192.168.1.1               # admin node only; pushed into every node config
  # - name: linode1
  #   role: hub
  #   address: %s
  #   endpoint: 203.0.113.10:51820
  # - name: phone
  #   role: spoke
  #   address: %s
  #   via: home                        # spokes only; primary hub with full routes
  #   # backup: [linode1]              # spokes only; backup hubs with host routes only
`, p, netconfig.DefaultAPIPort, addr, second, third)
}
