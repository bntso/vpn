package server

import (
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"bntsovpn/internal/state"
)

const testDNSConfig = `name = "bntso"
subnet = "10.13.13.0/24"
admin = "home"
dns_domain = "bntso.internal"

node "home" {
  role = "hub"
  endpoint = "home.example.com:51820"
  dns = "192.0.2.1"
  aliases = ["gateway", "files"]
}

node "phone" {
  role = "spoke"
  via = "home"
}
`

func writeDNSFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.hcl"), []byte(testDNSConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &state.State{Nodes: map[string]*state.NodeState{
		"home":  {Address: "10.13.13.1"},
		"phone": {Address: "10.13.13.2"},
	}}
	if err := st.Save(filepath.Join(dir, "state.yaml")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func startTestDNSService(t *testing.T, dir string) string {
	t.Helper()
	packet, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", packet.LocalAddr().String())
	if err != nil {
		packet.Close()
		t.Fatal(err)
	}
	s := &dnsService{
		packet:   packet,
		listener: listener,
		errors:   make(chan error, 2),
		closing:  make(chan struct{}),
	}
	go s.serveUDP(dir)
	go s.serveTCP(dir)
	t.Cleanup(s.Close)
	return packet.LocalAddr().String()
}

func dnsQuery(t *testing.T, id uint16, name string, queryType dnsmessage.Type) []byte {
	t.Helper()
	qname, err := dnsmessage.NewName(name)
	if err != nil {
		t.Fatal(err)
	}
	message := dnsmessage.Message{
		Header: dnsmessage.Header{ID: id, RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name: qname, Type: queryType, Class: dnsmessage.ClassINET,
		}},
	}
	query, err := message.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return query
}

func exchangeTestDNS(t *testing.T, network, addr string, query []byte) dnsmessage.Message {
	t.Helper()
	conn, err := net.DialTimeout(network, addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if network == "tcp" {
		if err := binary.Write(conn, binary.BigEndian, uint16(len(query))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Write(query); err != nil {
		t.Fatal(err)
	}
	var response []byte
	if network == "tcp" {
		var size uint16
		if err := binary.Read(conn, binary.BigEndian, &size); err != nil {
			t.Fatal(err)
		}
		response = make([]byte, int(size))
		if _, err := io.ReadFull(conn, response); err != nil {
			t.Fatal(err)
		}
	} else {
		response = make([]byte, 65535)
		n, err := conn.Read(response)
		if err != nil {
			t.Fatal(err)
		}
		response = response[:n]
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(response); err != nil {
		t.Fatal(err)
	}
	return msg
}

func requireARecord(t *testing.T, msg dnsmessage.Message, want [4]byte) {
	t.Helper()
	if msg.Header.RCode != dnsmessage.RCodeSuccess || len(msg.Answers) != 1 {
		t.Fatalf("DNS response code = %s, answers = %d", msg.Header.RCode, len(msg.Answers))
	}
	body, ok := msg.Answers[0].Body.(*dnsmessage.AResource)
	if !ok || body.A != want {
		t.Fatalf("DNS answer = %#v, want A %v", msg.Answers[0].Body, want)
	}
}

func TestDNSServiceResolvesNamesAliasesAndReloads(t *testing.T) {
	dir := writeDNSFixture(t)
	addr := startTestDNSService(t, dir)
	for _, network := range []string{"udp", "tcp"} {
		t.Run(network, func(t *testing.T) {
			for _, name := range []string{"home.bntso.internal.", "GATEWAY.bntso.internal.", "files.bntso.internal."} {
				msg := exchangeTestDNS(t, network, addr, dnsQuery(t, 10, name, dnsmessage.TypeA))
				requireARecord(t, msg, [4]byte{10, 13, 13, 1})
			}
		})
	}

	missing := exchangeTestDNS(t, "udp", addr, dnsQuery(t, 11, "missing.bntso.internal.", dnsmessage.TypeA))
	if missing.Header.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("unknown local name code = %s", missing.Header.RCode)
	}
	aaaa := exchangeTestDNS(t, "udp", addr, dnsQuery(t, 12, "home.bntso.internal.", dnsmessage.TypeAAAA))
	if aaaa.Header.RCode != dnsmessage.RCodeSuccess || len(aaaa.Answers) != 0 {
		t.Fatalf("IPv4 node AAAA response = %#v", aaaa)
	}

	st, err := state.Load(filepath.Join(dir, "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	st.Nodes["home"].Address = "10.13.13.9"
	if err := st.Save(filepath.Join(dir, "state.yaml")); err != nil {
		t.Fatal(err)
	}
	reloaded := exchangeTestDNS(t, "udp", addr, dnsQuery(t, 13, "home.bntso.internal.", dnsmessage.TypeA))
	requireARecord(t, reloaded, [4]byte{10, 13, 13, 9})
}

func TestDNSAnswersAAAA(t *testing.T) {
	addr := netip.MustParseAddr("fd7a:3b9c:1a2b::1")
	z := &dnsZone{
		domain:  "bntso.internal.",
		records: map[string]netip.Addr{"home.bntso.internal.": addr},
	}
	packed, err := answerDNSZone(z, "udp", dnsQuery(t, 20, "home.bntso.internal.", dnsmessage.TypeAAAA))
	if err != nil {
		t.Fatal(err)
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(packed); err != nil {
		t.Fatal(err)
	}
	if len(msg.Answers) != 1 {
		t.Fatalf("AAAA answers = %d", len(msg.Answers))
	}
	body, ok := msg.Answers[0].Body.(*dnsmessage.AAAAResource)
	if !ok || body.AAAA != addr.As16() {
		t.Fatalf("AAAA answer = %#v", msg.Answers[0].Body)
	}
}

func TestDNSForwardsOutsideZone(t *testing.T) {
	for _, network := range []string{"udp", "tcp"} {
		t.Run(network, func(t *testing.T) {
			var addr string
			if network == "udp" {
				conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				addr = conn.LocalAddr().String()
				go func() {
					query := make([]byte, 65535)
					n, client, err := conn.ReadFrom(query)
					if err == nil {
						_, _ = conn.WriteTo(dnsError(query[:n], dnsmessage.RCodeSuccess), client)
					}
				}()
			} else {
				listener, err := net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				addr = listener.Addr().String()
				go func() {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					var size uint16
					if binary.Read(conn, binary.BigEndian, &size) != nil {
						return
					}
					query := make([]byte, int(size))
					if _, err := io.ReadFull(conn, query); err != nil {
						return
					}
					response := dnsError(query, dnsmessage.RCodeSuccess)
					if binary.Write(conn, binary.BigEndian, uint16(len(response))) == nil {
						_, _ = conn.Write(response)
					}
				}()
			}
			z := &dnsZone{domain: "bntso.internal.", upstream: addr}
			response, err := answerDNSZone(z, network, dnsQuery(t, 42, "example.com.", dnsmessage.TypeA))
			if err != nil {
				t.Fatal(err)
			}
			var msg dnsmessage.Message
			if err := msg.Unpack(response); err != nil {
				t.Fatal(err)
			}
			if !msg.Response || msg.ID != 42 || msg.RCode != dnsmessage.RCodeSuccess {
				t.Fatalf("forwarded response = %#v", msg.Header)
			}
		})
	}
}

func TestLoadDNSZoneRejectsUpstreamLoop(t *testing.T) {
	dir := writeDNSFixture(t)
	path := filepath.Join(dir, "config.hcl")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contents = []byte(strings.Replace(string(contents), `dns = "192.0.2.1"`, `dns = "10.13.13.1"`, 1))
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDNSZone(dir); err == nil {
		t.Fatal("expected upstream loop to be rejected")
	}
}
