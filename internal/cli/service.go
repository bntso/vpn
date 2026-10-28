package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"bntsovpn/systemd"
)

const (
	serviceName     = "bntsovpn"
	serviceUnitName = serviceName + ".service"
	serviceUnitDir  = "/etc/systemd/system"
)

var legacyServiceNames = []string{"bntsovpn-agent", "bntsovpn-serve"}

type serviceInstallOptions struct {
	unitDir string
	run     func(args ...string) ([]byte, error)
}

func installService(role systemd.Role) error {
	return installServiceWithOptions(role, serviceInstallOptions{
		unitDir: serviceUnitDir,
		run: func(args ...string) ([]byte, error) {
			return exec.Command("systemctl", args...).CombinedOutput()
		},
	})
}

func installServiceWithOptions(role systemd.Role, opts serviceInstallOptions) error {
	unit, err := systemd.Unit(role)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(opts.unitDir, serviceUnitName), []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", serviceUnitName, err)
	}

	for _, name := range legacyServiceNames {
		path := filepath.Join(opts.unitDir, name+".service")
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("check legacy unit %s: %w", name, err)
		}
		if output, err := opts.run("disable", "--now", name); err != nil {
			return systemctlError(err, output)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove legacy unit %s: %w", name, err)
		}
	}

	for _, args := range [][]string{
		{"daemon-reload"},
		{"enable", serviceName},
		{"restart", serviceName},
	} {
		if output, err := opts.run(args...); err != nil {
			return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), systemctlError(err, output))
		}
	}
	return nil
}

func systemctlError(err error, output []byte) error {
	detail := strings.TrimSpace(string(output))
	if detail != "" {
		return fmt.Errorf("%w (%s)", err, detail)
	}
	return err
}
