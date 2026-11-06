package device

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestParseFilePeerEndpoints(t *testing.T) {
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wg0.conf")
	contents := "[Peer]\nPublicKey = " + key.PublicKey().String() + "\n" +
		"Endpoint = 10.0.0.10:51821\n" +
		"Endpoints = 10.0.0.10:51821, 192.0.2.10:51821\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	fc, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fc.Wg.Peers[0].Endpoint.String(); got != "10.0.0.10:51821" {
		t.Fatalf("active endpoint = %q, want first endpoint", got)
	}
	if len(fc.PeerEndpoints) != 1 || len(fc.PeerEndpoints[0]) != 2 {
		t.Fatalf("peer endpoints = %v, want two entries", fc.PeerEndpoints)
	}
	if got := fc.PeerEndpoints[0][1].String(); got != "192.0.2.10:51821" {
		t.Fatalf("fallback endpoint = %q", got)
	}
}

func TestParseFileRejectsInvalidEndpointList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wg0.conf")
	if err := os.WriteFile(path, []byte("[Peer]\nEndpoints = invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFile(path); err == nil {
		t.Fatal("ParseFile succeeded with an invalid endpoint list")
	}
}

func TestSelectEndpointChanges(t *testing.T) {
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	publicKey := key.PublicKey()
	lan := &net.UDPAddr{IP: net.ParseIP("10.0.0.10"), Port: 51821}
	internet := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 51821}
	now := time.Now()
	fc := &FileConfig{
		Wg:            &wgtypes.Config{Peers: []wgtypes.PeerConfig{{PublicKey: publicKey}}},
		PeerComments:  []string{"home (hub, via)"},
		PeerEndpoints: [][]*net.UDPAddr{{lan, internet}},
	}
	tests := []struct {
		name      string
		current   *net.UDPAddr
		handshake time.Time
		want      *net.UDPAddr
	}{
		{name: "healthy LAN", current: lan, handshake: now.Add(-time.Minute)},
		{name: "healthy internet", current: internet, handshake: now.Add(-time.Minute)},
		{name: "stale LAN", current: lan, handshake: now.Add(-4 * time.Minute), want: internet},
		{name: "stale internet wraps to LAN", current: internet, handshake: now.Add(-4 * time.Minute), want: lan},
		{name: "never handshook", current: lan, want: internet},
		{name: "unknown live endpoint", current: &net.UDPAddr{IP: net.ParseIP("198.51.100.1"), Port: 51821}, handshake: now, want: lan},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			live := []PeerStat{{PublicKey: publicKey, Endpoint: tt.current, LastHandshake: tt.handshake}}
			changes := selectEndpointChanges(fc, live, now, 3*time.Minute)
			if tt.want == nil {
				if len(changes) != 0 {
					t.Fatalf("changes = %v, want none", changes)
				}
				return
			}
			if len(changes) != 1 || changes[0].To != tt.want {
				t.Fatalf("changes = %v, want endpoint %s", changes, tt.want)
			}
			if changes[0].Peer != "home (hub, via)" {
				t.Errorf("peer name = %q", changes[0].Peer)
			}
		})
	}
}
