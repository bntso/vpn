package netconfig

import (
	"fmt"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	"gopkg.in/yaml.v3"
)

// ConvertYAML converts a legacy YAML network configuration to validated HCL.
func ConvertYAML(data []byte) ([]byte, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse legacy YAML: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid legacy config: %w", err)
	}
	return renderHCL(&cfg), nil
}

func renderHCL(cfg *Config) []byte {
	file := hclwrite.NewEmptyFile()
	body := file.Body()
	body.SetAttributeValue("name", cty.StringVal(cfg.Name))
	body.SetAttributeValue("subnet", cty.StringVal(cfg.Subnet))
	body.SetAttributeValue("admin", cty.StringVal(cfg.Admin))
	body.SetAttributeValue("api_port", cty.NumberIntVal(int64(cfg.APIPort)))
	setOptionalString(body, "api_bind", cfg.APIBind)
	if cfg.APIPublic {
		body.SetAttributeValue("api_public", cty.BoolVal(true))
	}
	setOptionalString(body, "api_url", cfg.APIURL)

	for _, node := range cfg.Nodes {
		body.AppendNewline()
		block := body.AppendNewBlock("node", []string{node.Name})
		nodeBody := block.Body()
		nodeBody.SetAttributeValue("role", cty.StringVal(node.Role))
		setOptionalString(nodeBody, "address", node.Address)
		setOptionalString(nodeBody, "endpoint", node.Endpoint)
		if node.ListenPort != 0 {
			nodeBody.SetAttributeValue("listen_port", cty.NumberIntVal(int64(node.ListenPort)))
		}
		setOptionalStrings(nodeBody, "expose", node.Expose)
		setOptionalString(nodeBody, "nat_iface", node.NATIface)
		setOptionalString(nodeBody, "dns", node.DNS)
		setOptionalString(nodeBody, "via", node.Via)
		setOptionalStrings(nodeBody, "backup", node.Backup)
	}
	return hclwrite.Format(file.Bytes())
}

func setOptionalString(body *hclwrite.Body, name, value string) {
	if value != "" {
		body.SetAttributeValue(name, cty.StringVal(value))
	}
}

func setOptionalStrings(body *hclwrite.Body, name string, values []string) {
	if len(values) == 0 {
		return
	}
	items := make([]cty.Value, len(values))
	for i, value := range values {
		items[i] = cty.StringVal(value)
	}
	body.SetAttributeValue(name, cty.ListVal(items))
}
