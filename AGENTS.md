# AGENTS.md

Guidance for coding agents working in this repository.

## Project overview

`bntsovpn` is a Linux-only, config-driven WireGuard hub-and-spoke manager. One
Go binary provides the admin control plane (`serve`) and node daemon (`agent`).
It manages kernel WireGuard interfaces directly with netlink and wgctrl; it
does not use `wg-quick`.

The control plane renders per-node configuration. On startup, `serve` recreates
the admin WireGuard interface from its rendered configuration to remove stale
kernel state. With `dns_domain` configured, it also serves private node/alias
DNS records on the admin VPN address and forwards other queries upstream.
Agents poll with bearer tokens, unseal responses with their WireGuard private
keys, and apply changed configuration while preserving the last-known-good live
setup on failure.

## Repository layout

- `main.go`: binary entry point.
- `internal/cli`: Cobra commands and shared config-directory paths.
- `internal/netconfig`: user-managed `config.hcl` schema, validation, and legacy YAML conversion.
- `internal/state`: secret-bearing `state.yaml`, key/token lifecycle, and address allocation.
- `internal/config`: deterministic per-node WireGuard config rendering and routing policy.
- `internal/server`: authenticated control-plane HTTP API, private DNS service, and bind-address safety checks.
- `internal/agent`: polling, payload unsealing, staging, and live application.
- `internal/device`: WireGuard/netlink lifecycle, routes, resolvconf DNS/search-domain registration, and hooks.
- `internal/invite`: gzip + base64 join blobs containing secrets.
- `systemd`: service units and their embedded Go source.
- `install-*.sh`: Linux amd64/arm64 release installers.
- `installers_test.go`: non-destructive installer download, checksum, completion, and corruption tests.
- `.github/workflows/release.yml`: tests and builds every push, then publishes a `build-N` GitHub release.
- `dist`: generated release binaries and checksums; do not edit by hand.

## Build and verification

Requires the Go version declared in `go.mod` (currently Go 1.27.3). Run from
the repository root after changes:

```sh
gofmt -w <changed-go-files>
go test ./...
go vet ./...
go build -o bntsovpn .
```

For shell installer changes, update `installers_test.go` when the download,
checksum, or install flow changes. Also run `bash -n install-agent.sh
install-serve.sh` and exercise only non-destructive paths.

Use `go build ./...` instead of the final command when a build check should
not replace the existing `bntsovpn` binary.

## Invariants and safety

- `config.hcl` contains no secrets. Private keys and token hashes belong in
  `state.yaml`; agent tokens and private keys belong in `agent.yaml`.
- Never print, commit, or expose private keys, bearer tokens, join blobs,
  rendered configs, `state.yaml`, or `agent.yaml` in logs or examples.
- Secret-bearing files must retain restrictive permissions. Plaintext API
  tokens are never persisted in `state.yaml`.
- Preserve existing node keys, addresses, and tokens during reconciliation;
  only explicit lifecycle operations may rotate or revoke them.
- API responses must remain sealed to the requesting node's WireGuard public
  key. Token lookup must retain constant-time hash comparison.
- Preserve split-tunnel behavior: never emit or install a default route.
  Maintain hub/spoke `AllowedIPs`, backup-hub host routes, and non-overlapping
  WireGuard cryptokey routing.
- Preserve last-known-good behavior: fetch, authenticate, unseal, and validate
  before changing the running interface. Poll or apply failures must not tear
  down a working tunnel.
- Avoid duplicate NAT rules when reconciling hooks; retain old `PostDown` and
  new `PostUp` handling when hooks change.
- Device operations are privileged and mutate real interfaces, routes, DNS,
  and iptables. Do not run `apply`, `join`, `serve`, `agent`, installers, or
  other live network operations as part of routine verification unless the
  user explicitly requests it and provides a suitable environment.
- Do not run installers or service-management commands merely to test changes.

## Change guidelines

- Keep the single-binary architecture and existing package boundaries.
- Use the existing Cobra, hclconfig/HCL, YAML, netlink, wgctrl, and NaCl box
  APIs rather than introducing overlapping dependencies. User-managed network
  configuration is HCL; state and agent files remain YAML.
- Validate configuration before rendering, reconciling state, or touching the
  system. Return contextual errors with node names and paths where useful.
- Keep generated WireGuard output stable where possible; incidental changes
  trigger every agent to apply a new config.
- Whenever configuration fields, defaults, validation, or behavior change,
  update and verify the `bntsovpn init` generated template in the same change.
  Keep every supported user-managed field and its current guidance represented.
- Before finishing every change, check whether documentation or this
  `AGENTS.md` needs an update. Update the affected files in the same change;
  do not defer documentation work or leave instructions describing old behavior.
- Keep `README.md`, service units, installer comments, and package docs aligned
  with behavior changes. Update `AGENTS.md` when commands, architecture,
  workflows, conventions, or safety guidance change.
- The default runtime directory is `/etc/bntsovpn`; use `--dir` or
  `BNTSOVPN_DIR` for non-system fixtures and manual inspection.
