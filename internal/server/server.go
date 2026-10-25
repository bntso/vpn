// Package server implements the pull-based control plane: a tiny HTTP API
// bound to the admin node's WireGuard address. Each node authenticates with
// its bearer token and can only fetch its own rendered config.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/crypto/nacl/box"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"bntsovpn/internal/netconfig"
	"bntsovpn/internal/state"
)

type configResponse struct {
	Node    string `json:"node"`
	Payload string `json:"payload"` // base64 box.SealAnonymous(config, node public key)
	SHA256  string `json:"sha256"`  // hex sha256 of the PLAINTEXT config
}

// Handler serves GET /v1/config, authenticating by bearer token against the
// token hashes in state.yaml (re-read per request so revocations and token
// rotations take effect without a restart).
func Handler(dir string) http.Handler {
	statePath := filepath.Join(dir, "state.yaml")
	renderedDir := filepath.Join(dir, "rendered")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/config", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || token == "" {
			http.Error(w, "missing bearer token", http.StatusForbidden)
			return
		}
		st, err := state.Load(statePath)
		if err != nil {
			http.Error(w, "state unavailable", http.StatusInternalServerError)
			return
		}
		node := nodeByToken(st, token)
		if node == "" {
			http.Error(w, "unknown or revoked token", http.StatusForbidden)
			return
		}
		conf, err := os.ReadFile(filepath.Join(renderedDir, node+".conf"))
		if err != nil {
			http.Error(w, "rendered config unavailable", http.StatusInternalServerError)
			return
		}
		// Seal the config to the requesting node's WireGuard public key so the
		// transport no longer matters: even a public bind leaks nothing to
		// anyone holding only a (valid) token for another node.
		pubStr := st.Nodes[node].PublicKey
		if pubStr == "" {
			http.Error(w, "node has no public key in state", http.StatusInternalServerError)
			return
		}
		pub, err := wgtypes.ParseKey(pubStr)
		if err != nil {
			http.Error(w, "bad public key in state", http.StatusInternalServerError)
			return
		}
		pubArr := [32]byte(pub)
		sealed, err := box.SealAnonymous(nil, conf, &pubArr, rand.Reader)
		if err != nil {
			http.Error(w, "seal failed", http.StatusInternalServerError)
			return
		}
		sum := sha256.Sum256(conf)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(configResponse{
			Node:    node,
			Payload: base64.StdEncoding.EncodeToString(sealed),
			SHA256:  hex.EncodeToString(sum[:]),
		})
	})
	return mux
}

// nodeByToken looks up the node owning a presented token using constant-time
// comparison on the stored hashes.
func nodeByToken(st *state.State, token string) string {
	want := state.HashToken(token)
	for name, ns := range st.Nodes {
		if subtle.ConstantTimeCompare([]byte(ns.TokenHash), []byte(want)) == 1 {
			return name
		}
	}
	return ""
}

// CheckBind refuses wildcard and public bind addresses unless allowPublic is
// set (via `api_public: true` in config.yaml or the --allow-public flag).
// Loopback, private (RFC1918/ULA) and link-local addresses are always allowed,
// as is the admin node's VPN address regardless (it is always inside the mesh
// subnet).
func CheckBind(host string, adminVPN netip.Addr, allowPublic bool) error {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		// Hostname: resolve and check every address.
		ips, err := net.LookupIP(host)
		if err != nil {
			return fmt.Errorf("resolve bind address %q: %w", host, err)
		}
		if len(ips) == 0 {
			return fmt.Errorf("bind address %q resolves to nothing", host)
		}
		for _, ip := range ips {
			a, ok := netip.AddrFromSlice(ip)
			if !ok {
				continue
			}
			if err := checkAddr(a, adminVPN, allowPublic); err != nil {
				return fmt.Errorf("bind address %q resolves to %s: %w", host, a, err)
			}
		}
		return nil
	}
	return checkAddr(addr, adminVPN, allowPublic)
}

func checkAddr(addr, adminVPN netip.Addr, allowPublic bool) error {
	if addr == adminVPN || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() {
		return nil
	}
	if allowPublic {
		return nil
	}
	kind := "public"
	if addr.IsUnspecified() {
		kind = "wildcard"
	}
	return fmt.Errorf("refusing to bind %s address %s: the API would be reachable outside the VPN. "+
		"If you really want this, opt in with `api_public: true` in config.yaml or the --allow-public flag "+
		"(config payloads are sealed to each node's WireGuard key, tokens are still required)", kind, addr)
}

// Run starts the API server on addr. The socket is bound with IP_FREEBIND so
// the API may start before the WireGuard interface exists; it answers as soon
// as the tunnel address is up. allowPublic permits wildcard/public binds (see
// CheckBind).
func Run(dir, addr string, allowPublic bool) error {
	if err := CheckBind(addrOnly(addr), adminVPNAddr(dir), allowPublic); err != nil {
		return err
	}
	lc := net.ListenConfig{Control: freebind}
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	fmt.Printf("bntsovpn serve: listening on http://%s (dir %s)\n", addr, dir)
	host := addrOnly(addr)
	if a, err := netip.ParseAddr(host); err == nil && (a.IsUnspecified() || !(a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast())) {
		fmt.Fprintln(os.Stderr, "WARNING: the API is bound to a public/wildcard address and is reachable OUTSIDE the tunnel.")
		fmt.Fprintln(os.Stderr, "         Config payloads are sealed to each node's WireGuard key and bearer tokens are still")
		fmt.Fprintln(os.Stderr, "         required, but anyone on the internet can now probe this endpoint.")
	} else if !addrExists(host) {
		fmt.Println("note: the VPN address is not assigned locally yet (wg0 down?) — the API will answer once the tunnel is up")
	}
	return http.Serve(ln, Handler(dir))
}

// freebind sets IP_FREEBIND / IPV6_FREEBIND so the listener can bind an
// address that does not exist locally yet (the wg0 address before the tunnel
// is up).
func freebind(network, address string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		a, _ := netip.ParseAddr(addrOnly(address))
		if a.Is6() {
			serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_FREEBIND, 1)
		} else {
			serr = unix.SetsockoptInt(int(fd), unix.SOL_IP, unix.IP_FREEBIND, 1)
		}
	})
	if err != nil {
		return err
	}
	return serr
}

// addrExists reports whether the given host is assigned to any local interface.
func addrExists(host string) bool {
	want, err := netip.ParseAddr(host)
	if err != nil {
		return true // hostname: nothing to check
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return true
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr() == want {
				return true
			}
		}
	}
	return false
}

func addrOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// adminVPNAddr returns the admin node's tunnel address from state, or an
// invalid Addr when unavailable.
func adminVPNAddr(dir string) netip.Addr {
	cfg, err := netconfig.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		return netip.Addr{}
	}
	st, err := state.Load(filepath.Join(dir, "state.yaml"))
	if err != nil {
		return netip.Addr{}
	}
	ns := st.Nodes[cfg.Admin]
	if ns == nil {
		return netip.Addr{}
	}
	a, _ := netip.ParseAddr(ns.Address)
	return a
}
