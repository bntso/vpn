package netconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConvertYAMLPrivateDNS(t *testing.T) {
	converted, err := ConvertYAML([]byte(`name: bntso
subnet: 10.13.13.0/24
admin: home
dns_domain: bntso.internal
nodes:
  - name: home
    role: hub
    endpoint: home.example.com:51820
    dns: 192.0.2.1
    aliases: [gateway, files]
`))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.hcl")
	if err := os.WriteFile(path, converted, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DNSDomain != "bntso.internal" {
		t.Errorf("converted domain = %q", cfg.DNSDomain)
	}
	home := cfg.NodeByName(cfg.Admin)
	if len(home.Aliases) != 2 || home.Aliases[0] != "gateway" || home.Aliases[1] != "files" {
		t.Errorf("converted aliases = %v", home.Aliases)
	}
}
