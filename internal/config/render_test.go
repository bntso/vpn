package config

import (
	"strings"
	"testing"

	"bntsovpn/internal/netconfig"
	"bntsovpn/internal/state"
)

func meshRenderFixture() (*netconfig.Config, *state.State) {
	cfg := &netconfig.Config{
		Name:   "bntso",
		Subnet: "10.13.13.0/24",
		Admin:  "home",
		Nodes: []netconfig.Node{
			{Name: "home", Role: netconfig.RoleHub, Endpoint: "home.example.com:51820", Aliases: []string{"gateway"}},
			{Name: "phone", Role: netconfig.RoleSpoke, Via: "home"},
		},
	}
	st := &state.State{Nodes: map[string]*state.NodeState{
		"home":  {Address: "10.13.13.1", PrivateKey: "home-private", PublicKey: "home-public"},
		"phone": {Address: "10.13.13.2", PrivateKey: "phone-private", PublicKey: "phone-public"},
	}}
	return cfg, st
}

func TestRenderHosts(t *testing.T) {
	cfg, st := meshRenderFixture()
	rendered, err := Render(cfg, st, "phone")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Hosts = 10.13.13.1 home gateway\n", "Hosts = 10.13.13.2 phone\n"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered config missing %q", want)
		}
	}
	if strings.Contains(rendered, "DNS =") || strings.Contains(rendered, "DNSSearch =") {
		t.Errorf("rendered config contains DNS settings:\n%s", rendered)
	}
}

func TestRenderEndpointFailover(t *testing.T) {
	cfg, st := meshRenderFixture()
	cfg.Nodes[0].Endpoint = ""
	cfg.Nodes[0].Endpoints = []string{"10.0.0.10:51820", "home.example.com:51820"}
	rendered, err := Render(cfg, st, "phone")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Endpoint = 10.0.0.10:51820\n",
		"Endpoints = 10.0.0.10:51820, home.example.com:51820\n",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered config missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderSingleEndpointOmitsFailoverMetadata(t *testing.T) {
	cfg, st := meshRenderFixture()
	rendered, err := Render(cfg, st, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, "Endpoints =") {
		t.Errorf("single-endpoint config contains failover metadata:\n%s", rendered)
	}
}

func TestAliasesChangeRenderedHosts(t *testing.T) {
	cfg, st := meshRenderFixture()
	before, err := Render(cfg, st, "phone")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Nodes[0].Aliases = []string{"gateway", "files", "nas"}
	after, err := Render(cfg, st, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("alias-only change did not alter rendered hosts")
	}
	if !strings.Contains(after, "Hosts = 10.13.13.1 home gateway files nas\n") {
		t.Errorf("rendered config missing updated aliases:\n%s", after)
	}
}

func TestRenderRoutesRemoteSpokesThroughTheirPrimaryHub(t *testing.T) {
	cfg, st := meshRenderFixture()
	cfg.Nodes = append(cfg.Nodes,
		netconfig.Node{Name: "linode1", Role: netconfig.RoleHub, Endpoint: "linode1.example.com:51820"},
		netconfig.Node{Name: "caster", Role: netconfig.RoleSpoke, Via: "linode1"},
	)
	st.Nodes["linode1"] = &state.NodeState{Address: "10.13.13.3", PrivateKey: "linode1-private", PublicKey: "linode1-public"}
	st.Nodes["caster"] = &state.NodeState{Address: "10.13.13.4", PrivateKey: "caster-private", PublicKey: "caster-public"}

	for _, test := range []struct {
		node        string
		viaPeer     string
		allowedIPs  string
		directSpoke string
		remoteSpoke string
	}{
		{node: "home", viaPeer: "linode1", allowedIPs: "AllowedIPs = 10.13.13.3/32, 10.13.13.4/32\n", directSpoke: "phone", remoteSpoke: "caster"},
		{node: "linode1", viaPeer: "home", allowedIPs: "AllowedIPs = 10.13.13.1/32, 10.13.13.2/32\n", directSpoke: "caster", remoteSpoke: "phone"},
	} {
		rendered, err := Render(cfg, st, test.node)
		if err != nil {
			t.Fatalf("Render(%q): %v", test.node, err)
		}
		peerStart := strings.Index(rendered, "# "+test.viaPeer+" (hub)\n")
		if peerStart < 0 {
			t.Fatalf("%s config has no %s peer", test.node, test.viaPeer)
		}
		peer := rendered[peerStart:]
		if next := strings.Index(peer[1:], "\n# "); next >= 0 {
			peer = peer[:next+1]
		}
		if !strings.Contains(peer, test.allowedIPs) {
			t.Errorf("%s peer on %s does not route the remote spoke:\n%s", test.viaPeer, test.node, peer)
		}
		if !strings.Contains(rendered, "# "+test.directSpoke+" (spoke)\n") {
			t.Errorf("%s config lost its direct spoke %s", test.node, test.directSpoke)
		}
		if strings.Contains(rendered, "# "+test.remoteSpoke+" (spoke)\n") {
			t.Errorf("%s config has a direct peer for remote spoke %s", test.node, test.remoteSpoke)
		}
	}
}
