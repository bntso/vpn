package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	upgradeReleaseBaseURL = "https://github.com/bntso/vpn/releases/latest/download"
	upgradeCompletionPath = "/usr/share/bash-completion/completions/bntsovpn"
	upgradeSystemdPath    = "/run/systemd/system"
)

type upgradeOptions struct {
	baseURL         string
	executablePath  string
	completionPath  string
	client          *http.Client
	restartServices func() error
}

func upgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade this binary to the latest release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isRoot() {
				return fmt.Errorf("upgrade requires root (re-run with sudo)")
			}
			executable, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate current executable: %w", err)
			}
			executable, err = filepath.EvalSymlinks(executable)
			if err != nil {
				return fmt.Errorf("resolve current executable: %w", err)
			}
			return runUpgrade(upgradeOptions{
				baseURL:        upgradeReleaseBaseURL,
				executablePath: executable,
				completionPath: upgradeCompletionPath,
				client:         &http.Client{Timeout: 2 * time.Minute},
				restartServices: func() error {
					return restartActiveServices(upgradeSystemdPath, func(args ...string) ([]byte, error) {
						return exec.Command("systemctl", args...).CombinedOutput()
					})
				},
			})
		},
	}
}

func runUpgrade(opts upgradeOptions) error {
	asset, err := upgradeAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	var checksumData bytes.Buffer
	if err := downloadUpgradeFile(opts.client, opts.baseURL+"/checksums.txt", &checksumData); err != nil {
		return err
	}
	expected, err := parseUpgradeChecksum(checksumData.Bytes(), asset)
	if err != nil {
		return err
	}

	staged, err := os.CreateTemp(filepath.Dir(opts.executablePath), ".bntsovpn-upgrade-*")
	if err != nil {
		return fmt.Errorf("stage upgrade: %w", err)
	}
	stagedPath := staged.Name()
	defer func() {
		staged.Close()
		os.Remove(stagedPath)
	}()
	if err := downloadUpgradeFile(opts.client, opts.baseURL+"/"+asset, staged); err != nil {
		return err
	}
	if err := staged.Chmod(0o755); err != nil {
		return fmt.Errorf("make staged upgrade executable: %w", err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("close staged upgrade: %w", err)
	}

	actual, err := upgradeFileHash(stagedPath)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("checksum verification failed for %s", asset)
	}
	current, err := upgradeFileHash(opts.executablePath)
	if err != nil {
		return fmt.Errorf("hash current executable: %w", err)
	}
	if current == expected {
		fmt.Println("bntsovpn is already up to date")
		return nil
	}

	completion, err := exec.Command(stagedPath, "completion", "bash").CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(completion))
		if detail != "" {
			return fmt.Errorf("generate Bash completion from verified release: %w (%s)", err, detail)
		}
		return fmt.Errorf("generate Bash completion from verified release: %w", err)
	}
	if err := os.Rename(stagedPath, opts.executablePath); err != nil {
		return fmt.Errorf("replace %s: %w", opts.executablePath, err)
	}
	fmt.Printf("upgraded %s\n", opts.executablePath)

	if err := installUpgradeCompletion(opts.completionPath, completion); err != nil {
		return err
	}
	fmt.Printf("updated %s\n", opts.completionPath)
	if opts.restartServices != nil {
		if err := opts.restartServices(); err != nil {
			return err
		}
	}
	return nil
}

func upgradeAsset(goos, goarch string) (string, error) {
	if goos != "linux" {
		return "", fmt.Errorf("upgrade is only supported on Linux")
	}
	switch goarch {
	case "amd64", "arm64":
		return "bntsovpn-linux-" + goarch, nil
	default:
		return "", fmt.Errorf("upgrade does not support architecture %s", goarch)
	}
}

func downloadUpgradeFile(client *http.Client, url string, dst io.Writer) error {
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	if _, err := io.Copy(dst, resp.Body); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	return nil
}

func parseUpgradeChecksum(data []byte, asset string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return sum, fmt.Errorf("invalid checksums.txt line %q", line)
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name != asset {
			continue
		}
		if found {
			return sum, fmt.Errorf("checksums.txt contains duplicate entries for %s", asset)
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size {
			return sum, fmt.Errorf("invalid SHA-256 checksum for %s", asset)
		}
		copy(sum[:], decoded)
		found = true
	}
	if !found {
		return sum, fmt.Errorf("checksums.txt has no entry for %s", asset)
	}
	return sum, nil
}

func upgradeFileHash(path string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	file, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return sum, err
	}
	copy(sum[:], hash.Sum(nil))
	return sum, nil
}

func installUpgradeCompletion(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create Bash completion directory: %w", err)
	}
	staged, err := os.CreateTemp(filepath.Dir(path), ".bntsovpn-completion-*")
	if err != nil {
		return fmt.Errorf("stage Bash completion: %w", err)
	}
	stagedPath := staged.Name()
	defer func() {
		staged.Close()
		os.Remove(stagedPath)
	}()
	if _, err := staged.Write(data); err != nil {
		return fmt.Errorf("write Bash completion: %w", err)
	}
	if err := staged.Chmod(0o644); err != nil {
		return fmt.Errorf("set Bash completion permissions: %w", err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("close Bash completion: %w", err)
	}
	if err := os.Rename(stagedPath, path); err != nil {
		return fmt.Errorf("install Bash completion: %w", err)
	}
	return nil
}

func restartActiveServices(systemdPath string, run func(args ...string) ([]byte, error)) error {
	if _, err := os.Stat(systemdPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("check systemd availability: %w", err)
	}
	for _, unit := range []string{"bntsovpn-agent", "bntsovpn-serve"} {
		if _, err := run("is-active", "--quiet", unit); err != nil {
			continue
		}
		output, err := run("restart", unit)
		if err != nil {
			detail := strings.TrimSpace(string(output))
			if detail != "" {
				return fmt.Errorf("restart %s: %w (%s)", unit, err, detail)
			}
			return fmt.Errorf("restart %s: %w", unit, err)
		}
		fmt.Printf("restarted %s\n", unit)
	}
	return nil
}
