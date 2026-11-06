package device

import (
	"fmt"
	"net"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// EndpointChange records a peer's active WireGuard endpoint replacement.
type EndpointChange struct {
	Peer      string
	PublicKey wgtypes.Key
	From      *net.UDPAddr
	To        *net.UDPAddr
}

// FailoverEndpoints advances peers with stale handshakes through their ordered
// endpoint lists. Healthy endpoints are left alone. Only endpoint fields are
// changed; the interface, routes, keys, and AllowedIPs remain live.
func FailoverEndpoints(iface, confPath string, now time.Time, maxHandshakeAge time.Duration) ([]EndpointChange, error) {
	fc, err := ParseFile(confPath)
	if err != nil {
		return nil, err
	}
	live, err := LivePeers(iface)
	if err != nil {
		return nil, err
	}
	changes := selectEndpointChanges(fc, live, now, maxHandshakeAge)
	if len(changes) == 0 {
		return nil, nil
	}
	cfg := wgtypes.Config{Peers: make([]wgtypes.PeerConfig, 0, len(changes))}
	for _, change := range changes {
		cfg.Peers = append(cfg.Peers, wgtypes.PeerConfig{PublicKey: change.PublicKey, Endpoint: change.To})
	}
	client, err := wgctrl.New()
	if err != nil {
		return nil, fmt.Errorf("wgctrl: %w", err)
	}
	defer client.Close()
	if err := client.ConfigureDevice(iface, cfg); err != nil {
		return nil, fmt.Errorf("configure %s endpoints: %w", iface, err)
	}
	return changes, nil
}

func selectEndpointChanges(fc *FileConfig, live []PeerStat, now time.Time, maxHandshakeAge time.Duration) []EndpointChange {
	liveByKey := make(map[string]PeerStat, len(live))
	for _, peer := range live {
		liveByKey[peer.PublicKey.String()] = peer
	}
	var changes []EndpointChange
	for i, peer := range fc.Wg.Peers {
		if i >= len(fc.PeerEndpoints) || len(fc.PeerEndpoints[i]) < 2 {
			continue
		}
		current, ok := liveByKey[peer.PublicKey.String()]
		if !ok {
			continue
		}
		endpoints := fc.PeerEndpoints[i]
		index := endpointIndex(endpoints, current.Endpoint)
		if index >= 0 && !current.LastHandshake.IsZero() && now.Sub(current.LastHandshake) <= maxHandshakeAge {
			continue
		}
		next := 0
		if index >= 0 {
			next = (index + 1) % len(endpoints)
		}
		name := fmt.Sprintf("peer %d", i+1)
		if i < len(fc.PeerComments) && fc.PeerComments[i] != "" {
			name = fc.PeerComments[i]
		}
		changes = append(changes, EndpointChange{
			Peer:      name,
			PublicKey: peer.PublicKey,
			From:      current.Endpoint,
			To:        endpoints[next],
		})
	}
	return changes
}

func endpointIndex(endpoints []*net.UDPAddr, current *net.UDPAddr) int {
	if current == nil {
		return -1
	}
	for i, endpoint := range endpoints {
		if endpoint.Port == current.Port && endpoint.Zone == current.Zone && endpoint.IP.Equal(current.IP) {
			return i
		}
	}
	return -1
}
