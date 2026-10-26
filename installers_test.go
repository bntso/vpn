package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallersVerifyDownloadedReleaseAsset(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is required to test the installers")
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum is required to test checksum verification")
	}

	for _, script := range []string{"install-serve.sh", "install-agent.sh"} {
		t.Run(script, func(t *testing.T) {
			fixture := t.TempDir()
			binDir := filepath.Join(fixture, "bin")
			if err := os.Mkdir(binDir, 0o755); err != nil {
				t.Fatal(err)
			}

			const assetName = "bntsovpn-linux-amd64"
			asset := []byte("release binary fixture")
			assetPath := filepath.Join(fixture, assetName)
			if err := os.WriteFile(assetPath, asset, 0o644); err != nil {
				t.Fatal(err)
			}
			checksum := sha256.Sum256(asset)
			checksums := fmt.Sprintf("%x  %s\n", checksum, assetName)
			if err := os.WriteFile(filepath.Join(fixture, "checksums.txt"), []byte(checksums), 0o644); err != nil {
				t.Fatal(err)
			}

			writeExecutable(t, binDir, "curl", `#!/usr/bin/env bash
set -euo pipefail
cp "$FIXTURE/${2##*/}" "$4"
`)
			writeExecutable(t, binDir, "id", `#!/usr/bin/env bash
printf '0\n'
`)
			writeExecutable(t, binDir, "uname", `#!/usr/bin/env bash
if [ "${1:-}" = "-m" ]; then
    printf 'x86_64\n'
else
    printf 'Linux\n'
fi
`)
			writeExecutable(t, binDir, "install", `#!/usr/bin/env bash
set -euo pipefail
cp "$3" "$FIXTURE/installed"
`)

			env := append(os.Environ(),
				"FIXTURE="+fixture,
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			)
			runInstaller := func() (string, error) {
				cmd := exec.Command("bash", script)
				cmd.Env = env
				output, err := cmd.CombinedOutput()
				return string(output), err
			}

			output, err := runInstaller()
			if err != nil {
				t.Fatalf("installer failed with a valid release: %v\n%s", err, output)
			}
			installed, err := os.ReadFile(filepath.Join(fixture, "installed"))
			if err != nil {
				t.Fatalf("read installed fixture: %v", err)
			}
			if string(installed) != string(asset) {
				t.Fatal("installer did not install the verified release asset")
			}

			if err := os.Remove(filepath.Join(fixture, "installed")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(assetPath, []byte("corrupted download"), 0o644); err != nil {
				t.Fatal(err)
			}
			output, err = runInstaller()
			if err == nil {
				t.Fatal("installer accepted a corrupted release asset")
			}
			if !strings.Contains(output, "error: checksum verification failed") {
				t.Fatalf("installer failed for an unexpected reason:\n%s", output)
			}
			if _, err := os.Stat(filepath.Join(fixture, "installed")); !os.IsNotExist(err) {
				t.Fatal("corrupted release asset was installed")
			}
		})
	}
}

func writeExecutable(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
