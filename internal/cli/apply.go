package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	wgrender "bntsovpn/internal/config"
	"bntsovpn/internal/device"
	"bntsovpn/internal/netconfig"
	"bntsovpn/internal/state"
	"bntsovpn/systemd"
)

func applyCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Reconcile state with config.hcl, render all node configs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApply(dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "render to stdout; touch nothing on the system (no state, no rendered files, no live interface/sysctl/systemd changes)")
	return cmd
}

func runApply(dryRun bool) error {
	cfg, err := netconfig.Load(configPath())
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	st, err := state.Load(statePath())
	if err != nil {
		return err
	}

	added, removed, err := state.Reconcile(cfg, st)
	if err != nil {
		return err
	}
	sort.Strings(added)
	sort.Strings(removed)

	rendered, err := wgrender.RenderAll(cfg, st)
	if err != nil {
		return err
	}

	if dryRun {
		for _, n := range cfg.Nodes {
			fmt.Printf("# ===== rendered/%s.conf =====\n%s\n", n.Name, rendered[n.Name])
		}
		printSummary(added, removed, changedSet(cfg, rendered), true)
		return nil
	}

	// Persist state and rendered configs. The admin node's previous rendered
	// conf is kept for hook diffing (PostDown of the old NAT rules).
	oldAdminConf, _ := os.ReadFile(filepath.Join(renderedDir(), cfg.Admin+".conf"))
	if err := st.Save(statePath()); err != nil {
		return err
	}
	changed := changedSet(cfg, rendered)
	if err := os.MkdirAll(renderedDir(), 0o700); err != nil {
		return err
	}
	for _, n := range cfg.Nodes {
		path := filepath.Join(renderedDir(), n.Name+".conf")
		if err := os.WriteFile(path, []byte(rendered[n.Name]), 0o600); err != nil {
			return err
		}
	}
	// Drop renders of removed nodes.
	entries, err := os.ReadDir(renderedDir())
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if filepath.Ext(name) != ".conf" {
				continue
			}
			node := name[:len(name)-len(".conf")]
			if cfg.NodeByName(node) == nil {
				os.Remove(filepath.Join(renderedDir(), name))
			}
		}
	}

	printSummary(added, removed, changed, false)

	// Local live reconcile: only meaningful on the admin machine, and only
	// attempted as root with the interface present — otherwise cleanly skipped.
	if err := localReconcile(cfg, oldAdminConf); err != nil {
		return err
	}
	return nil
}

// changedSet compares freshly rendered configs against the files on disk.
func changedSet(cfg *netconfig.Config, rendered map[string]string) map[string]bool {
	changed := map[string]bool{}
	for _, n := range cfg.Nodes {
		old, err := os.ReadFile(filepath.Join(renderedDir(), n.Name+".conf"))
		if err != nil || string(old) != rendered[n.Name] {
			changed[n.Name] = true
		}
	}
	return changed
}

func printSummary(added, removed []string, changed map[string]bool, dryRun bool) {
	label := "apply"
	if dryRun {
		label = "apply (dry-run)"
	}
	if len(added) == 0 && len(removed) == 0 && len(changed) == 0 {
		fmt.Printf("%s: no changes\n", label)
		return
	}
	if len(added) > 0 {
		fmt.Printf("%s: nodes added: %v\n", label, added)
	}
	if len(removed) > 0 {
		fmt.Printf("%s: nodes removed: %v (tokens revoked)\n", label, removed)
	}
	var names []string
	for n := range changed {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > 0 {
		fmt.Printf("%s: configs changed: %v\n", label, names)
	}
}

// localReconcile brings up and configures the live wg0 interface directly
// (netlink + wgctrl, no wg-quick), plus forwarding sysctls and the systemd
// serve unit. Everything is skipped with a note when unprivileged, so smoke
// tests in a temp dir never touch the system. oldAdminConf is the admin
// node's previously rendered conf (nil on first apply), used to run PostDown
// hooks when the NAT rules changed.
func localReconcile(cfg *netconfig.Config, oldAdminConf []byte) error {
	if !isRoot() {
		fmt.Println("note: not root — skipping live interface, sysctl and systemd steps")
		return nil
	}
	subnet, err := cfg.SubnetPrefix()
	if err != nil {
		return err
	}

	conf := filepath.Join(renderedDir(), cfg.Admin+".conf")
	oldPath := ""
	if oldAdminConf != nil {
		oldPath = filepath.Join(renderedDir(), "."+cfg.Admin+".conf.prev")
		if err := os.WriteFile(oldPath, oldAdminConf, 0o600); err != nil {
			return fmt.Errorf("stage previous admin config: %w", err)
		}
	}
	if err := device.Up("wg0", conf, oldPath); err != nil {
		return fmt.Errorf("configure live wg0: %w", err)
	}
	fmt.Println("applied admin config to live wg0 (interface managed directly, no wg-quick)")

	// Forwarding sysctls, per family.
	var lines []string
	if subnet.Addr().Is4() {
		lines = append(lines, "net.ipv4.ip_forward=1")
	} else {
		lines = append(lines, "net.ipv6.conf.all.forwarding=1")
	}
	dropin := ""
	for _, l := range lines {
		dropin += l + "\n"
	}
	if err := os.WriteFile("/etc/sysctl.d/90-bntsovpn.conf", []byte(dropin), 0o644); err == nil {
		if out, err := exec.Command("sysctl", "-p", "/etc/sysctl.d/90-bntsovpn.conf").CombinedOutput(); err != nil {
			fmt.Printf("warning: sysctl -p failed: %v (%s)\n", err, out)
		}
	}

	// systemd: serve unit on the admin machine.
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		if err := os.WriteFile("/etc/systemd/system/bntsovpn-serve.service", []byte(systemd.ServeUnit), 0o644); err != nil {
			fmt.Printf("warning: write serve unit: %v\n", err)
		}
		for _, args := range [][]string{
			{"daemon-reload"},
			{"enable", "bntsovpn-serve"},
		} {
			if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
				fmt.Printf("warning: systemctl %v failed: %v (%s)\n", args, err, out)
			}
		}
		fmt.Println("installed bntsovpn-serve.service")
	}
	return nil
}
