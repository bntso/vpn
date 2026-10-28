package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"bntsovpn/internal/agent"
	"bntsovpn/internal/device"
)

func TestStatusUsesAgentConfigWithoutControlPlaneConfig(t *testing.T) {
	tempDir := t.TempDir()
	oldDir := dirFlag
	dirFlag = tempDir
	t.Cleanup(func() { dirFlag = oldDir })

	privateKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	peerKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	publicKey := peerKey.PublicKey()
	configPath := filepath.Join(tempDir, "wg0.conf")
	config := fmt.Sprintf(`# bntsovpn: node phone (spoke) -- generated, do not edit
[Interface]
PrivateKey = %s
Address = 10.13.13.2/24

# home (hub, via)
[Peer]
PublicKey = %s
Endpoint = 192.0.2.1:51820
AllowedIPs = 10.13.13.0/24, 192.168.1.0/24
PersistentKeepalive = 25
`, privateKey, publicKey)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	agentConfig := &agent.Config{
		Node:       "phone",
		Token:      "test-token",
		API:        "http://10.13.13.1:51821",
		ConfigPath: configPath,
		Interface:  "wg-test",
		PrivateKey: privateKey.String(),
	}
	if err := agentConfig.SaveConfig(agentConfPath()); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err = runStatusWithDevice(&out, func(iface string) bool {
		if iface != "wg-test" {
			t.Errorf("interface = %q, want wg-test", iface)
		}
		return true
	}, func(iface string) ([]device.PeerStat, error) {
		return []device.PeerStat{{
			PublicKey:     publicKey,
			LastHandshake: time.Now().Add(-10 * time.Second),
			ReceiveBytes:  123,
			TransmitBytes: 456,
		}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	output := out.String()
	for _, want := range []string{
		"NODE", "phone",
		"INTERFACE", "wg-test (up)",
		"ADDRESS", "10.13.13.2/24",
		"PEER", "home (hub, via)",
		"ENDPOINT", "192.0.2.1:51820",
		"ALLOWED IPS", "10.13.13.0/24, 192.168.1.0/24",
		"HANDSHAKE", "ago", "123 B", "456 B",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("status output missing %q:\n%s", want, output)
		}
	}
	for _, secret := range []string{agentConfig.Token, privateKey.String()} {
		if strings.Contains(output, secret) {
			t.Error("status output contains agent credentials")
		}
	}
}

func TestStatusWithoutControlPlaneOrAgentConfig(t *testing.T) {
	oldDir := dirFlag
	dirFlag = t.TempDir()
	t.Cleanup(func() { dirFlag = oldDir })

	var out bytes.Buffer
	err := runStatusWithDevice(&out, func(string) bool { return false }, func(string) ([]device.PeerStat, error) {
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "config.hcl") {
		t.Fatalf("status error = %v, want missing config.hcl", err)
	}
}
