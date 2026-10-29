// Package agent implements the pull-based node daemon: reconcile after startup,
// then poll the control plane and apply configurations when their hashes change.
// The agent manages the WireGuard interface directly (netlink + wgctrl via
// package device — no wg-quick). The fetch and hash comparison always happen
// before any system call; a failed live apply is logged and the previous
// live config keeps running (last-known-good).
package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/nacl/box"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"gopkg.in/yaml.v3"

	"bntsovpn/internal/device"
)

// Config is the agent's runtime configuration, normally read from
// agent.yaml (written by `join`) with flag overrides.
type Config struct {
	Node       string `yaml:"node"`
	Token      string `yaml:"token"`
	API        string `yaml:"api"`       // http://<admin VPN IP>:<api_port> or api_url override
	ConfigPath string `yaml:"config"`    // wg0.conf path the agent manages
	Interface  string `yaml:"interface"` // defaults to wg0
	// PrivateKey is the node's WireGuard private key, needed to unseal config
	// payloads. Written by join; the file is 0600.
	PrivateKey string `yaml:"private_key"`
}

// LoadConfig reads an agent.yaml file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.API = normalizeAPI(cfg.API)
	return &cfg, nil
}

func normalizeAPI(api string) string {
	scheme, rest, ok := strings.Cut(api, "://")
	if !ok {
		return api
	}
	authority := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		authority = rest[:i]
	}
	if strings.HasPrefix(authority, "[") || strings.Count(authority, ":") < 2 {
		return api
	}
	i := strings.LastIndexByte(authority, ':')
	host, port := authority[:i], authority[i+1:]
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return api
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return api
	}
	return scheme + "://" + netip.AddrPortFrom(addr, uint16(portNumber)).String() + rest[len(authority):]
}

// SaveConfig writes agent.yaml with mode 0600 (it contains the token).
func (c *Config) SaveConfig(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (c *Config) defaults() {
	if c.Interface == "" {
		c.Interface = "wg0"
	}
}

func (c *Config) validate() error {
	c.defaults()
	if c.Token == "" {
		return fmt.Errorf("agent: token is required")
	}
	if c.API == "" {
		return fmt.Errorf("agent: API address is required")
	}
	if c.ConfigPath == "" {
		return fmt.Errorf("agent: config path is required")
	}
	if c.PrivateKey == "" {
		return fmt.Errorf("agent: private_key is required (re-run join with a fresh invite blob) — needed to unseal config payloads")
	}
	return nil
}

type fetchResult struct {
	Node    string `json:"node"`
	Payload string `json:"payload"` // base64 box.SealAnonymous(config, node public key)
	SHA256  string `json:"sha256"`  // hex sha256 of the PLAINTEXT config
}

// fetch retrieves the node's own sealed config from the control plane. No
// system state is touched here.
func (c *Config) fetch(client *http.Client) (*fetchResult, error) {
	url := strings.TrimRight(c.API, "/") + "/v1/config"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var fr fetchResult
	if err := json.Unmarshal(body, &fr); err != nil {
		return nil, fmt.Errorf("decode API response: %w", err)
	}
	return &fr, nil
}

// unseal opens the sealed payload with the node's private key and verifies it
// against the plaintext hash.
func (c *Config) unseal(fr *fetchResult) ([]byte, error) {
	priv, err := wgtypes.ParseKey(c.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("agent: invalid private_key: %w", err)
	}
	privArr := [32]byte(priv)
	pub := priv.PublicKey()
	pubArr := [32]byte(pub)
	sealed, err := base64.StdEncoding.DecodeString(fr.Payload)
	if err != nil {
		return nil, fmt.Errorf("payload is not valid base64: %w", err)
	}
	plain, ok := box.OpenAnonymous(nil, sealed, &pubArr, &privArr)
	if !ok {
		return nil, fmt.Errorf("could not unseal config payload (wrong private key? re-run join with a fresh invite blob)")
	}
	sum := sha256.Sum256(plain)
	if hex.EncodeToString(sum[:]) != fr.SHA256 {
		return nil, fmt.Errorf("unsealed config does not match the server-provided sha256")
	}
	return plain, nil
}

// ensureInterface brings the WireGuard interface up from the staged config
// when it is missing. Kernel interfaces do not survive a reboot, and the API
// is reached through the tunnel — so without this the agent could never
// fetch after a reboot (this was previously wg-quick@wg0's job at boot).
// Best-effort: failures are logged and retried on the next poll cycle.
func (c *Config) ensureInterface() {
	if os.Geteuid() != 0 || device.InterfaceExists(c.Interface) {
		return
	}
	if _, err := os.Stat(c.ConfigPath); err != nil {
		return // nothing staged yet (join will bring the interface up)
	}
	// oldConfPath == newConfPath: hooks are identical, so they run only if
	// the interface was just created (fresh NAT rules needed after boot).
	if err := device.Up(c.Interface, c.ConfigPath, c.ConfigPath); err != nil {
		fmt.Printf("warning: could not bring %s up from the staged config: %v\n", c.Interface, err)
	} else {
		fmt.Printf("brought %s up from the staged config\n", c.Interface)
	}
}

// Once fetches and reconciles the live configuration, even when its hash is
// unchanged. A failed sync is tolerated (logged); the running interface keeps
// its last-known-good configuration.
func (c *Config) Once() error {
	return c.once(true)
}

func (c *Config) once(force bool) error {
	if err := c.validate(); err != nil {
		return err
	}
	c.ensureInterface()
	client := &http.Client{Timeout: 15 * time.Second}

	fr, err := c.fetch(client)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	fmt.Printf("fetched sealed config for node %q from %s (sha256 %s, %d sealed bytes)\n", fr.Node, c.API, fr.SHA256, len(fr.Payload))

	conf, err := c.unseal(fr)
	if err != nil {
		return err
	}
	fmt.Printf("unsealed %d bytes with the node's WireGuard key\n", len(conf))

	hashPath := c.ConfigPath + ".sha256"
	last, _ := os.ReadFile(hashPath)
	unchanged := strings.TrimSpace(string(last)) == fr.SHA256
	if unchanged && !force {
		// Hash unchanged, but a failed ensureInterface above (or an
		// interface that flapped since) may still need the staged config.
		c.ensureInterface()
		fmt.Println("hash unchanged: config already up to date, nothing to do")
		return nil
	}
	if unchanged {
		fmt.Println("hash unchanged: reconciling live configuration")
	} else {
		fmt.Println("hash changed: applying new config")
	}

	if len(conf) == 0 || !strings.Contains(string(conf), "[Interface]") {
		return fmt.Errorf("unsealed config looks invalid, keeping last-known-good")
	}

	if err := os.MkdirAll(filepath.Dir(c.ConfigPath), 0o755); err != nil {
		return err
	}
	// Keep the previous conf for hook diffing: device.Up runs PostDown from
	// it when the NAT rules changed, so iptables rules are never duplicated.
	prevPath := c.ConfigPath + ".prev"
	if old, err := os.ReadFile(c.ConfigPath); err == nil {
		if err := os.WriteFile(prevPath, old, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", prevPath, err)
		}
	}
	if err := os.WriteFile(c.ConfigPath, conf, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", c.ConfigPath, err)
	}
	fmt.Printf("wrote %s (0600)\n", c.ConfigPath)

	// Best-effort live apply. Fails harmlessly on unprivileged machines
	// (e.g. smoke tests); the staged file is picked up on the next run as
	// root and the running interface keeps its last-known-good config.
	if os.Geteuid() != 0 {
		fmt.Println("note: not root — config staged on disk, skipping live apply")
	} else if err := device.Up(c.Interface, c.ConfigPath, prevPath); err != nil {
		fmt.Printf("warning: live apply on %s failed: %v (file is staged; last-known-good still live)\n", c.Interface, err)
	} else {
		fmt.Printf("applied live to %s (interface managed directly, no wg-quick)\n", c.Interface)
	}

	if err := os.WriteFile(hashPath, []byte(fr.SHA256+"\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", hashPath, err)
	}
	return nil
}

// Run is the daemon loop: reconcile after startup, then poll every interval
// plus jitter and apply changed hashes. Any error leaves the last-known-good
// config live.
func (c *Config) Run(interval time.Duration) error {
	if err := c.validate(); err != nil {
		return err
	}
	fmt.Printf("bntsovpn agent: node %q, polling %s every %s\n", c.Node, c.API, interval)
	failures := 0
	force := true
	for {
		if err := c.once(force); err != nil {
			failures++
			fmt.Printf("agent: %v (keeping last-known-good)\n", err)
		} else {
			failures = 0
			force = false
		}
		delay := interval + time.Duration(rand.Int63n(int64(interval/4+1)))
		if failures > 0 {
			backoff := interval << min(failures-1, 4) // up to 16x
			if backoff > 15*time.Minute {
				backoff = 15 * time.Minute
			}
			delay = backoff
		}
		time.Sleep(delay)
	}
}
