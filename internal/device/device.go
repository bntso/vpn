// Package device owns the WireGuard interface lifecycle: it creates the
// kernel interface via netlink, parses a rendered wg0.conf, applies peers
// with wgctrl syncconf semantics (peers replaced, no link flap), and
// reproduces the wg-quick side effects the mesh relies on (address, routes,
// DNS, PostUp/PostDown NAT hooks). Callers must gate all entry points on
// root privileges so unprivileged runs never touch the system.
package device

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// FileConfig is a parsed rendered wg0.conf: the WireGuard-relevant parts
// (PrivateKey/ListenPort/[Peer] sections) plus the interface-only keys the
// tool applies itself (Address, DNS, DNSSearch, PostUp/PostDown).
type FileConfig struct {
	Wg           *wgtypes.Config
	Address      string // [Interface] Address (CIDR)
	DNS          string // [Interface] DNS (may be empty)
	DNSSearch    string // [Interface] DNSSearch (may be empty)
	PostUp       string // raw PostUp line, %i placeholders intact
	PostDown     string
	PeerComments []string // comments immediately before each [Peer] section
}

// ParseConfig parses the WireGuard-relevant parts of a rendered wg0.conf
// ([Interface] PrivateKey/ListenPort and [Peer] sections).
func ParseConfig(path string) (*wgtypes.Config, error) {
	fc, err := ParseFile(path)
	if err != nil {
		return nil, err
	}
	return fc.Wg, nil
}

// ParseFile parses a rendered wg0.conf into a FileConfig.
func ParseFile(path string) (*FileConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fc := &FileConfig{Wg: &wgtypes.Config{}}
	cfg := fc.Wg
	var peer *wgtypes.PeerConfig
	section := ""
	peerComment := ""
	pendingComment := ""

	flush := func() {
		if peer != nil {
			cfg.Peers = append(cfg.Peers, *peer)
			fc.PeerComments = append(fc.PeerComments, peerComment)
			peer = nil
			peerComment = ""
		}
	}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			pendingComment = ""
			continue
		}
		if strings.HasPrefix(line, "#") {
			pendingComment = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			continue
		}
		if strings.HasPrefix(line, "[") {
			flush()
			section = strings.ToLower(strings.Trim(line, "[]"))
			if section == "peer" {
				peer = &wgtypes.PeerConfig{}
				peerComment = pendingComment
			}
			pendingComment = ""
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch section {
		case "interface":
			switch strings.ToLower(key) {
			case "privatekey":
				k, err := wgtypes.ParseKey(val)
				if err != nil {
					return nil, fmt.Errorf("%s: private key: %w", path, err)
				}
				cfg.PrivateKey = &k
			case "listenport":
				p, err := strconv.Atoi(val)
				if err != nil {
					return nil, fmt.Errorf("%s: listen port: %w", path, err)
				}
				cfg.ListenPort = &p
			case "address":
				fc.Address = val
			case "dns":
				fc.DNS = val
			case "dnssearch":
				fc.DNSSearch = val
			case "postup":
				fc.PostUp = val
			case "postdown":
				fc.PostDown = val
			}
		case "peer":
			if peer == nil {
				continue
			}
			switch strings.ToLower(key) {
			case "publickey":
				k, err := wgtypes.ParseKey(val)
				if err != nil {
					return nil, fmt.Errorf("%s: peer public key: %w", path, err)
				}
				peer.PublicKey = k
			case "endpoint":
				addr, err := net.ResolveUDPAddr("udp", val)
				if err != nil {
					return nil, fmt.Errorf("%s: peer endpoint %q: %w", path, val, err)
				}
				peer.Endpoint = addr
			case "allowedips":
				for _, s := range strings.Split(val, ",") {
					_, ipnet, err := net.ParseCIDR(strings.TrimSpace(s))
					if err != nil {
						return nil, fmt.Errorf("%s: allowed ips %q: %w", path, s, err)
					}
					peer.AllowedIPs = append(peer.AllowedIPs, *ipnet)
				}
			case "persistentkeepalive":
				n, err := strconv.Atoi(val)
				if err != nil {
					return nil, fmt.Errorf("%s: persistent keepalive: %w", path, err)
				}
				d := time.Duration(n) * time.Second
				peer.PersistentKeepaliveInterval = &d
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	flush()
	return fc, nil
}

// InterfaceExists reports whether the named network interface is present.
func InterfaceExists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

// SyncConf applies the given rendered config to a live interface, replacing
// all peers (equivalent to `wg syncconf` on a stripped config: existing peers
// not in the file are removed, the rest updated in place, no link flap).
func SyncConf(iface, confPath string) error {
	cfg, err := ParseConfig(confPath)
	if err != nil {
		return err
	}
	replace := true
	for i := range cfg.Peers {
		cfg.Peers[i].ReplaceAllowedIPs = true
	}
	cfg.ReplacePeers = replace

	c, err := wgctrl.New()
	if err != nil {
		return fmt.Errorf("wgctrl: %w", err)
	}
	defer c.Close()
	if err := c.ConfigureDevice(iface, *cfg); err != nil {
		return fmt.Errorf("configure %s: %w", iface, err)
	}
	return nil
}

// PeerStat is live per-peer traffic info for the status command.
type PeerStat struct {
	PublicKey     wgtypes.Key
	LastHandshake time.Time
	ReceiveBytes  int64
	TransmitBytes int64
}

// LivePeers returns per-peer stats for a live interface.
func LivePeers(iface string) ([]PeerStat, error) {
	c, err := wgctrl.New()
	if err != nil {
		return nil, fmt.Errorf("wgctrl: %w", err)
	}
	defer c.Close()
	dev, err := c.Device(iface)
	if err != nil {
		return nil, fmt.Errorf("device %s: %w", iface, err)
	}
	var out []PeerStat
	for _, p := range dev.Peers {
		out = append(out, PeerStat{
			PublicKey:     p.PublicKey,
			LastHandshake: p.LastHandshakeTime,
			ReceiveBytes:  p.ReceiveBytes,
			TransmitBytes: p.TransmitBytes,
		})
	}
	return out, nil
}
