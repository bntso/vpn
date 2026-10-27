package agent

import (
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
