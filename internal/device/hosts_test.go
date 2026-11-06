package device

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncHostsPreservesUnmanagedLinesAndUpdatesBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	original := "127.0.0.1 localhost\n192.0.2.10 existing\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := SyncHosts(path, []string{"10.13.13.1 home gateway", "10.13.13.2 phone"}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := original + hostsBeginMarker + "\n10.13.13.1 home gateway\n10.13.13.2 phone\n" + hostsEndMarker + "\n"
	if string(contents) != want {
		t.Fatalf("hosts contents = %q, want %q", contents, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("hosts mode = %o, want 640", info.Mode().Perm())
	}

	if err := SyncHosts(path, []string{"10.13.13.9 home files", "fd7a::2 phone"}); err != nil {
		t.Fatal(err)
	}
	contents, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want = original + hostsBeginMarker + "\n10.13.13.9 home files\nfd7a::2 phone\n" + hostsEndMarker + "\n"
	if string(contents) != want {
		t.Fatalf("updated hosts contents = %q, want %q", contents, want)
	}
}

func TestSyncHostsRejectsMalformedManagedBlock(t *testing.T) {
	for name, contents := range map[string]string{
		"missing end":   hostsBeginMarker + "\n10.13.13.1 home\n",
		"missing begin": "10.13.13.1 home\n" + hostsEndMarker + "\n",
		"duplicate begin": hostsBeginMarker + "\n" + hostsBeginMarker + "\n" +
			hostsEndMarker + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hosts")
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := SyncHosts(path, []string{"10.13.13.1 home"}); err == nil {
				t.Fatal("SyncHosts succeeded with malformed markers")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != contents {
				t.Fatalf("malformed hosts file changed to %q", got)
			}
		})
	}
}

func TestSyncHostsValidatesEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	for _, entry := range []string{"", "10.13.13.1", "bad home", "10.13.13.1 bad_name", "10.13.13.1  home"} {
		if err := SyncHosts(path, []string{entry}); err == nil {
			t.Errorf("SyncHosts accepted invalid entry %q", entry)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid entries created hosts file: %v", err)
	}
}

func TestSyncHostsPreservesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hosts")
	link := filepath.Join(dir, "hosts-link")
	if err := os.WriteFile(target, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := SyncHosts(link, []string{"10.13.13.1 home"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("hosts symlink was replaced")
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "10.13.13.1 home") {
		t.Fatalf("symlink target was not updated: %q", contents)
	}
}

func TestParseFileHosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wg0.conf")
	contents := "[Interface]\nHosts = 10.13.13.1 home gateway\nHosts = fd7a::2 phone\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hosts) != 2 || cfg.Hosts[0] != "10.13.13.1 home gateway" || cfg.Hosts[1] != "fd7a::2 phone" {
		t.Fatalf("parsed hosts = %v", cfg.Hosts)
	}
}
