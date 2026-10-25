// Package invite encodes and decodes the one-line join blob: gzip + base64 of
// a small JSON struct carrying everything a fresh node needs. The blob
// contains secrets (the node's private key inside the rendered config, plus
// its API bearer token) and must be treated accordingly.
package invite

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

// Blob is the join payload produced by `invite` and consumed by `join`.
type Blob struct {
	Node       string `json:"node"`
	Role       string `json:"role"`
	Config     string `json:"config"`      // rendered wg0.conf (contains the private key)
	APIAddr    string `json:"api_addr"`    // http://<admin VPN IP>:<api_port> or api_url override
	Token      string `json:"token"`       // plaintext bearer token
	PrivateKey string `json:"private_key"` // node's WireGuard private key; the agent needs it to unseal configs
	Hub        bool   `json:"hub"`         // join enables IP forwarding on hubs
}

// Encode serializes a blob to a single-line string.
func Encode(b *Blob) (string, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

// Decode parses a blob string.
func Decode(s string) (*Blob, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("blob is not valid base64: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("blob is not gzip data: %w", err)
	}
	defer zr.Close()
	plain, err := io.ReadAll(io.LimitReader(zr, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("decompress blob: %w", err)
	}
	var b Blob
	if err := json.Unmarshal(plain, &b); err != nil {
		return nil, fmt.Errorf("blob payload is not valid JSON: %w", err)
	}
	if b.Node == "" || b.Config == "" {
		return nil, fmt.Errorf("blob is missing node or config")
	}
	return &b, nil
}
