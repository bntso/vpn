package cli

import (
	"errors"
	"testing"
)

func TestReconcileMissingInterface(t *testing.T) {
	upErr := errors.New("up failed")
	tests := []struct {
		name         string
		exists       bool
		upErr        error
		wantRestored bool
		wantUpCalls  int
		wantErr      error
	}{
		{name: "existing interface is unchanged", exists: true},
		{name: "missing interface is restored", wantRestored: true, wantUpCalls: 1},
		{name: "restore failure is returned", upErr: upErr, wantUpCalls: 1, wantErr: upErr},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upCalls := 0
			restored, err := reconcileMissingInterface("wg0", "rendered/home.conf", func(iface string) bool {
				if iface != "wg0" {
					t.Errorf("exists interface = %q, want wg0", iface)
				}
				return tt.exists
			}, func(iface, conf, oldConf string) error {
				upCalls++
				if iface != "wg0" {
					t.Errorf("up interface = %q, want wg0", iface)
				}
				if conf != "rendered/home.conf" || oldConf != conf {
					t.Errorf("up configs = %q, %q, want the rendered config for both", conf, oldConf)
				}
				return tt.upErr
			})

			if restored != tt.wantRestored {
				t.Errorf("restored = %v, want %v", restored, tt.wantRestored)
			}
			if upCalls != tt.wantUpCalls {
				t.Errorf("up calls = %d, want %d", upCalls, tt.wantUpCalls)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
