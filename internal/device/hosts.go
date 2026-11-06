package device

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultHostsPath = "/etc/hosts"
	hostsBeginMarker = "# BEGIN bntsovpn managed hosts"
	hostsEndMarker   = "# END bntsovpn managed hosts"
)

// SyncHostsFromConfig re-asserts the managed hosts block from a rendered config.
func SyncHostsFromConfig(confPath string) error {
	fc, err := ParseFile(confPath)
	if err != nil {
		return err
	}
	return SyncHosts(defaultHostsPath, fc.Hosts)
}

// SyncHosts replaces bntsovpn's managed hosts block while preserving all other lines.
func SyncHosts(path string, entries []string) error {
	for _, entry := range entries {
		if err := validateHostsEntry(entry); err != nil {
			return fmt.Errorf("invalid hosts entry %q: %w", entry, err)
		}
	}
	contents, mode, err := readHostsFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	updated, err := updateHostsContents(string(contents), entries)
	if err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}
	if updated == string(contents) {
		return nil
	}
	if err := atomicWriteHostsFile(path, []byte(updated), mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func validateHostsEntry(entry string) error {
	fields := strings.Fields(entry)
	if len(fields) < 2 || strings.Join(fields, " ") != entry {
		return fmt.Errorf("must contain an address and at least one name separated by single spaces")
	}
	addr, err := netip.ParseAddr(fields[0])
	if err != nil || addr.Zone() != "" {
		return fmt.Errorf("invalid IP address %q", fields[0])
	}
	for _, name := range fields[1:] {
		if !validHostsName(name) {
			return fmt.Errorf("invalid name %q", name)
		}
	}
	return nil
}

func validHostsName(name string) bool {
	if name == "" || len(name) > 63 || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for _, c := range []byte(name) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return false
	}
	return true
}

func updateHostsContents(contents string, entries []string) (string, error) {
	block := hostsBlock(entries)
	lines := strings.Split(contents, "\n")
	begin, end := -1, -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case hostsBeginMarker:
			if begin >= 0 {
				return "", fmt.Errorf("duplicate begin marker")
			}
			begin = i
		case hostsEndMarker:
			if end >= 0 {
				return "", fmt.Errorf("duplicate end marker")
			}
			end = i
		}
	}
	if begin < 0 && end < 0 {
		if contents == "" {
			return block, nil
		}
		if !strings.HasSuffix(contents, "\n") {
			contents += "\n"
		}
		return contents + block, nil
	}
	if begin < 0 || end < 0 || end < begin {
		return "", fmt.Errorf("unmatched or misordered managed-hosts markers")
	}
	blockLines := strings.Split(strings.TrimSuffix(block, "\n"), "\n")
	updated := append([]string{}, lines[:begin]...)
	updated = append(updated, blockLines...)
	updated = append(updated, lines[end+1:]...)
	return strings.Join(updated, "\n"), nil
}

func hostsBlock(entries []string) string {
	var b strings.Builder
	b.WriteString(hostsBeginMarker + "\n")
	for _, entry := range entries {
		b.WriteString(entry + "\n")
	}
	b.WriteString(hostsEndMarker + "\n")
	return b.String()
}

func readHostsFile(path string) ([]byte, os.FileMode, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0o644, nil
	}
	if err != nil {
		return nil, 0, err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	return contents, info.Mode().Perm(), nil
}

func atomicWriteHostsFile(path string, contents []byte, mode os.FileMode) error {
	target := path
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err = filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
	}
	file, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".bntsovpn-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(contents); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, target)
}
