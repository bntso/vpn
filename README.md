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
bntsovpn init                        # scaffold /etc/bntsovpn/config.yaml + state.yaml
$EDITOR /etc/bntsovpn/config.yaml    # define the mesh nodes
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

After editing `config.yaml`, run `bntsovpn apply` again on the admin machine;
agents pick up their new config on the next poll.

## Build from source

```sh
go build -o bntsovpn .
```

Requires Go 1.27+.
