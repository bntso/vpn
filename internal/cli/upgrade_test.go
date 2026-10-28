package cli

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testUpgradeCompletion = "# bash completion fixture\ncomplete -F __start_bntsovpn bntsovpn\n"

var testUpgradeAsset = []byte(`#!/bin/sh
if [ "${1:-}" = "completion" ] && [ "${2:-}" = "bash" ]; then
    printf '# bash completion fixture\ncomplete -F __start_bntsovpn bntsovpn\n'
    exit 0
fi
exit 1
`)

func TestRunUpgradeInstallsVerifiedRelease(t *testing.T) {
	server := newUpgradeReleaseServer(t, testUpgradeAsset, "")
	defer server.Close()

	dir := t.TempDir()
	executable := filepath.Join(dir, "bntsovpn")
	if err := os.WriteFile(executable, []byte("old release"), 0o755); err != nil {
		t.Fatal(err)
	}
	completion := filepath.Join(dir, "completions", "bntsovpn")
	restarts := 0
	err := runUpgrade(upgradeOptions{
		baseURL:        server.URL,
		executablePath: executable,
		completionPath: completion,
		client:         server.Client(),
		restartServices: func() error {
			restarts++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("upgrade failed: %v", err)
	}
	assertUpgradeFile(t, executable, testUpgradeAsset, 0o755)
	assertUpgradeFile(t, completion, []byte(testUpgradeCompletion), 0o644)
	if restarts != 1 {
		t.Fatalf("service restart calls = %d, want 1", restarts)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".bntsovpn-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("staged files left behind: %v", leftovers)
	}
}

func TestRunUpgradeNoOpWhenReleaseMatches(t *testing.T) {
	server := newUpgradeReleaseServer(t, testUpgradeAsset, "")
	defer server.Close()

	dir := t.TempDir()
	executable := filepath.Join(dir, "bntsovpn")
	if err := os.WriteFile(executable, testUpgradeAsset, 0o755); err != nil {
		t.Fatal(err)
	}
	completion := filepath.Join(dir, "completions", "bntsovpn")
	restarts := 0
	err := runUpgrade(upgradeOptions{
		baseURL:        server.URL,
		executablePath: executable,
		completionPath: completion,
		client:         server.Client(),
		restartServices: func() error {
			restarts++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("no-op upgrade failed: %v", err)
	}
	assertUpgradeFile(t, executable, testUpgradeAsset, 0o755)
	if _, err := os.Stat(completion); !os.IsNotExist(err) {
		t.Fatalf("no-op upgrade created completion file: %v", err)
	}
	if restarts != 0 {
		t.Fatalf("no-op upgrade restarted services %d times", restarts)
	}
}

func TestRunUpgradeRejectsInvalidRelease(t *testing.T) {
	asset := "bntsovpn-linux-" + runtime.GOARCH
	wrongSum := sha256.Sum256([]byte("different release"))
	tests := []struct {
		name      string
		asset     []byte
		checksums string
		wantError string
	}{
		{
			name:      "checksum mismatch",
			asset:     testUpgradeAsset,
			checksums: fmt.Sprintf("%x  %s\n", wrongSum, asset),
			wantError: "checksum verification failed",
		},
		{
			name:      "missing checksum",
			asset:     testUpgradeAsset,
			checksums: fmt.Sprintf("%x  another-asset\n", wrongSum),
			wantError: "has no entry",
		},
		{
			name:      "completion fails",
			asset:     []byte("#!/bin/sh\nexit 1\n"),
			wantError: "generate Bash completion",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newUpgradeReleaseServer(t, tt.asset, tt.checksums)
			defer server.Close()
			dir := t.TempDir()
			executable := filepath.Join(dir, "bntsovpn")
			old := []byte("known good release")
			if err := os.WriteFile(executable, old, 0o755); err != nil {
				t.Fatal(err)
			}
			restarts := 0
			err := runUpgrade(upgradeOptions{
				baseURL:        server.URL,
				executablePath: executable,
				completionPath: filepath.Join(dir, "completion"),
				client:         server.Client(),
				restartServices: func() error {
					restarts++
					return nil
				},
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("upgrade error = %v, want text %q", err, tt.wantError)
			}
			assertUpgradeFile(t, executable, old, 0o755)
			if restarts != 0 {
				t.Fatalf("invalid release restarted services %d times", restarts)
			}
		})
	}
}

func TestRunUpgradeDownloadFailurePreservesExecutable(t *testing.T) {
	asset := "bntsovpn-linux-" + runtime.GOARCH
	sum := sha256.Sum256(testUpgradeAsset)
	for _, failPath := range []string{"/checksums.txt", "/" + asset} {
		t.Run(failPath, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == failPath {
					http.NotFound(w, r)
					return
				}
				if r.URL.Path == "/checksums.txt" {
					fmt.Fprintf(w, "%x  %s\n", sum, asset)
					return
				}
				w.Write(testUpgradeAsset)
			}))
			defer server.Close()
			dir := t.TempDir()
			executable := filepath.Join(dir, "bntsovpn")
			old := []byte("known good release")
			if err := os.WriteFile(executable, old, 0o755); err != nil {
				t.Fatal(err)
			}
			err := runUpgrade(upgradeOptions{
				baseURL:        server.URL,
				executablePath: executable,
				completionPath: filepath.Join(dir, "completion"),
				client:         server.Client(),
			})
			if err == nil || !strings.Contains(err.Error(), "404 Not Found") {
				t.Fatalf("download error = %v, want HTTP status", err)
			}
			assertUpgradeFile(t, executable, old, 0o755)
		})
	}
}

func TestRunUpgradeReturnsServiceRestartFailure(t *testing.T) {
	server := newUpgradeReleaseServer(t, testUpgradeAsset, "")
	defer server.Close()
	dir := t.TempDir()
	executable := filepath.Join(dir, "bntsovpn")
	if err := os.WriteFile(executable, []byte("old release"), 0o755); err != nil {
		t.Fatal(err)
	}
	restartErr := errors.New("restart failed")
	err := runUpgrade(upgradeOptions{
		baseURL:        server.URL,
		executablePath: executable,
		completionPath: filepath.Join(dir, "completion"),
		client:         server.Client(),
		restartServices: func() error {
			return restartErr
		},
	})
	if !errors.Is(err, restartErr) {
		t.Fatalf("upgrade error = %v, want restart failure", err)
	}
	assertUpgradeFile(t, executable, testUpgradeAsset, 0o755)
}

func TestParseUpgradeChecksum(t *testing.T) {
	want := sha256.Sum256(testUpgradeAsset)
	const asset = "bntsovpn-linux-amd64"
	valid := fmt.Sprintf("%x  %s\n", want, asset)
	if got, err := parseUpgradeChecksum([]byte(valid), asset); err != nil || got != want {
		t.Fatalf("valid checksum = %x, %v; want %x", got, err, want)
	}
	for _, data := range []string{
		valid + valid,
		"not-a-checksum\n",
		fmt.Sprintf("xyz  %s\n", asset),
		fmt.Sprintf("%x  another-asset\n", want),
	} {
		if _, err := parseUpgradeChecksum([]byte(data), asset); err == nil {
			t.Fatalf("parseUpgradeChecksum(%q) succeeded, want error", data)
		}
	}
}

func TestUpgradeAsset(t *testing.T) {
	if got, err := upgradeAsset("linux", "amd64"); err != nil || got != "bntsovpn-linux-amd64" {
		t.Fatalf("amd64 asset = %q, %v", got, err)
	}
	if got, err := upgradeAsset("linux", "arm64"); err != nil || got != "bntsovpn-linux-arm64" {
		t.Fatalf("arm64 asset = %q, %v", got, err)
	}
	if _, err := upgradeAsset("darwin", "amd64"); err == nil {
		t.Fatal("non-Linux upgrade succeeded")
	}
	if _, err := upgradeAsset("linux", "386"); err == nil {
		t.Fatal("unsupported architecture upgrade succeeded")
	}
}

func TestRestartActiveServices(t *testing.T) {
	var calls []string
	run := func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) == 3 && args[2] == "bntsovpn-serve" {
			return nil, errors.New("inactive")
		}
		return nil, nil
	}
	if err := restartActiveServices(t.TempDir(), run); err != nil {
		t.Fatalf("restart active services: %v", err)
	}
	want := []string{
		"is-active --quiet bntsovpn-agent",
		"restart bntsovpn-agent",
		"is-active --quiet bntsovpn-serve",
	}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("systemctl calls = %v, want %v", calls, want)
	}
}

func TestRestartActiveServicesFailure(t *testing.T) {
	run := func(args ...string) ([]byte, error) {
		if args[0] == "restart" {
			return []byte("unit failure"), errors.New("exit status 1")
		}
		return nil, nil
	}
	err := restartActiveServices(t.TempDir(), run)
	if err == nil || !strings.Contains(err.Error(), "restart bntsovpn-agent") || !strings.Contains(err.Error(), "unit failure") {
		t.Fatalf("restart error = %v, want unit and systemctl output", err)
	}
}

func TestRestartActiveServicesWithoutSystemd(t *testing.T) {
	calls := 0
	err := restartActiveServices(filepath.Join(t.TempDir(), "missing"), func(args ...string) ([]byte, error) {
		calls++
		return nil, nil
	})
	if err != nil || calls != 0 {
		t.Fatalf("restart without systemd = %v with %d calls", err, calls)
	}
}

func newUpgradeReleaseServer(t *testing.T, asset []byte, checksums string) *httptest.Server {
	t.Helper()
	assetName := "bntsovpn-linux-" + runtime.GOARCH
	if checksums == "" {
		sum := sha256.Sum256(asset)
		checksums = fmt.Sprintf("%x  %s\n", sum, assetName)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checksums.txt":
			fmt.Fprint(w, checksums)
		case "/" + assetName:
			w.Write(asset)
		default:
			http.NotFound(w, r)
		}
	}))
}

func assertUpgradeFile(t *testing.T, path string, want []byte, wantMode os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s contents = %q, want %q", path, got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != wantMode {
		t.Fatalf("%s mode = %04o, want %04o", path, info.Mode().Perm(), wantMode)
	}
}
