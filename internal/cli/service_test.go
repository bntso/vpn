package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bntsovpn/systemd"
)

func TestInstallServiceWithOptionsMigratesLegacyUnits(t *testing.T) {
	dir := t.TempDir()
	for _, name := range legacyServiceNames {
		if err := os.WriteFile(filepath.Join(dir, name+".service"), []byte("legacy"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var calls []string
	opts := serviceInstallOptions{
		unitDir: dir,
		run: func(args ...string) ([]byte, error) {
			calls = append(calls, strings.Join(args, " "))
			return nil, nil
		},
	}
	if err := installServiceWithOptions(systemd.Agent, opts); err != nil {
		t.Fatalf("install agent service: %v", err)
	}

	want := []string{
		"disable --now bntsovpn-agent",
		"disable --now bntsovpn-serve",
		"daemon-reload",
		"enable bntsovpn",
		"restart bntsovpn",
	}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("systemctl calls = %v, want %v", calls, want)
	}
	for _, name := range legacyServiceNames {
		if _, err := os.Stat(filepath.Join(dir, name+".service")); !os.IsNotExist(err) {
			t.Fatalf("legacy unit %s still exists", name)
		}
	}
	unit, err := os.ReadFile(filepath.Join(dir, serviceUnitName))
	if err != nil {
		t.Fatal(err)
	}
	text := string(unit)
	for _, want := range []string{
		"ExecStart=/usr/local/bin/bntsovpn agent\n",
		"Restart=always\n",
		"RestartSec=10\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("agent unit does not contain %q:\n%s", want, text)
		}
	}
	info, err := os.Stat(filepath.Join(dir, serviceUnitName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("unit mode = %o, want 644", info.Mode().Perm())
	}
}

func TestInstallServiceWithOptionsFreshServeUnit(t *testing.T) {
	dir := t.TempDir()
	var calls []string
	opts := serviceInstallOptions{
		unitDir: dir,
		run: func(args ...string) ([]byte, error) {
			calls = append(calls, strings.Join(args, " "))
			return nil, nil
		},
	}
	if err := installServiceWithOptions(systemd.Serve, opts); err != nil {
		t.Fatalf("install serve service: %v", err)
	}
	want := []string{"daemon-reload", "enable bntsovpn", "restart bntsovpn"}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("systemctl calls = %v, want %v", calls, want)
	}
	unit, err := os.ReadFile(filepath.Join(dir, serviceUnitName))
	if err != nil {
		t.Fatal(err)
	}
	text := string(unit)
	for _, want := range []string{
		"ExecStart=/usr/local/bin/bntsovpn serve\n",
		"Restart=on-failure\n",
		"RestartSec=5\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("serve unit does not contain %q:\n%s", want, text)
		}
	}
}

func TestInstallServiceWithOptionsStopsAfterLegacyDisableFailure(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "bntsovpn-agent.service")
	if err := os.WriteFile(legacyPath, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls []string
	opts := serviceInstallOptions{
		unitDir: dir,
		run: func(args ...string) ([]byte, error) {
			calls = append(calls, strings.Join(args, " "))
			return []byte("disable failed"), errors.New("exit status 1")
		},
	}
	err := installServiceWithOptions(systemd.Serve, opts)
	if err == nil || !strings.Contains(err.Error(), "disable failed") {
		t.Fatalf("install error = %v, want systemctl output", err)
	}
	if len(calls) != 1 || calls[0] != "disable --now bntsovpn-agent" {
		t.Fatalf("systemctl calls = %v, want only legacy disable", calls)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy unit was removed after failed disable: %v", err)
	}
}
