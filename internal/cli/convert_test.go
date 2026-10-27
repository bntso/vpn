package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bntsovpn/internal/netconfig"
)

const validLegacyConfig = `name: bntso
subnet: 10.13.13.0/24
admin: home
nodes:
  - name: home
    role: hub
    endpoint: home.example.com:51820
`

func TestRunConvertConfig(t *testing.T) {
	useConvertTestDir(t)
	stateContents := []byte("existing state fixture")
	if err := os.WriteFile(legacyConfigPath(), []byte(validLegacyConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath(), stateContents, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runConvertConfig(); err != nil {
		t.Fatalf("runConvertConfig: %v", err)
	}
	if _, err := netconfig.Load(configPath()); err != nil {
		t.Fatalf("converted config does not load: %v", err)
	}
	assertFileContents(t, legacyConfigPath(), []byte(validLegacyConfig))
	assertFileContents(t, statePath(), stateContents)

	converted, err := os.ReadFile(configPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := runConvertConfig(); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("second conversion = %v, want no-overwrite error", err)
	}
	assertFileContents(t, configPath(), converted)
	assertFileContents(t, legacyConfigPath(), []byte(validLegacyConfig))
	assertFileContents(t, statePath(), stateContents)
}

func TestRunConvertConfigRejectsInvalidInput(t *testing.T) {
	useConvertTestDir(t)
	if err := os.WriteFile(legacyConfigPath(), []byte("nodes: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runConvertConfig(); err == nil {
		t.Fatal("runConvertConfig succeeded with invalid YAML")
	}
	if _, err := os.Stat(configPath()); !os.IsNotExist(err) {
		t.Fatalf("invalid conversion left an output file: %v", err)
	}
	assertFileContents(t, legacyConfigPath(), []byte("nodes: ["))
}

func useConvertTestDir(t *testing.T) {
	t.Helper()
	oldDirFlag := dirFlag
	dirFlag = t.TempDir()
	t.Cleanup(func() { dirFlag = oldDirFlag })
}

func assertFileContents(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("%s changed unexpectedly\ngot:  %q\nwant: %q", filepath.Base(path), got, want)
	}
}
