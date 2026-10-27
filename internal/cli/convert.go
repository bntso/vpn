package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"bntsovpn/internal/netconfig"
)

func convertConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "convert-config",
		Short: "Convert legacy config.yaml to config.hcl",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConvertConfig()
		},
	}
}

func runConvertConfig() error {
	input := legacyConfigPath()
	output := configPath()
	data, err := os.ReadFile(input)
	if err != nil {
		return fmt.Errorf("read %s: %w", input, err)
	}
	converted, err := netconfig.ConvertYAML(data)
	if err != nil {
		return fmt.Errorf("convert %s: %w", input, err)
	}
	if err := writeNewConfig(output, converted); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists: refusing to overwrite", output)
		}
		return fmt.Errorf("write %s: %w", output, err)
	}
	fmt.Printf("converted %s to %s\n", input, output)
	return nil
}

func writeNewConfig(path string, data []byte) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	_, err = file.Write(data)
	return err
}
