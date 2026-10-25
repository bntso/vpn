package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"bntsovpn/internal/agent"
)

func agentCmd() *cobra.Command {
	var once bool
	var node, token, api, confPath, iface, agentConf, privKey string
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "agent [--once]",
		Short: "Poll the control plane and apply own config changes via wg syncconf",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := &agent.Config{}
			confFile := agentConf
			if confFile == "" {
				confFile = agentConfPath()
			}
			if _, err := os.Stat(confFile); err == nil {
				loaded, err := agent.LoadConfig(confFile)
				if err != nil {
					return err
				}
				cfg = loaded
			} else if os.IsPermission(err) {
				return fmt.Errorf("cannot read %s: permission denied — re-run with sudo (join writes it as root, mode 0600)", confFile)
			} else if !os.IsNotExist(err) {
				return err
			}
			if node != "" {
				cfg.Node = node
			}
			if token != "" {
				cfg.Token = token
			}
			if api != "" {
				cfg.API = api
			}
			if confPath != "" {
				cfg.ConfigPath = confPath
			}
			if iface != "" {
				cfg.Interface = iface
			}
			if privKey != "" {
				cfg.PrivateKey = privKey
			}
			if cfg.Token == "" {
				return fmt.Errorf("no credentials: %s not found or empty — run `bntsovpn join <blob>` first (it stores the token in agent.yaml), or pass --node/--token/--api/--private-key explicitly", confFile)
			}
			if once {
				return cfg.Once()
			}
			return cfg.Run(interval)
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "single fetch/apply cycle (for testing)")
	cmd.Flags().StringVar(&agentConf, "agent-config", "", "agent.yaml path (default <dir>/agent.yaml)")
	cmd.Flags().StringVar(&node, "node", "", "node name (overrides agent.yaml)")
	cmd.Flags().StringVar(&token, "token", "", "bearer token (overrides agent.yaml)")
	cmd.Flags().StringVar(&api, "api", "", "control plane address, e.g. http://10.13.13.1:51821 (overrides agent.yaml)")
	cmd.Flags().StringVar(&confPath, "config", "", "managed wg0.conf path (overrides agent.yaml)")
	cmd.Flags().StringVar(&iface, "interface", "", "WireGuard interface (default wg0)")
	cmd.Flags().StringVar(&privKey, "private-key", "", "node WireGuard private key for unsealing (overrides agent.yaml)")
	cmd.Flags().DurationVar(&interval, "interval", 60*time.Second, "poll interval (jitter added)")
	return cmd
}
