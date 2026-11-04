package agent

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeAPI(t *testing.T) {
	tests := []struct {
		name string
		api  string
		want string
	}{
		{
			name: "legacy IPv6",
			api:  "http://fd7a:3b9c:1a2b::1:51821",
			want: "http://[fd7a:3b9c:1a2b::1]:51821",
		},
		{
			name: "legacy IPv6 with path",
			api:  "http://fd7a:3b9c:1a2b::1:51821/base/",
			want: "http://[fd7a:3b9c:1a2b::1]:51821/base/",
		},
		{
			name: "bracketed IPv6",
			api:  "http://[fd7a:3b9c:1a2b::1]:51821",
			want: "http://[fd7a:3b9c:1a2b::1]:51821",
		},
		{
			name: "IPv4",
			api:  "http://10.13.13.1:51821",
			want: "http://10.13.13.1:51821",
		},
		{
			name: "hostname",
			api:  "http://vpn.example.com:51821",
			want: "http://vpn.example.com:51821",
		},
		{
			name: "invalid port",
			api:  "http://fd7a:3b9c:1a2b::1:invalid",
			want: "http://fd7a:3b9c:1a2b::1:invalid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeAPI(tt.api); got != tt.want {
				t.Fatalf("normalizeAPI(%q) = %q, want %q", tt.api, got, tt.want)
			}
		})
	}
}

func TestLoadConfigNormalizesLegacyIPv6API(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("api: http://fd7a:3b9c:1a2b::1:51821\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "http://[fd7a:3b9c:1a2b::1]:51821"
	if cfg.API != want {
		t.Fatalf("API = %q, want %q", cfg.API, want)
	}
}

func TestFetchTokenRejected(t *testing.T) {
	tests := map[string]struct {
		status int
		want   bool
	}{
		"forbidden":    {status: http.StatusForbidden, want: true},
		"unauthorized": {status: http.StatusUnauthorized, want: true},
		"server error": {status: http.StatusInternalServerError, want: false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "nope", tt.status)
			}))
			defer server.Close()
			cfg := &Config{Token: "test-token", API: server.URL}
			_, err := cfg.fetch(server.Client())
			if err == nil {
				t.Fatal("fetch succeeded, want error")
			}
			if got := errors.Is(err, ErrTokenRejected); got != tt.want {
				t.Fatalf("errors.Is(err, ErrTokenRejected) = %v, want %v (err: %v)", got, tt.want, err)
			}
		})
	}
}

func TestLoadConfigSetsAgentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("token: t\napi: http://10.13.13.1:51821\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentFile != path {
		t.Fatalf("AgentFile = %q, want %q", cfg.AgentFile, path)
	}
}

func TestTokenRejectedStatusLifecycle(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{AgentFile: filepath.Join(dir, "agent.yaml")}
	statusPath := filepath.Join(dir, "agent-status.yaml")

	cfg.noteTokenRejected()
	st, err := LoadStatus(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastTokenRejection.IsZero() {
		t.Fatal("LastTokenRejection not recorded")
	}
	if info, err := os.Stat(statusPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("status file mode = %v, err = %v, want 0600", info.Mode().Perm(), err)
	}

	cfg.clearTokenRejected()
	st, err = LoadStatus(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	if !st.LastTokenRejection.IsZero() {
		t.Fatal("LastTokenRejection not cleared")
	}
	if _, err := os.Stat(statusPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status file not removed: %v", err)
	}
}
