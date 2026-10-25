// Package config renders per-node wg0.conf files from the validated config
// and reconciled state, following the hub/spoke cryptokey-routing model:
//
//   - hubs peer with every node using host routes (/32 or /128); hub<->hub
//     entries additionally carry the other hub's exposed prefixes;
//   - spokes get full routes (subnet + all exposed prefixes) on their `via`
//     hub and host routes only on `backup` hubs, with PersistentKeepalive=25;
//   - hubs exposing v4 prefixes get iptables masquerade PostUp/PostDown
//     (executed by the tool itself via package device, not wg-quick);
//   - split tunnel everywhere: a default route is never emitted.
package config

import (
	"fmt"
	"net/netip"
	"strings"
	"text/template"

	"bntsovpn/internal/netconfig"
	"bntsovpn/internal/state"
)

type peerView struct {
	Comment    string
	PublicKey  string
	Endpoint   string
	AllowedIPs string
	Keepalive  int
}

type view struct {
	Name       string
	Role       string
	PrivateKey string
	Address    string
	ListenPort int
	DNS        string
	PostUp     string
	PostDown   string
	Peers      []peerView
}

var tmpl = template.Must(template.New("wg0").Parse(`# bntsovpn: node {{ .Name }} ({{ .Role }}) -- generated, do not edit
[Interface]
PrivateKey = {{ .PrivateKey }}
Address = {{ .Address }}
{{- if .ListenPort }}
ListenPort = {{ .ListenPort }}
{{- end }}
{{- if .DNS }}
DNS = {{ .DNS }}
{{- end }}
{{- if .PostUp }}
PostUp = {{ .PostUp }}
PostDown = {{ .PostDown }}
{{- end }}
{{ range .Peers }}
# {{ .Comment }}
[Peer]
PublicKey = {{ .PublicKey }}
{{- if .Endpoint }}
Endpoint = {{ .Endpoint }}
{{- end }}
AllowedIPs = {{ .AllowedIPs }}
{{- if .Keepalive }}
PersistentKeepalive = {{ .Keepalive }}
{{- end }}
{{ end }}`))

// Render produces the wg0.conf content for one node.
func Render(cfg *netconfig.Config, st *state.State, nodeName string) (string, error) {
	node := cfg.NodeByName(nodeName)
	if node == nil {
		return "", fmt.Errorf("unknown node %q", nodeName)
	}
	ns := st.Nodes[nodeName]
	if ns == nil {
		return "", fmt.Errorf("node %q has no state (run apply first)", nodeName)
	}
	subnet, err := cfg.SubnetPrefix()
	if err != nil {
		return "", err
	}
	addr, err := netip.ParseAddr(ns.Address)
	if err != nil {
		return "", fmt.Errorf("node %q: bad state address %q: %w", nodeName, ns.Address, err)
	}

	hostRoute := func(a string) string {
		parsed, err := netip.ParseAddr(a)
		if err == nil && parsed.Is4() {
			return a + "/32"
		}
		return a + "/128"
	}
	join := func(ps []netip.Prefix) string {
		var s []string
		for _, p := range ps {
			s = append(s, p.String())
		}
		return strings.Join(s, ", ")
	}

	// All exposed prefixes across the mesh (any family; WireGuard tunnels both).
	var allExposed []netip.Prefix
	for _, n := range cfg.Nodes {
		ps, err := n.ExposePrefixes()
		if err != nil {
			return "", err
		}
		allExposed = append(allExposed, ps...)
	}

	v := view{
		Name:       node.Name,
		Role:       node.Role,
		PrivateKey: ns.PrivateKey,
		Address:    fmt.Sprintf("%s/%d", addr, subnet.Bits()),
	}
	if node.Role == netconfig.RoleHub {
		v.ListenPort = node.ListenPortOrDefault()
	}
	if admin := cfg.NodeByName(cfg.Admin); admin != nil && admin.DNS != "" {
		v.DNS = admin.DNS
	}

	// Masquerade for hubs exposing v4 prefixes (VPN -> LAN NAT).
	expose, err := node.ExposePrefixes()
	if err != nil {
		return "", err
	}
	var v4Expose []netip.Prefix
	for _, p := range expose {
		if p.Addr().Is4() {
			v4Expose = append(v4Expose, p)
		}
	}
	if node.Role == netconfig.RoleHub && len(v4Expose) > 0 {
		iface := node.NATIfaceOrDefault()
		var up, down []string
		up = append(up, "iptables -A FORWARD -i %i -j ACCEPT")
		down = append(down, "iptables -D FORWARD -i %i -j ACCEPT")
		for _, p := range v4Expose {
			up = append(up, fmt.Sprintf("iptables -t nat -A POSTROUTING -s %s -d %s -o %s -j MASQUERADE", subnet, p, iface))
			down = append(down, fmt.Sprintf("iptables -t nat -D POSTROUTING -s %s -d %s -o %s -j MASQUERADE", subnet, p, iface))
		}
		v.PostUp = strings.Join(up, "; ")
		v.PostDown = strings.Join(down, "; ")
	}

	switch node.Role {
	case netconfig.RoleHub:
		// One host-route peer per other node; hub<->hub entries also carry the
		// other hub's exposed prefixes. No overlapping prefixes.
		for _, other := range cfg.Nodes {
			if other.Name == node.Name {
				continue
			}
			ost := st.Nodes[other.Name]
			if ost == nil {
				return "", fmt.Errorf("node %q has no state", other.Name)
			}
			allowed := []string{hostRoute(ost.Address)}
			if other.Role == netconfig.RoleHub {
				ps, err := other.ExposePrefixes()
				if err != nil {
					return "", err
				}
				for _, p := range ps {
					allowed = append(allowed, p.String())
				}
			}
			pv := peerView{
				Comment:    fmt.Sprintf("%s (%s)", other.Name, other.Role),
				PublicKey:  ost.PublicKey,
				AllowedIPs: strings.Join(allowed, ", "),
			}
			if other.Role == netconfig.RoleHub {
				pv.Endpoint = other.Endpoint
			}
			v.Peers = append(v.Peers, pv)
		}
	case netconfig.RoleSpoke:
		addSpokePeer := func(hubName string, full bool) error {
			hub := cfg.NodeByName(hubName)
			hst := st.Nodes[hubName]
			if hub == nil || hst == nil {
				return fmt.Errorf("spoke %q: hub %q has no state", node.Name, hubName)
			}
			var allowed string
			if full {
				// Full reachability: the whole subnet plus every exposed prefix.
				allowed = subnet.String()
				if s := join(allExposed); s != "" {
					allowed += ", " + s
				}
			} else {
				// Backup hub: host route only (no overlap with the via hub).
				allowed = hostRoute(hst.Address)
			}
			v.Peers = append(v.Peers, peerView{
				Comment:    fmt.Sprintf("%s (hub, %s)", hub.Name, map[bool]string{true: "via", false: "backup"}[full]),
				PublicKey:  hst.PublicKey,
				Endpoint:   hub.Endpoint,
				AllowedIPs: allowed,
				Keepalive:  25,
			})
			return nil
		}
		if err := addSpokePeer(node.Via, true); err != nil {
			return "", err
		}
		for _, b := range node.Backup {
			if err := addSpokePeer(b, false); err != nil {
				return "", err
			}
		}
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, v); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// RenderAll renders every node's config, keyed by node name.
func RenderAll(cfg *netconfig.Config, st *state.State) (map[string]string, error) {
	out := map[string]string{}
	for _, n := range cfg.Nodes {
		s, err := Render(cfg, st, n.Name)
		if err != nil {
			return nil, err
		}
		out[n.Name] = s
	}
	return out, nil
}
