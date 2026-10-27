package device

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvconfConfig(t *testing.T) {
	if got, want := resolvconfConfig("10.13.13.1", "bntso.internal"), "nameserver 10.13.13.1\nsearch bntso.internal\n"; got != want {
		t.Fatalf("resolvconf config = %q, want %q", got, want)
	}
	if got, want := resolvconfConfig("192.0.2.1", ""), "nameserver 192.0.2.1\n"; got != want {
		t.Fatalf("resolvconf config without search = %q, want %q", got, want)
	}
}

func TestParseFileDNSSearch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wg0.conf")
	contents := "[Interface]\nDNS = 10.13.13.1\nDNSSearch = bntso.internal\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DNS != "10.13.13.1" || cfg.DNSSearch != "bntso.internal" {
		t.Fatalf("parsed DNS = %q, search = %q", cfg.DNS, cfg.DNSSearch)
	}
}
