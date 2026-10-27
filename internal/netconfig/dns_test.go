package netconfig

import (
	"strings"
	"testing"
)

func validDNSConfig() *Config {
	return &Config{
		Name:      "bntso",
		Subnet:    "10.13.13.0/24",
		Admin:     "home",
		DNSDomain: "bntso.internal",
		Nodes: []Node{
			{Name: "home", Role: RoleHub, Endpoint: "home.example.com:51820", DNS: "192.0.2.1", Aliases: []string{"gateway", "files"}},
			{Name: "phone", Role: RoleSpoke, Via: "home"},
		},
	}
}

func TestValidatePrivateDNS(t *testing.T) {
	cfg := validDNSConfig()
	cfg.DNSDomain = "BnTsO.Internal."
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid private DNS config: %v", err)
	}
	if cfg.DNSDomain != "bntso.internal" {
		t.Fatalf("normalized domain = %q", cfg.DNSDomain)
	}
}

func TestValidatePrivateDNSRejectsInvalidNames(t *testing.T) {
	tests := map[string]struct {
		change func(*Config)
		want   string
	}{
		"aliases require domain": {
			change: func(c *Config) { c.DNSDomain = "" },
			want:   "aliases require dns_domain",
		},
		"upstream required": {
			change: func(c *Config) { c.Nodes[0].DNS = "" },
			want:   "requires an upstream dns",
		},
		"local domain": {
			change: func(c *Config) { c.DNSDomain = "vpn.local" },
			want:   "reserved for mDNS",
		},
		"invalid domain": {
			change: func(c *Config) { c.DNSDomain = "bad_domain" },
			want:   "ASCII letters",
		},
		"invalid node name": {
			change: func(c *Config) { c.Nodes[1].Name = "phone.example" },
			want:   "name is not a valid DNS label",
		},
		"invalid alias": {
			change: func(c *Config) { c.Nodes[0].Aliases[0] = "gateway.example" },
			want:   "alias \"gateway.example\" is not a valid DNS label",
		},
		"alias node collision": {
			change: func(c *Config) { c.Nodes[1].Name = "GATEWAY" },
			want:   "name conflicts with alias",
		},
		"case insensitive alias collision": {
			change: func(c *Config) { c.Nodes[1].Aliases = []string{"FILES"} },
			want:   "conflicts with alias \"files\"",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validDNSConfig()
			test.change(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate error = %v, want substring %q", err, test.want)
			}
		})
	}
}
