// Package netconfig defines the user-managed config.hcl schema, loading,
// validation, and legacy YAML conversion. The config holds no secrets; keys,
// tokens and auto-allocated addresses live in state.yaml (package state).
package netconfig

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/caster-zip/hclconfig"
)

const (
	RoleHub   = "hub"
	RoleSpoke = "spoke"

	DefaultAPIPort    = 51821
	DefaultListenPort = 51820
	DefaultNATIface   = "eth0"
)

// Node is one mesh member. Address is optional: when set it must parse with
// net/netip (dotted-quad => IPv4, colon => IPv6), match the subnet family, lie
// inside the subnet and be unique. When empty, apply auto-allocates one.
type Node struct {
	Name       string   `hcl:"name,label" yaml:"name"`
	Role       string   `hcl:"role,attr" yaml:"role"` // hub | spoke
	Address    string   `hcl:"address,optional" yaml:"address,omitempty"`
	Endpoint   string   `hcl:"endpoint,optional" yaml:"endpoint,omitempty"`       // hubs: host:port (IP or DNS)
	ListenPort int      `hcl:"listen_port,optional" yaml:"listen_port,omitempty"` // hubs: defaults to 51820
	Expose     []string `hcl:"expose,optional" yaml:"expose,omitempty"`           // extra prefixes reachable via this node
	NATIface   string   `hcl:"nat_iface,optional" yaml:"nat_iface,omitempty"`     // for v4 masquerade, defaults to eth0
	DNS        string   `hcl:"dns,optional" yaml:"dns,omitempty"`
	Via        string   `hcl:"via,optional" yaml:"via,omitempty"`       // spokes: primary hub (full routes)
	Backup     []string `hcl:"backup,optional" yaml:"backup,omitempty"` // spokes: backup hubs (host routes only)
}

// Config is the root of config.hcl.
type Config struct {
	Name    string `hcl:"name,attr" yaml:"name"`
	Subnet  string `hcl:"subnet,attr" yaml:"subnet"` // family of this CIDR decides the network family
	Admin   string `hcl:"admin,attr" yaml:"admin"`   // node that runs the control plane
	APIPort int    `hcl:"api_port,optional" yaml:"api_port"`
	// APIBind overrides what serve binds (host or host:port; empty = admin
	// node's VPN address + api_port).
	APIBind string `hcl:"api_bind,optional" yaml:"api_bind,omitempty"`
	// APIPublic explicitly opts in to wildcard/public API bind addresses.
	// Safe because config payloads are sealed to each node's WireGuard key.
	APIPublic bool `hcl:"api_public,optional" yaml:"api_public,omitempty"`
	// APIURL overrides the api_addr written into join blobs (empty =
	// http://<admin VPN IP>:<api_port>).
	APIURL string `hcl:"api_url,optional" yaml:"api_url,omitempty"`
	Nodes  []Node `hcl:"node,block" yaml:"nodes"`
}

// Load reads, resolves and parses config.hcl.
func Load(path string) (*Config, error) {
	var cfg Config
	if err := hclconfig.LoadFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	return &cfg, nil
}

// SubnetPrefix returns the parsed network subnet.
func (c *Config) SubnetPrefix() (netip.Prefix, error) {
	p, err := netip.ParsePrefix(c.Subnet)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("subnet %q: %w", c.Subnet, err)
	}
	return p, nil
}

// NodeByName returns the named node, or nil.
func (c *Config) NodeByName(name string) *Node {
	for i := range c.Nodes {
		if c.Nodes[i].Name == name {
			return &c.Nodes[i]
		}
	}
	return nil
}

// Hubs returns the names of all hub nodes, in config order.
func (c *Config) Hubs() []string {
	var out []string
	for _, n := range c.Nodes {
		if n.Role == RoleHub {
			out = append(out, n.Name)
		}
	}
	return out
}

// ListenPortOrDefault returns the node's WireGuard listen port.
func (n *Node) ListenPortOrDefault() int {
	if n.ListenPort != 0 {
		return n.ListenPort
	}
	return DefaultListenPort
}

// NATIfaceOrDefault returns the interface used for masquerading.
func (n *Node) NATIfaceOrDefault() string {
	if n.NATIface != "" {
		return n.NATIface
	}
	return DefaultNATIface
}

// ExposePrefixes returns the parsed expose prefixes, in config order.
func (n *Node) ExposePrefixes() ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range n.Expose {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("node %q: expose %q: %w", n.Name, s, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// Validate checks the whole config and returns a descriptive error for the
// first problem found.
func (c *Config) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("config: name is required")
	}
	subnet, err := c.SubnetPrefix()
	if err != nil {
		return err
	}
	if subnet != subnet.Masked() {
		return fmt.Errorf("subnet %q is not a canonical network prefix (did you mean %q?)", c.Subnet, subnet.Masked())
	}
	if c.APIPort == 0 {
		c.APIPort = DefaultAPIPort
	}
	if c.APIPort < 1 || c.APIPort > 65535 {
		return fmt.Errorf("api_port %d out of range", c.APIPort)
	}
	if c.APIBind != "" {
		if err := validateAPIBind(c.APIBind); err != nil {
			return err
		}
	}
	if c.APIURL != "" {
		u, err := url.Parse(c.APIURL)
		if err != nil || u.Host == "" {
			return fmt.Errorf("api_url %q is not a valid URL with a host", c.APIURL)
		}
	}
	if len(c.Nodes) == 0 {
		return fmt.Errorf("config: at least one node is required")
	}

	seen := map[string]bool{}
	for i := range c.Nodes {
		n := &c.Nodes[i]
		if n.Name == "" {
			return fmt.Errorf("node #%d: name is required", i+1)
		}
		if seen[n.Name] {
			return fmt.Errorf("duplicate node name %q", n.Name)
		}
		seen[n.Name] = true
		switch n.Role {
		case RoleHub, RoleSpoke:
		default:
			return fmt.Errorf("node %q: role must be %q or %q, got %q", n.Name, RoleHub, RoleSpoke, n.Role)
		}
	}

	admin := c.NodeByName(c.Admin)
	if admin == nil {
		return fmt.Errorf("admin node %q not found in nodes", c.Admin)
	}
	if admin.Role != RoleHub {
		return fmt.Errorf("admin node %q must be a hub", c.Admin)
	}

	pinned := map[string]string{} // address -> node name
	for i := range c.Nodes {
		n := &c.Nodes[i]
		if _, err := n.ExposePrefixes(); err != nil {
			return err
		}
		if n.DNS != "" {
			if _, err := netip.ParseAddr(n.DNS); err != nil {
				return fmt.Errorf("node %q: dns %q is not a valid IP address", n.Name, n.DNS)
			}
		}
		if n.Address != "" {
			addr, err := netip.ParseAddr(n.Address)
			if err != nil {
				return fmt.Errorf("node %q: address %q does not parse: %w", n.Name, n.Address, err)
			}
			if addr.Is4() != subnet.Addr().Is4() {
				fam, want := "IPv6", "IPv4"
				if addr.Is4() {
					fam, want = "IPv4", "IPv6"
				}
				return fmt.Errorf("node %q: address %s is %s but the subnet %s is %s (single-family network)", n.Name, addr, fam, subnet, want)
			}
			if !subnet.Contains(addr) {
				return fmt.Errorf("node %q: address %s is outside subnet %s", n.Name, addr, subnet)
			}
			if other, dup := pinned[addr.String()]; dup {
				return fmt.Errorf("nodes %q and %q pin the same address %s", other, n.Name, addr)
			}
			pinned[addr.String()] = n.Name
		}

		switch n.Role {
		case RoleHub:
			if n.Endpoint == "" {
				return fmt.Errorf("hub %q: endpoint is required (hubs must be reachable)", n.Name)
			}
			if err := validateEndpoint(n.Endpoint); err != nil {
				return fmt.Errorf("hub %q: %w", n.Name, err)
			}
			if n.Via != "" || len(n.Backup) > 0 {
				return fmt.Errorf("hub %q: via/backup are only valid on spokes", n.Name)
			}
		case RoleSpoke:
			if n.Via == "" {
				return fmt.Errorf("spoke %q: via is required (name of the primary hub)", n.Name)
			}
			via := c.NodeByName(n.Via)
			if via == nil {
				return fmt.Errorf("spoke %q: via references unknown node %q", n.Name, n.Via)
			}
			if via.Role != RoleHub {
				return fmt.Errorf("spoke %q: via node %q is not a hub", n.Name, n.Via)
			}
			if n.Endpoint != "" {
				return fmt.Errorf("spoke %q: endpoint is only valid on hubs (spokes have no exposed port)", n.Name)
			}
			for _, b := range n.Backup {
				if b == n.Via {
					return fmt.Errorf("spoke %q: backup %q duplicates via", n.Name, b)
				}
				bn := c.NodeByName(b)
				if bn == nil {
					return fmt.Errorf("spoke %q: backup references unknown node %q", n.Name, b)
				}
				if bn.Role != RoleHub {
					return fmt.Errorf("spoke %q: backup node %q is not a hub", n.Name, b)
				}
			}
		}
	}
	return nil
}

func validateAPIBind(bind string) error {
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		// No port: treat the whole string as host (port defaults to api_port).
		if strings.Contains(err.Error(), "missing port") {
			if strings.Trim(bind, "[]") == "" {
				return fmt.Errorf("api_bind: empty host")
			}
			return nil
		}
		return fmt.Errorf("api_bind %q: %w", bind, err)
	}
	if host == "" && !strings.HasPrefix(bind, ":") {
		return fmt.Errorf("api_bind %q: empty host", bind)
	}
	if port != "" {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("api_bind %q: invalid port %q", bind, port)
		}
	}
	return nil
}

func validateEndpoint(ep string) error {
	host, port, err := net.SplitHostPort(ep)
	if err != nil {
		return fmt.Errorf("endpoint %q: %w", ep, err)
	}
	if host == "" {
		return fmt.Errorf("endpoint %q: empty host", ep)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("endpoint %q: invalid port %q", ep, port)
	}
	return nil
}
