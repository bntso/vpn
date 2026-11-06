# bntsovpn

Config-driven WireGuard hub-and-spoke mesh manager. A single `bntsovpn` binary
runs the control plane on the admin machine (`serve`) and the polling agent on
every node (`agent`). The WireGuard interface is managed directly via netlink —
no `wg-quick` required.

## Install

Install the binary on the admin machine and every node:

```sh
curl -fsSL https://raw.githubusercontent.com/bntso/vpn/main/install.sh | sudo bash
```

To run the admin initialization flow while installing:

```sh
curl -fsSL https://raw.githubusercontent.com/bntso/vpn/main/install.sh | sudo bash -s -- --init
```

Or install and join a node in one line (blob from `bntsovpn invite <node>` on the admin machine):

```sh
curl -fsSL https://raw.githubusercontent.com/bntso/vpn/main/install.sh | sudo bash -s -- '<join-blob>'
```

The script detects the architecture (linux/amd64, linux/arm64), downloads the
latest release binary, verifies its SHA-256 checksum, and installs it to
`/usr/local/bin/bntsovpn`. It also installs Bash completion to
`/usr/share/bash-completion/completions/bntsovpn` on Ubuntu and Arch Linux.

Install the Bash completion loader if it is not already present:

```sh
# Ubuntu
sudo apt install bash-completion

# Arch Linux
sudo pacman -S bash-completion
```

Start a new shell to enable completion, or run
`source /usr/share/bash-completion/completions/bntsovpn` in the current shell.

## Upgrade

Upgrade an installed binary to the latest release:

```sh
sudo bntsovpn upgrade
```

The command downloads the matching linux/amd64 or linux/arm64 release and
verifies its SHA-256 checksum before replacing the current executable. If the
installed binary already matches the release, it makes no changes. Otherwise,
it also refreshes the Bash completion and restarts the active `bntsovpn`
service. Legacy `bntsovpn-agent` and `bntsovpn-serve` services are also
recognized until the next `apply` or `join` migrates them to
`bntsovpn.service`. Restarting the service on the admin machine recreates its
WireGuard interface and briefly interrupts VPN connections.

## Usage

On the admin machine:

```sh
bntsovpn init --subnet 10.13.13.0/24  # scaffold /etc/bntsovpn/config.hcl + state.yaml
$EDITOR /etc/bntsovpn/config.hcl      # define the mesh nodes
bntsovpn apply                        # render configs, bring up wg0, install + start bntsovpn
bntsovpn invite <node>                # print a one-line join blob for a node
```

On each node:

```sh
bntsovpn join '<join-blob>'          # writes wg0 config, brings up the tunnel, installs + starts bntsovpn
```

Anytime:

```sh
bntsovpn status
```

On the control plane, this shows all mesh nodes. On an agent, it shows the
local interface, tunnel address, configured peers, and live handshake/traffic
when available; `config.hcl` is not required. Use `sudo` if needed to read the
root-only agent files. It also warns when the control plane has rejected the
node's token (revoked or rotated): the tunnel keeps running its last-known-good
config, but the node must be re-joined with a fresh `bntsovpn invite` blob.

After editing `config.hcl`, run `bntsovpn apply` again on the admin machine;
agents pick up their new config on the next poll.

## Configuration

The user-managed network configuration is `/etc/bntsovpn/config.hcl` by
default. Use `--dir <path>` or set `BNTSOVPN_DIR` to select a different
configuration and state directory. Run `bntsovpn init --subnet <cidr>` to
create a fully commented template; the subnet can be IPv4 or IPv6 and decides
the address family for the whole network.

The configuration uses HCL. Nodes are labeled blocks, and unmapped top-level
attributes can be reused as variables. References, `env("NAME")`, and
cross-block interpolation are resolved when the file loads:

```hcl
domain         = env("VPN_DOMAIN")
wireguard_port = 51820

name       = "bntso"
subnet     = "10.13.13.0/24"
admin      = "home"

node "home" {
  role        = "hub"
  endpoint    = "home.${domain}:${wireguard_port}"
  listen_port = wireguard_port
  aliases     = ["gateway", "files"]
}

node "phone" {
  role = "spoke"
  via  = "home"
}
```

Top-level settings:

- `name` (required): mesh name.
- `subnet` (required): canonical IPv4 or IPv6 network prefix. Node addresses
  must belong to this subnet and use the same address family.
- `admin` (required): name of the hub node that runs the control plane.
- `api_port` (optional): control-plane TCP port; defaults to `51821`.
- `api_bind` (optional): address for `serve` to bind, as a host or `host:port`.
  By default, it binds the admin node's VPN address and `api_port`.
- `api_public` (optional): set to `true` to explicitly allow a wildcard or
  public API bind address. Bearer tokens and sealed configuration payloads are
  still required. External access also requires forwarding the API TCP port.
- `api_url` (optional): API address written into join blobs. It defaults to
  `http://<admin VPN IP>:<api_port>`; set a public URL when nodes must reach
  the API without the tunnel, such as during recovery from a broken config.

Each `node "<name>"` block supports:

- `role` (required): `hub` or `spoke`.
- `address` (optional): fixed tunnel address inside `subnet`; omit it to
  auto-allocate an address.
- `endpoint` (required for hubs): publicly reachable IP or DNS host and
  WireGuard port in `host:port` form. It is not valid on spokes.
- `listen_port` (optional, hubs): WireGuard listen port; defaults to `51820`.
- `expose` (optional, hubs): list of additional network prefixes reachable
  through this hub.
- `nat_iface` (optional, hubs): interface used for IPv4 masquerading of exposed
  prefixes; defaults to `eth0`.
- `aliases` (optional): additional `/etc/hosts` names for the node. Node names
  and aliases must be single labels and case-insensitively unique across the mesh.
- `via` (required for spokes): primary hub, with full routes through the VPN.
  Other hubs route the spoke's traffic through this hub rather than creating a
  direct peer without an endpoint.
- `backup` (optional, spokes): backup hubs, with host routes only.

The configuration contains no secrets; `state.yaml` and `agent.yaml` remain
YAML and are tool-managed. The daemon additionally keeps a tool-managed
`agent-status.yaml` next to `agent.yaml` with operator-facing runtime state
(currently: token rejections, for the `status` warning).

### Hosts-based name resolution

`bntsovpn` maintains a marked block in `/etc/hosts` on every machine. The block
maps every node's VPN address to its node name and aliases. For the example
above:

```text
# BEGIN bntsovpn managed hosts
10.13.13.1 home gateway files
10.13.13.2 phone
# END bntsovpn managed hosts
```

All other hosts entries are preserved. Address, node-name, and alias changes
replace the managed block when each agent applies its updated configuration.
Do not edit inside the markers; add custom entries outside them. The service
does not run a DNS server or modify resolver settings.

Convert an existing YAML network configuration with:

```sh
bntsovpn convert-config  # reads config.yaml and creates config.hcl
```

The converter validates the configuration, refuses to overwrite an existing
`config.hcl`, and leaves the YAML input and state untouched. It emits literal
HCL values; you can add variables and `env()` expressions afterward. The old
YAML file is not loaded automatically.

Starting or restarting `bntsovpn` recreates the admin machine's `wg0`
from its rendered configuration, removing stale addresses, routes, peers, and
hook state. This briefly interrupts the hub's VPN connections. While running,
`serve` checks every 30 seconds and restores `wg0` from the rendered configuration
if the interface disappears. Agents reconcile once after startup, then apply
changed configurations on later polls without recreating existing interfaces.
Both paths synchronize the managed hosts block without changing resolver settings,
and agents re-assert that block on every poll. A token rejected by the control
plane (HTTP 401/403) never heals by retrying: the agent keeps the last-known-good
config running, records the rejection in `agent-status.yaml`, reports it via
`bntsovpn status`, and clears it after a successful fetch — re-join the node
with a fresh invite blob to recover.

## Build from source

```sh
go build -o bntsovpn .
```

Requires Go 1.27+.
