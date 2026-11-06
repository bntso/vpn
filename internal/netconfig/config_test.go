package netconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadResolvesHCLVariables(t *testing.T) {
	t.Setenv("BNTSOVPN_TEST_DOMAIN", "example.com")
	cfg := loadTestConfig(t, `
domain        = env("BNTSOVPN_TEST_DOMAIN")
wireguard_port = 51820

name       = "bntso"
subnet     = "10.13.13.0/24"
admin      = "home"
api_url    = "http://${node.home.endpoint}"

node "home" {
  role        = "hub"
  address     = "10.13.13.1"
  endpoint    = "home.${domain}:${wireguard_port}"
  listen_port = wireguard_port
  expose      = ["192.168.1.0/24"]
  nat_iface   = "eth1"
  aliases     = ["gateway", "files"]
}

node "phone" {
  role = "spoke"
  via  = "home"
}
`)

	if len(cfg.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(cfg.Nodes))
	}
	home := cfg.NodeByName("home")
	if home == nil {
		t.Fatal("labeled home node was not decoded")
	}
	if home.Endpoint != "home.example.com:51820" {
		t.Errorf("endpoint = %q, want resolved endpoint", home.Endpoint)
	}
	if home.ListenPort != 51820 {
		t.Errorf("listen_port = %d, want 51820", home.ListenPort)
	}
	if cfg.APIURL != "http://home.example.com:51820" {
		t.Errorf("api_url = %q, want cross-block reference", cfg.APIURL)
	}
	if home.NATIface != "eth1" || len(home.Expose) != 1 || len(home.Aliases) != 2 {
		t.Errorf("optional home attributes were not decoded: %+v", home)
	}
	if cfg.NodeByName("phone").Via != "home" {
		t.Errorf("spoke via = %q, want home", cfg.NodeByName("phone").Via)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("resolved config is invalid: %v", err)
	}
}

func TestLoadOptionalFieldsAndDefaults(t *testing.T) {
	cfg := loadTestConfig(t, `
name   = "bntso"
subnet = "10.13.13.0/24"
admin  = "home"

node "home" {
  role     = "hub"
  endpoint = "home.example.com:51820"
}
`)
	if cfg.APIPort != 0 || cfg.APIBind != "" || cfg.APIPublic || cfg.APIURL != "" {
		t.Errorf("optional root fields did not retain zero values: %+v", cfg)
	}
	home := cfg.NodeByName("home")
	if home.Address != "" || home.ListenPort != 0 || home.NATIface != "" || home.Via != "" || len(home.Expose) != 0 || len(home.Backup) != 0 {
		t.Errorf("optional node fields did not retain zero values: %+v", home)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("minimal config is invalid: %v", err)
	}
	if cfg.APIPort != DefaultAPIPort {
		t.Errorf("api_port default = %d, want %d", cfg.APIPort, DefaultAPIPort)
	}
	if home.ListenPortOrDefault() != DefaultListenPort {
		t.Errorf("listen port default = %d, want %d", home.ListenPortOrDefault(), DefaultListenPort)
	}
	if home.NATIfaceOrDefault() != DefaultNATIface {
		t.Errorf("NAT interface default = %q, want %q", home.NATIfaceOrDefault(), DefaultNATIface)
	}
}

func TestLoadRejectsInvalidHCL(t *testing.T) {
	for name, src := range map[string]string{
		"malformed": `name =`,
		"unknown node attribute": `
name   = "bntso"
subnet = "10.13.13.0/24"
admin  = "home"
node "home" {
  role     = "hub"
  endpoint = "home.example.com:51820"
  bogus    = true
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.hcl")
			if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load succeeded with invalid HCL")
			} else if !strings.Contains(err.Error(), path) {
				t.Errorf("error does not identify %s: %v", path, err)
			}
		})
	}
}

func loadTestConfig(t *testing.T, src string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.hcl")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestLoadEndpointList(t *testing.T) {
	cfg := loadTestConfig(t, `
name   = "bntso"
subnet = "10.13.13.0/24"
admin  = "home"
node "home" {
  role      = "hub"
  endpoints = ["10.0.0.10:51820", "home.example.com:51820"]
}
`)
	home := cfg.NodeByName("home")
	if got := strings.Join(home.EndpointList(), ", "); got != "10.0.0.10:51820, home.example.com:51820" {
		t.Fatalf("EndpointList() = %q", got)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("endpoint list is invalid: %v", err)
	}
}

func TestValidateEndpointFields(t *testing.T) {
	tests := map[string]struct {
		node    Node
		wantErr string
	}{
		"both forms": {
			node:    Node{Name: "home", Role: RoleHub, Endpoint: "home.example.com:51820", Endpoints: []string{"10.0.0.10:51820"}},
			wantErr: "mutually exclusive",
		},
		"invalid fallback": {
			node:    Node{Name: "home", Role: RoleHub, Endpoints: []string{"10.0.0.10:51820", "invalid"}},
			wantErr: "endpoint \"invalid\"",
		},
		"single endpoint list": {
			node:    Node{Name: "home", Role: RoleHub, Endpoints: []string{"10.0.0.10:51820"}},
			wantErr: "",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := &Config{Name: "bntso", Subnet: "10.13.13.0/24", Admin: "home", Nodes: []Node{tt.node}}
			err := cfg.Validate()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("Validate error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
