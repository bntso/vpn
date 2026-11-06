# bntsovpn operations

Current live configuration and verification checklist, recorded 2026-10-30.
This file contains no private keys, tokens, join blobs, or rendered WireGuard
configuration.

## Current topology

The control plane runs on `home` (`pongolos`). All nodes use `wg0` for
bntsovpn. The old Tinc interface is named `bntso`; do not confuse the two.

WireGuard subnet: `fd7a:3b9c:1a2b::/64`
API: `http://[fd7a:3b9c:1a2b::1]:51880` (an unauthenticated request should
return HTTP 404, not a network error)

| Node | Legacy name | Role | VPN address | Primary hub | Public endpoint |
| --- | --- | --- | --- | --- | --- |
| `home` | `pongolos` | hub/admin | `fd7a:3b9c:1a2b::1` | - | `home.bntso.com:51821` |
| `linode1` | `server01` | hub | `fd7a:3b9c:1a2b::3` | - | `104.237.154.95:51821` |
| `ricardo-laptop` | - | spoke | `fd7a:3b9c:1a2b::2` | `home` | - |
| `caster` | - | spoke | `fd7a:3b9c:1a2b::4` | `home` | - |

Both hubs listen on UDP 51821. `caster` intentionally uses `home`; its direct
UDP path to `linode1` did not work. Hubs route spokes through each spoke's
primary hub rather than creating direct peers without endpoints.

The deployed routing fix is commit `a2c945c`, released as `build-11`.

## Hosts-based name resolution

`bntsovpn` maintains this block in `/etc/hosts` on every machine and preserves
entries outside the markers:

```text
# BEGIN bntsovpn managed hosts
fd7a:3b9c:1a2b::1 home pongolos postgres jenkins files files-vpn
fd7a:3b9c:1a2b::2 ricardo-laptop
fd7a:3b9c:1a2b::3 linode1 server01
fd7a:3b9c:1a2b::4 caster
# END bntsovpn managed hosts
```

Do not modify the NetworkManager-generated `wg0` connection profile: doing so
can deactivate the externally managed interface.

## Service configuration

On `home`/`pongolos`:

- SSH: TCP 22.
- Public Caddy: TCP 80 and 443 on the LAN/public-forwarded address.
- Internal Caddy: TCP 80 and 443 on `fd7a:3b9c:1a2b::1`.
- PostgreSQL through the Incus proxy: TCP 5432 on
  `fd7a:3b9c:1a2b::1` and localhost.
- bntsovpn API: TCP 51880 on `fd7a:3b9c:1a2b::1`.
- WireGuard: UDP 51821.
- Tinc, until retired: UDP/TCP 655.

The Incus source is `/root/incus` on `pongolos`. `services.hcl` must retain
the bntsovpn address in the PostgreSQL and internal Caddy `ports` entries.
Apply changes with `containertool -c services.hcl` from that directory. The
last apply recreated the service containers successfully. `files.bntso.com`
and `files-vpn.bntso.com` return HTTP 200 through bntsovpn. Jenkins currently
returns 502 because there is no running `jenkins` Incus instance.

On `linode1`/`server01`:

- SSH: TCP 22 on `wg0`.
- NTP: UDP 123 on `wg0`; upstream pools are public Ubuntu NTP pools.
- Public web services: TCP 80 and 443.
- WireGuard: UDP 51821.
- Tinc, until retired: UDP/TCP 655.

The laptop can use the managed hosts names `pongolos` and `server01` directly
for SSH.

## Firewall requirements

On `home`:

- Allow UDP 51821 from the internet for WireGuard.
- Allow TCP 5432 inbound on `wg0`.
- Allow routed traffic entering and leaving `wg0`. This is required for TCP
  between nodes connected to different hubs; successful ping tests alone do
  not prove it works.
- Keep the default-deny incoming and routed policies for other traffic.

On `linode1`:

- Allow UDP 51821 from the internet for WireGuard.
- Allow TCP 22 and UDP 123 inbound on `wg0`.
- Allow routed traffic entering and leaving `wg0`.

On `caster`:

- UFW is installed, active, and enabled at boot. Incoming and routed traffic
  default to deny; outgoing traffic defaults to allow.
- Preserve the existing TCP 22, 2222, 80, 443, 5000, and 8080 service rules,
  inbound traffic on `wg0`, and routed traffic to and from `docker0`.
- Temporary management access still requires `admin session open`; close it
  with `admin session close` afterward.

UFW is active and enabled at boot on `pongolos`, `server01`, and `caster`.
The laptop is intentionally excluded.

The router was configured to forward UDP 51821 and TCP 51880 to `10.0.0.10`.
The API itself binds only to the admin VPN IPv6 address.

Rules for the old `bntso` interface apply to Tinc, not bntsovpn. Rules for
bntsovpn must name `wg0`.

## Restart verification

Run these checks after rebooting nodes or changing hub configuration.

1. Check services and handshakes:

   ```sh
   sudo systemctl is-active bntsovpn.service
   sudo bntsovpn status
   ssh pongolos 'sudo -n bntsovpn status'
   ```

   The hub should show recent handshakes and nonzero traffic for every node.
   `caster` should show `home` as its primary hub.

2. Check managed hosts names and public DNS from the laptop and another node:

   ```sh
   getent ahostsv6 home
   getent ahostsv6 linode1
   getent ahostsv6 caster
   getent ahostsv6 pongolos
   getent ahostsv6 server01
   getent ahostsv6 postgres
   getent ahostsv6 files
   getent ahostsv4 example.com
   ```

3. Check connectivity in both directions, including names:

   ```sh
   ping -c 2 home
   ping -c 2 linode1
   ping -c 2 caster
   ssh pongolos true
   ssh server01 true
   ```

   From `caster`, also ping `home`, `linode1`, and
   `fd7a:3b9c:1a2b::2`. Use a TCP test such as SSH for cross-hub checks; ping
   can succeed while forwarded TCP is blocked.

4. Check the private API from an agent:

   ```sh
   curl -sS -o /dev/null -w '%{http_code}\n' \
     'http://[fd7a:3b9c:1a2b::1]:51880/'
   ```

   Expected: `404`.

5. Check PostgreSQL without printing credentials:

   ```sh
   python3 -c 'import socket; s=socket.create_connection(("fd7a:3b9c:1a2b::1",5432),5); print("PostgreSQL TCP OK"); s.close()'
   ```

6. Check Files through the bntsovpn address while retaining the Caddy site
   name and TLS name:

   ```sh
   curl -sS -o /dev/null -w '%{http_code}\n' \
     --resolve files.bntso.com:443:fd7a:3b9c:1a2b::1 \
     https://files.bntso.com/
   curl -sS -o /dev/null -w '%{http_code}\n' \
     --resolve files-vpn.bntso.com:443:fd7a:3b9c:1a2b::1 \
     https://files-vpn.bntso.com/
   ```

   Expected: `200` for GET requests. HEAD requests returned 404 during the
   migration and are not the correct health check.

7. Check NTP over bntsovpn:

   ```sh
   python3 -c 'import socket; s=socket.socket(socket.AF_INET6,socket.SOCK_DGRAM); s.settimeout(5); s.sendto(b"\x23"+bytes(47),("fd7a:3b9c:1a2b::3",123)); data,_=s.recvfrom(512); print("NTP OK",len(data),"bytes")'
   ```

   Expected: `NTP OK 48 bytes`.

8. After Tinc is disabled, confirm that bntsovpn still passes every check and
   that nothing required the old `fd9b:cf10:ac00::/64` addresses.

## Troubleshooting notes

- If a VPN name does not resolve, check that machine's managed `/etc/hosts`
  block and confirm the address matches the node's current VPN address. Also
  check for a conflicting unmanaged entry earlier in the file.
- If ping between hubs works but SSH or another TCP service times out, check
  the UFW `route allow` rule for `wg0` on the forwarding hub.
- A disconnected agent cannot fetch changes through the private API. After
  changing its endpoint or primary hub, a fresh join may be required. Invites
  rotate that node's token; generate and use them one node at a time, and never
  record them here.
- If `caster` loses its handshake, confirm it is still using `home`, not
  `linode1`.
- Do not expose or commit `state.yaml`, `agent.yaml`, rendered configs, private
  keys, tokens, or join blobs.

## Tinc retirement state

Tinc was stopped and disabled on 2026-10-30. `tincgen.service` is inactive and
disabled on `pongolos` and `server01`; the laptop's manually started `tincd`
was stopped. Tinc remains installed for rollback. Port 655 is closed in
`server01`'s UFW configuration (verified over public IPv4 and IPv6), and its
Linode cloud-firewall rule was removed manually. Port 655 is also closed in
`pongolos`'s UFW configuration, verified from `server01` using the public
address. `casterweb` and `casterdb` are legacy and do not need migration; there
is now only `caster`.

All checks passed with Tinc stopped: bntsovpn services and handshakes, name
resolution, node ping and SSH, API, PostgreSQL, both Files sites, and NTP.
Observe the system before uninstalling Tinc. A short
throughput comparison favored bntsovpn: approximately 48.2/41.9 Mbit/s
upload/download versus Tinc's 38.1/25.4 Mbit/s between the laptop and
`pongolos`.
