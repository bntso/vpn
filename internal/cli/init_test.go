package cli

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bntsovpn/internal/netconfig"
)

func TestTemplateForIsValidHCL(t *testing.T) {
	for _, subnet := range []string{"10.13.13.0/24", "fd7a:3b9c:1a2b::/64"} {
		t.Run(subnet, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.hcl")
			prefix := netip.MustParsePrefix(subnet)
			if err := os.WriteFile(path, []byte(templateFor(prefix)), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := netconfig.Load(path)
			if err != nil {
				t.Fatalf("generated template does not load: %v", err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("generated template is invalid: %v", err)
			}
			home := cfg.NodeByName("home")
			if home == nil {
				t.Fatal("generated template has no home node")
			}
			if home.Endpoint != "home.example.com:51820" || home.ListenPort != netconfig.DefaultListenPort {
				t.Errorf("generated WireGuard variable was not resolved: %+v", home)
			}
		})
	}
}

func TestCheckInitConflicts(t *testing.T) {
	oldDirFlag := dirFlag
	dirFlag = t.TempDir()
	t.Cleanup(func() { dirFlag = oldDirFlag })

	if err := checkInitConflicts(); err != nil {
		t.Fatalf("empty directory has a conflict: %v", err)
	}

	stateContents := []byte("existing state fixture")
	if err := os.WriteFile(statePath(), stateContents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyConfigPath(), []byte("legacy: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkInitConflicts(); err == nil || !strings.Contains(err.Error(), "bntsovpn convert-config") {
		t.Fatalf("legacy config conflict = %v, want migration error", err)
	}
	got, err := os.ReadFile(statePath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(stateContents) {
		t.Fatal("legacy config conflict changed existing state")
	}

	if err := os.Remove(legacyConfigPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkInitConflicts(); err == nil || !strings.Contains(err.Error(), "refusing to init twice") {
		t.Fatalf("HCL config conflict = %v, want existing-file error", err)
	}
}
