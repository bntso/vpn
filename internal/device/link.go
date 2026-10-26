// Interface lifecycle management via rtnetlink: create the kernel WireGuard
// link, assign its address, reconcile routes, and run the PostUp/PostDown
// hooks a rendered conf carries. This replaces wg-quick; agents preserve the
// persistent kernel interface across restarts, while serve recreates it on start.
package device

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// DefaultMTU matches the wg-quick default for WireGuard links.
const DefaultMTU = 1420

// EnsureInterface creates the named WireGuard link if missing (MTU 1420, link
// up) and reports whether it created it. An existing link is left untouched.
func EnsureInterface(name string) (bool, error) {
	if _, err := netlink.LinkByName(name); err == nil {
		return false, nil
	}
	attrs := netlink.NewLinkAttrs()
	attrs.Name = name
	attrs.MTU = DefaultMTU
	if err := netlink.LinkAdd(&netlink.Wireguard{LinkAttrs: attrs}); err != nil {
		if errors.Is(err, syscall.EOPNOTSUPP) {
			return false, fmt.Errorf("create %s: kernel WireGuard support missing (load the module: modprobe wireguard): %w", name, err)
		}
		return false, fmt.Errorf("create %s: %w", name, err)
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		return true, fmt.Errorf("lookup %s after create: %w", name, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return true, fmt.Errorf("bring %s up: %w", name, err)
	}
	return true, nil
}

// ApplyAddress replaces the interface's addresses with the given CIDR and
// makes sure the link is up. Addresses other than the desired one are
// removed, so address changes take effect without a link flap.
func ApplyAddress(name, cidr string) error {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return fmt.Errorf("address %q: %w", cidr, err)
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("lookup %s: %w", name, err)
	}
	desired := &netlink.Addr{IPNet: &net.IPNet{
		IP:   net.IP(p.Addr().AsSlice()),
		Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen()),
	}}
	if err := netlink.AddrReplace(link, desired); err != nil {
		return fmt.Errorf("assign %s to %s: %w", cidr, name, err)
	}
	family := unix.AF_INET6
	if p.Addr().Is4() {
		family = unix.AF_INET
	}
	addrs, err := netlink.AddrList(link, family)
	if err != nil {
		return fmt.Errorf("list %s addresses: %w", name, err)
	}
	for _, a := range addrs {
		if a.IPNet.String() != desired.IPNet.String() {
			if err := netlink.AddrDel(link, &a); err != nil {
				return fmt.Errorf("remove stale address %s from %s: %w", a.IPNet, name, err)
			}
		}
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bring %s up: %w", name, err)
	}
	return nil
}

// SyncRoutes reconciles the interface's routes with the peer AllowedIPs in
// cfg: every allowed prefix gets a route via the link (replace semantics),
// and stale static routes on the link that are no longer allowed are
// removed. Routes carry proto static so only routes installed here are ever
// deleted — the connected route from the interface address (proto kernel) is
// untouched. Rendered configs are split-tunnel (never a default route);
// a default AllowedIPs entry is skipped defensively since it would need
// policy routing this tool does not do.
func SyncRoutes(name string, cfg *wgtypes.Config) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("lookup %s: %w", name, err)
	}
	desired := map[string]*net.IPNet{}
	for i := range cfg.Peers {
		for j := range cfg.Peers[i].AllowedIPs {
			ipnet := &cfg.Peers[i].AllowedIPs[j]
			if ones, bits := ipnet.Mask.Size(); ones == 0 && bits != 0 {
				continue // default route: out of scope (split tunnel)
			}
			desired[ipnet.String()] = ipnet
		}
	}
	for _, ipnet := range desired {
		r := &netlink.Route{
			LinkIndex: link.Attrs().Index,
			Dst:       ipnet,
			Protocol:  unix.RTPROT_STATIC,
			Scope:     netlink.SCOPE_UNIVERSE,
		}
		if err := netlink.RouteReplace(r); err != nil {
			return fmt.Errorf("route %s via %s: %w", ipnet, name, err)
		}
	}
	have, err := netlink.RouteListFiltered(netlink.FAMILY_ALL, &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Protocol:  unix.RTPROT_STATIC,
	}, netlink.RT_FILTER_OIF|netlink.RT_FILTER_PROTOCOL)
	if err != nil {
		return fmt.Errorf("list %s routes: %w", name, err)
	}
	for _, r := range have {
		if r.Dst == nil || r.Gw != nil {
			continue
		}
		if _, ok := desired[r.Dst.String()]; !ok {
			if err := netlink.RouteDel(&r); err != nil {
				return fmt.Errorf("remove stale route %s from %s: %w", r.Dst, name, err)
			}
		}
	}
	return nil
}

// RunHooks executes a PostUp/PostDown-style command list from a rendered
// conf: commands separated by "; ", with %i replaced by the interface name,
// each run via sh -c. An empty list is a no-op.
func RunHooks(iface, commands string) error {
	if strings.TrimSpace(commands) == "" {
		return nil
	}
	for _, cmd := range strings.Split(commands, ";") {
		cmd = strings.ReplaceAll(strings.TrimSpace(cmd), "%i", iface)
		if cmd == "" {
			continue
		}
		if out, err := exec.Command("sh", "-c", cmd).CombinedOutput(); err != nil {
			return fmt.Errorf("hook %q failed: %v (%s)", cmd, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// ApplyDNS registers the DNS server for the interface via resolvconf, the
// same mechanism wg-quick uses. When resolvconf is not installed a note is
// returned for the caller to surface and no error occurs.
func ApplyDNS(iface, dns string) (string, error) {
	if dns == "" {
		return "", nil
	}
	bin, err := exec.LookPath("resolvconf")
	if err != nil {
		return fmt.Sprintf("resolvconf not installed — DNS = %s from the config was not applied", dns), nil
	}
	cmd := exec.Command(bin, "-a", iface, "-m", "0", "-x")
	cmd.Stdin = strings.NewReader("nameserver " + dns + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("resolvconf -a %s: %v (%s)", iface, err, strings.TrimSpace(string(out)))
	}
	return "", nil
}

// RemoveDNS unregisters the interface's DNS server via resolvconf. When
// resolvconf is not installed a note is returned for the caller to surface.
func RemoveDNS(iface, dns string) (string, error) {
	if dns == "" {
		return "", nil
	}
	bin, err := exec.LookPath("resolvconf")
	if err != nil {
		return "resolvconf not installed — could not remove the interface's previous DNS registration", nil
	}
	cmd := exec.Command(bin, "-d", iface)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("resolvconf -d %s: %v (%s)", iface, err, strings.TrimSpace(string(out)))
	}
	return "", nil
}

// Recreate replaces an existing interface with a clean one built from confPath.
// The previous configuration supplies PostDown and DNS cleanup. Cleanup happens
// before deletion so a cleanup failure leaves the existing interface intact.
func Recreate(iface, confPath string) error {
	fc, err := ParseFile(confPath)
	if err != nil {
		return err
	}
	if !InterfaceExists(iface) {
		return Up(iface, confPath, "")
	}
	if err := RunHooks(iface, fc.PostDown); err != nil {
		return err
	}
	if note, err := RemoveDNS(iface, fc.DNS); err != nil {
		return err
	} else if note != "" {
		fmt.Println("note:", note)
	}
	link, err := netlink.LinkByName(iface)
	if err != nil {
		return fmt.Errorf("lookup %s before recreate: %w", iface, err)
	}
	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("delete %s before recreate: %w", iface, err)
	}
	return Up(iface, confPath, "")
}

// Up brings the interface fully up from a rendered conf file: create the link
// if needed, sync peers (no link flap), assign the address, reconcile routes
// and DNS, then handle hooks. Hooks run only when they changed: PostUp on
// first creation, PostDown(old)+PostUp(new) when the hook text differs from
// the previously applied conf — re-running identical PostUp commands would
// duplicate iptables rules. oldConfPath may be "" or missing; in that case a
// changed PostUp on an already-live interface can double-add rules (both
// real call sites keep the previous conf on disk, so this stays theoretical).
func Up(iface, newConfPath, oldConfPath string) error {
	fc, err := ParseFile(newConfPath)
	if err != nil {
		return err
	}
	created, err := EnsureInterface(iface)
	if err != nil {
		return err
	}
	if err := SyncConf(iface, newConfPath); err != nil {
		return err
	}
	if fc.Address != "" {
		if err := ApplyAddress(iface, fc.Address); err != nil {
			return err
		}
	}
	if err := SyncRoutes(iface, fc.Wg); err != nil {
		return err
	}
	if note, err := ApplyDNS(iface, fc.DNS); err != nil {
		return err
	} else if note != "" {
		fmt.Println("note:", note)
	}

	var old FileConfig
	if oldConfPath != "" {
		if o, err := ParseFile(oldConfPath); err == nil {
			old = *o
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("parse previous config %s: %w", oldConfPath, err)
		}
	}
	switch {
	case created:
		return RunHooks(iface, fc.PostUp)
	case old.PostUp != fc.PostUp || old.PostDown != fc.PostDown:
		if err := RunHooks(iface, old.PostDown); err != nil {
			return err
		}
		return RunHooks(iface, fc.PostUp)
	}
	return nil
}
