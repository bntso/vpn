// Package state manages state.yaml: the tool-managed, secret-bearing
// counterpart of config.yaml. It stores per-node keypairs, the (pinned or
// auto-allocated) tunnel address, and the SHA-256 hash of the node's API
// bearer token. Plaintext tokens are never persisted; `invite` rotates a
// node's token and hands out the new plaintext.
package state

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"gopkg.in/yaml.v3"

	"bntsovpn/internal/netconfig"
)

// NodeState is the tool-managed record for one node.
type NodeState struct {
	PrivateKey string `yaml:"private_key"`
	PublicKey  string `yaml:"public_key"`
	Address    string `yaml:"address"`
	TokenHash  string `yaml:"token_hash"`
}

// State is the root of state.yaml.
type State struct {
	AddressCursor string                `yaml:"address_cursor,omitempty"`
	Nodes         map[string]*NodeState `yaml:"nodes"`
}

// Load reads state.yaml. A missing file yields an empty state.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &State{Nodes: map[string]*NodeState{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var st State
	if err := yaml.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if st.Nodes == nil {
		st.Nodes = map[string]*NodeState{}
	}
	return &st, nil
}

// Save writes state.yaml with mode 0600.
func (s *State) Save(path string) error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// HashToken returns the stored form of a bearer token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewToken generates a fresh bearer token and its stored hash.
func NewToken() (plain, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	plain = base64.RawURLEncoding.EncodeToString(buf)
	return plain, HashToken(plain), nil
}

// RotateToken replaces the node's token and returns the new plaintext.
func (s *State) RotateToken(node string) (string, error) {
	ns := s.Nodes[node]
	if ns == nil {
		return "", fmt.Errorf("node %q has no state", node)
	}
	plain, hash, err := NewToken()
	if err != nil {
		return "", err
	}
	ns.TokenHash = hash
	return plain, nil
}

// NodeByTokenHash finds the node owning a presented plaintext token, or "".
func (s *State) NodeByToken(token string) string {
	h := HashToken(token)
	for name, ns := range s.Nodes {
		if ns.TokenHash == h {
			return name
		}
	}
	return ""
}

// Reconcile brings the state in line with the (validated) config: nodes
// removed from the config lose their state (revoking tokens), new nodes get a
// keypair, token and address, and pinned-address changes are picked up.
// Existing keys/addresses/tokens are never regenerated otherwise.
func Reconcile(cfg *netconfig.Config, st *State) (added, removed []string, err error) {
	subnet, err := cfg.SubnetPrefix()
	if err != nil {
		return nil, nil, err
	}

	for name := range st.Nodes {
		if cfg.NodeByName(name) == nil {
			delete(st.Nodes, name)
			removed = append(removed, name)
		}
	}

	for _, n := range cfg.Nodes {
		ns := st.Nodes[n.Name]
		if ns == nil {
			ns = &NodeState{}
			st.Nodes[n.Name] = ns
			added = append(added, n.Name)
		}
		if ns.PrivateKey == "" {
			key, err := wgtypes.GeneratePrivateKey()
			if err != nil {
				return nil, nil, fmt.Errorf("node %q: generate key: %w", n.Name, err)
			}
			ns.PrivateKey = key.String()
			pub := key.PublicKey()
			ns.PublicKey = pub.String()
		}
		if ns.TokenHash == "" {
			_, hash, err := NewToken()
			if err != nil {
				return nil, nil, err
			}
			ns.TokenHash = hash
		}
		if n.Address != "" {
			// Pinned address wins; config validation already checked it.
			ns.Address = n.Address
		}
	}

	// Auto-allocate addresses for nodes that still have none.
	used := map[string]bool{}
	for _, ns := range st.Nodes {
		if ns.Address != "" {
			used[ns.Address] = true
		}
	}
	for _, n := range cfg.Nodes {
		ns := st.Nodes[n.Name]
		if ns.Address != "" {
			continue
		}
		addr, err := AllocateAddress(subnet, used, st.AddressCursor)
		if err != nil {
			return nil, nil, fmt.Errorf("node %q: %w", n.Name, err)
		}
		ns.Address = addr.String()
		st.AddressCursor = addr.String()
		used[ns.Address] = true
	}
	return added, removed, nil
}

// AllocateAddress finds the next free address in the subnet, starting after
// the cursor (or the network address). The network address is always skipped;
// for IPv4 subnets wider than /31 the broadcast address is skipped too.
func AllocateAddress(subnet netip.Prefix, used map[string]bool, cursor string) (netip.Addr, error) {
	start := subnet.Addr().Next()
	if cursor != "" {
		if cur, err := netip.ParseAddr(cursor); err == nil && subnet.Contains(cur) {
			start = cur.Next()
		}
	}
	if !start.IsValid() || !subnet.Contains(start) {
		start = subnet.Addr().Next()
	}

	var bcast netip.Addr
	if subnet.Addr().Is4() && subnet.Bits() < 31 {
		bcast = lastAddr4(subnet)
	}

	addr := start
	for i := 0; i < 1<<20; i++ {
		if !subnet.Contains(addr) {
			break
		}
		if addr != subnet.Addr() && addr != bcast && !used[addr.String()] {
			return addr, nil
		}
		addr = addr.Next()
	}
	return netip.Addr{}, fmt.Errorf("subnet %s exhausted: no free address", subnet)
}

func lastAddr4(p netip.Prefix) netip.Addr {
	base := p.Masked().Addr().As4()
	hostBits := 32 - p.Bits()
	var last uint32
	for i := 0; i < hostBits; i++ {
		last |= 1 << i
	}
	v := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	v += last
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}
