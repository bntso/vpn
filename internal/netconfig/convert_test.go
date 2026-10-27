package netconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestConvertYAML(t *testing.T) {
	source := []byte(`
name: bntso
subnet: 10.13.13.0/24
admin: home
api_port: 51822
api_bind: 127.0.0.1:51822
api_public: true
api_url: http://home.example.com:51822
nodes:
  - name: home
    role: hub
    address: 10.13.13.1
    endpoint: home.example.com:51820
    listen_port: 51820
    expose: [192.168.1.0/24, 192.168.2.0/24]
    nat_iface: eth1
    dns: 192.168.1.1
  - name: dr
    role: hub
    address: 10.13.13.2
    endpoint: dr.example.com:51821
    listen_port: 51821
  - name: phone
    role: spoke
    address: 10.13.13.3
    via: home
    backup: [dr]
`)
	converted, err := ConvertYAML(source)
	if err != nil {
		t.Fatalf("ConvertYAML: %v", err)
	}
	again, err := ConvertYAML(source)
	if err != nil {
		t.Fatalf("second ConvertYAML: %v", err)
	}
	if string(converted) != string(again) {
		t.Fatal("conversion output is not deterministic")
	}

	path := filepath.Join(t.TempDir(), "config.hcl")
	if err := os.WriteFile(path, converted, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("converted HCL does not load: %v\n%s", err, converted)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("converted HCL is invalid: %v", err)
	}
	want := &Config{
		Name:      "bntso",
		Subnet:    "10.13.13.0/24",
		Admin:     "home",
		APIPort:   51822,
		APIBind:   "127.0.0.1:51822",
		APIPublic: true,
		APIURL:    "http://home.example.com:51822",
		Nodes: []Node{
			{
				Name: "home", Role: RoleHub, Address: "10.13.13.1",
				Endpoint: "home.example.com:51820", ListenPort: 51820,
				Expose:   []string{"192.168.1.0/24", "192.168.2.0/24"},
				NATIface: "eth1", DNS: "192.168.1.1",
			},
			{
				Name: "dr", Role: RoleHub, Address: "10.13.13.2",
				Endpoint: "dr.example.com:51821", ListenPort: 51821,
			},
			{
				Name: "phone", Role: RoleSpoke, Address: "10.13.13.3",
				Via: "home", Backup: []string{"dr"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("converted config mismatch\ngot:  %+v\nwant: %+v", got, want)
	}
}

func TestConvertYAMLPreservesDefaults(t *testing.T) {
	converted, err := ConvertYAML([]byte(`
name: bntso
subnet: 10.13.13.0/24
admin: home
nodes:
  - name: home
    role: hub
    endpoint: home.example.com:51820
`))
	if err != nil {
		t.Fatalf("ConvertYAML: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.hcl")
	if err := os.WriteFile(path, converted, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIPort != DefaultAPIPort {
		t.Errorf("api_port = %d, want default %d", cfg.APIPort, DefaultAPIPort)
	}
	home := cfg.NodeByName("home")
	if home.Address != "" || home.ListenPort != 0 || len(home.Expose) != 0 || len(home.Backup) != 0 {
		t.Errorf("unset optional fields changed during conversion: %+v", home)
	}
}

func TestConvertYAMLRejectsInvalidInput(t *testing.T) {
	for name, source := range map[string]string{
		"malformed YAML": "nodes: [",
		"invalid config": `
name: bntso
subnet: 10.13.13.0/24
admin: home
nodes:
  - name: home
    role: hub
`,
	} {
		t.Run(name, func(t *testing.T) {
			if converted, err := ConvertYAML([]byte(source)); err == nil {
				t.Fatalf("ConvertYAML succeeded with invalid input: %s", converted)
			}
		})
	}
}
