package config

import (
	"strings"
	"testing"

	"bntsovpn/internal/netconfig"
	"bntsovpn/internal/state"
)

func dnsRenderFixture() (*netconfig.Config, *state.State) {
	cfg := &netconfig.Config{
		Name:      "bntso",
		Subnet:    "10.13.13.0/24",
		Admin:     "home",
		DNSDomain: "bntso.internal",
		Nodes: []netconfig.Node{
			{Name: "home", Role: netconfig.RoleHub, Endpoint: "home.example.com:51820", DNS: "192.0.2.1", Aliases: []string{"gateway"}},
			{Name: "phone", Role: netconfig.RoleSpoke, Via: "home"},
		},
	}
	st := &state.State{Nodes: map[string]*state.NodeState{
		"home":  {Address: "10.13.13.1", PrivateKey: "home-private", PublicKey: "home-public"},
		"phone": {Address: "10.13.13.2", PrivateKey: "phone-private", PublicKey: "phone-public"},
	}}
	return cfg, st
}

func TestRenderPrivateDNS(t *testing.T) {
	cfg, st := dnsRenderFixture()
	rendered, err := Render(cfg, st, "phone")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DNS = 10.13.13.1\n", "DNSSearch = bntso.internal\n"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered config missing %q", want)
		}
	}
	if strings.Contains(rendered, "DNS = 192.0.2.1") {
		t.Error("upstream resolver was emitted instead of the admin VPN address")
	}
}

func TestRenderExistingDNSWithoutPrivateDomain(t *testing.T) {
	cfg, st := dnsRenderFixture()
	cfg.DNSDomain = ""
	cfg.Nodes[0].Aliases = nil
	rendered, err := Render(cfg, st, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "DNS = 192.0.2.1\n") || strings.Contains(rendered, "DNSSearch") {
		t.Error("legacy DNS rendering changed")
	}
}

func TestAliasesDoNotChangeRenderedConfigs(t *testing.T) {
	cfg, st := dnsRenderFixture()
	before, err := Render(cfg, st, "phone")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Nodes[0].Aliases = []string{"gateway", "files", "nas"}
	after, err := Render(cfg, st, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Error("alias-only change altered a rendered node config")
	}
}
