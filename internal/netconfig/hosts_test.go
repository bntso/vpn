package netconfig

import (
	"strings"
	"testing"
)

func validHostsConfig() *Config {
	return &Config{
		Name:   "bntso",
		Subnet: "10.13.13.0/24",
		Admin:  "home",
		Nodes: []Node{
			{Name: "home", Role: RoleHub, Endpoint: "home.example.com:51820", Aliases: []string{"gateway", "files"}},
			{Name: "phone", Role: RoleSpoke, Via: "home"},
		},
	}
}

func TestValidateHostsNames(t *testing.T) {
	if err := validHostsConfig().Validate(); err != nil {
		t.Fatalf("valid hosts config: %v", err)
	}
}

func TestValidateHostsNamesRejectsInvalidNames(t *testing.T) {
	tests := map[string]struct {
		change func(*Config)
		want   string
	}{
		"invalid node name": {
			change: func(c *Config) { c.Nodes[1].Name = "phone.example" },
			want:   "name is not a valid hosts-file name",
		},
		"invalid alias": {
			change: func(c *Config) { c.Nodes[0].Aliases[0] = "gateway.example" },
			want:   "alias \"gateway.example\" is not a valid hosts-file name",
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
			cfg := validHostsConfig()
			test.change(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate error = %v, want substring %q", err, test.want)
			}
		})
	}
}
