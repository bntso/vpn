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
`/usr/local/bin/bntsovpn`.

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

name   = "bntso"
subnet = "10.13.13.0/24"
admin  = "home"

node "home" {
  role        = "hub"
  endpoint    = "home.${domain}:${wireguard_port}"
  listen_port = wireguard_port
}

node "phone" {
  role = "spoke"
  via  = "home"
}
```

Run `bntsovpn init` for a fully commented template. The configuration contains
no secrets; `state.yaml` and `agent.yaml` remain YAML and are tool-managed.

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
