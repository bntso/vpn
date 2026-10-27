# bntsovpn

Config-driven WireGuard hub-and-spoke mesh manager. A single `bntsovpn` binary
runs the control plane on the admin machine (`serve`) and the polling agent on
every node (`agent`). The WireGuard interface is managed directly via netlink —
no `wg-quick` required.

## Install

Admin / control-plane machine:

```sh
curl -fsSL https://raw.githubusercontent.com/bntso/vpn/main/install-serve.sh | sudo bash
```

Each node (agent):

```sh
curl -fsSL https://raw.githubusercontent.com/bntso/vpn/main/install-agent.sh | sudo bash
```

Or install and join in one line (blob from `bntsovpn invite <node>` on the admin machine):

```sh
curl -fsSL https://raw.githubusercontent.com/bntso/vpn/main/install-agent.sh | sudo bash -s -- '<join-blob>'
```

Both scripts detect the architecture (linux/amd64, linux/arm64), download the
latest release binary, verify its SHA-256 checksum, and install it to
`/usr/local/bin/bntsovpn`. They also install Bash completion to
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

## Usage

On the admin machine:

```sh
bntsovpn init                        # scaffold /etc/bntsovpn/config.hcl + state.yaml
$EDITOR /etc/bntsovpn/config.hcl     # define the mesh nodes
bntsovpn apply                       # render configs, bring up wg0, install + start bntsovpn-serve
bntsovpn invite <node>               # print a one-line join blob for a node
```

On each node:

```sh
bntsovpn join '<join-blob>'          # writes wg0 config, brings up the tunnel, installs + starts bntsovpn-agent
```

Anytime:

```sh
bntsovpn status                      # mesh nodes; live handshake/traffic on a live node
```

After editing `config.hcl`, run `bntsovpn apply` again on the admin machine;
agents pick up their new config on the next poll.

## Configuration

The user-managed network configuration uses HCL. Nodes are labeled blocks, and
unmapped top-level attributes can be reused as variables. References,
`env("NAME")`, and cross-block interpolation are resolved when the file loads:

```hcl
domain         = env("VPN_DOMAIN")
wireguard_port = 51820

name       = "bntso"
subnet     = "10.13.13.0/24"
admin      = "home"
dns_domain = "bntso.internal"

node "home" {
  role        = "hub"
  endpoint    = "home.${domain}:${wireguard_port}"
  listen_port = wireguard_port
  dns         = "192.168.1.1"
  aliases     = ["gateway", "files"]
}

node "phone" {
  role = "spoke"
  via  = "home"
}
```

Run `bntsovpn init` for a fully commented template. The configuration contains
no secrets; `state.yaml` and `agent.yaml` remain YAML and are tool-managed.

### Private DNS

With `dns_domain` set, `serve` listens for DNS on the admin node's VPN address
(UDP/TCP port 53). Every node name and alias resolves to its current tunnel
address. In the example above, `home.bntso.internal`,
`gateway.bntso.internal`, and `files.bntso.internal` all resolve to `home`.
The search domain also makes short names such as `gateway` work.

The admin node's `dns` is the upstream resolver for names outside the private
zone; it must not be the admin VPN address. Without `dns_domain`, `dns` retains
its original behavior and is pushed directly to every node.

Nodes register the DNS server and search domain through `resolvconf`, so install
`openresolv` (or another compatible `resolvconf` implementation) on every node.
Port 53 must be available on the admin VPN address. Run `bntsovpn apply` after
changing nodes or aliases; the running DNS service reads the updated records
without a restart. Avoid `.local`, which is reserved for mDNS.

Convert an existing YAML network configuration with:

```sh
bntsovpn convert-config  # reads config.yaml and creates config.hcl
```

The converter validates the configuration, refuses to overwrite an existing
`config.hcl`, and leaves the YAML input and state untouched. It emits literal
HCL values; you can add variables and `env()` expressions afterward. The old
YAML file is not loaded automatically.

Starting or restarting `bntsovpn-serve` recreates the admin machine's `wg0`
from its rendered configuration, removing stale addresses, routes, peers, DNS,
and hook state. This briefly interrupts the hub's VPN connections. Agent polls
continue to update existing interfaces without recreating them.

## Build from source

```sh
go build -o bntsovpn .
```

Requires Go 1.27+.
