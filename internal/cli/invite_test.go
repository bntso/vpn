package cli

import "testing"

func TestDefaultAPIAddress(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    string
	}{
		{
			name:    "IPv6",
			address: "fd7a:3b9c:1a2b::1",
			want:    "http://[fd7a:3b9c:1a2b::1]:51821",
		},
		{
			name:    "IPv4",
			address: "10.13.13.1",
			want:    "http://10.13.13.1:51821",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultAPIAddress(tt.address, 51821); got != tt.want {
				t.Fatalf("defaultAPIAddress(%q, 51821) = %q, want %q", tt.address, got, tt.want)
			}
		})
	}
}
