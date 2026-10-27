package server

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"bntsovpn/internal/netconfig"
	"bntsovpn/internal/state"
)

const dnsTimeout = 5 * time.Second

type dnsZone struct {
	domain    string
	upstream  string
	adminAddr netip.Addr
	records   map[string]netip.Addr
}

type dnsService struct {
	packet   net.PacketConn
	listener net.Listener
	errors   chan error
	closing  chan struct{}
	close    sync.Once
}

func loadDNSZone(dir string) (*dnsZone, error) {
	cfg, err := netconfig.Load(filepath.Join(dir, "config.hcl"))
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	if cfg.DNSDomain == "" {
		return nil, nil
	}
	st, err := state.Load(filepath.Join(dir, "state.yaml"))
	if err != nil {
		return nil, err
	}
	adminState := st.Nodes[cfg.Admin]
	if adminState == nil {
		return nil, fmt.Errorf("admin node %q has no state — run apply first", cfg.Admin)
	}
	adminAddr, err := netip.ParseAddr(adminState.Address)
	if err != nil {
		return nil, fmt.Errorf("admin node %q has invalid state address: %w", cfg.Admin, err)
	}
	admin := cfg.NodeByName(cfg.Admin)
	upstreamAddr, err := netip.ParseAddr(admin.DNS)
	if err != nil {
		return nil, fmt.Errorf("admin node %q has invalid upstream dns address: %w", cfg.Admin, err)
	}
	if upstreamAddr == adminAddr {
		return nil, fmt.Errorf("admin node %q: upstream dns must differ from its VPN address", cfg.Admin)
	}
	z := &dnsZone{
		domain:    cfg.DNSDomain + ".",
		upstream:  net.JoinHostPort(admin.DNS, "53"),
		adminAddr: adminAddr,
		records:   map[string]netip.Addr{},
	}
	for _, node := range cfg.Nodes {
		ns := st.Nodes[node.Name]
		if ns == nil {
			return nil, fmt.Errorf("node %q has no state — run apply first", node.Name)
		}
		addr, err := netip.ParseAddr(ns.Address)
		if err != nil {
			return nil, fmt.Errorf("node %q has invalid state address: %w", node.Name, err)
		}
		z.records[strings.ToLower(node.Name)+"."+z.domain] = addr
		for _, alias := range node.Aliases {
			z.records[strings.ToLower(alias)+"."+z.domain] = addr
		}
	}
	return z, nil
}

func startDNSService(dir string) (*dnsService, error) {
	z, err := loadDNSZone(dir)
	if err != nil || z == nil {
		return nil, err
	}
	addr := net.JoinHostPort(z.adminAddr.String(), "53")
	udpNetwork, tcpNetwork := "udp6", "tcp6"
	if z.adminAddr.Is4() {
		udpNetwork, tcpNetwork = "udp4", "tcp4"
	}
	lc := net.ListenConfig{Control: freebind}
	packet, err := lc.ListenPacket(context.Background(), udpNetwork, addr)
	if err != nil {
		return nil, fmt.Errorf("DNS listen %s %s: %w", udpNetwork, addr, err)
	}
	listener, err := lc.Listen(context.Background(), tcpNetwork, addr)
	if err != nil {
		packet.Close()
		return nil, fmt.Errorf("DNS listen %s %s: %w", tcpNetwork, addr, err)
	}
	s := &dnsService{
		packet:   packet,
		listener: listener,
		errors:   make(chan error, 2),
		closing:  make(chan struct{}),
	}
	go s.serveUDP(dir)
	go s.serveTCP(dir)
	return s, nil
}

func (s *dnsService) Close() {
	s.close.Do(func() {
		close(s.closing)
		s.packet.Close()
		s.listener.Close()
	})
}

func (s *dnsService) report(err error) {
	select {
	case <-s.closing:
		return
	default:
	}
	select {
	case s.errors <- err:
	default:
	}
}

func (s *dnsService) serveUDP(dir string) {
	buf := make([]byte, 65535)
	for {
		n, addr, err := s.packet.ReadFrom(buf)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				s.report(fmt.Errorf("DNS UDP listener: %w", err))
			}
			return
		}
		query := append([]byte(nil), buf[:n]...)
		go func() {
			response, err := answerDNS(dir, "udp", query)
			if err == nil && response != nil {
				_, _ = s.packet.WriteTo(response, addr)
			}
		}()
	}
}

func (s *dnsService) serveTCP(dir string) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				s.report(fmt.Errorf("DNS TCP listener: %w", err))
			}
			return
		}
		go serveDNSConn(conn, dir)
	}
}

func serveDNSConn(conn net.Conn, dir string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	for {
		var size uint16
		if err := binary.Read(conn, binary.BigEndian, &size); err != nil {
			return
		}
		query := make([]byte, int(size))
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}
		response, err := answerDNS(dir, "tcp", query)
		if err != nil || response == nil {
			return
		}
		if err := binary.Write(conn, binary.BigEndian, uint16(len(response))); err != nil {
			return
		}
		if _, err := conn.Write(response); err != nil {
			return
		}
	}
}

func answerDNS(dir, network string, query []byte) ([]byte, error) {
	z, err := loadDNSZone(dir)
	if err != nil || z == nil {
		return dnsError(query, dnsmessage.RCodeServerFailure), nil
	}
	return answerDNSZone(z, network, query)
}

func answerDNSZone(z *dnsZone, network string, query []byte) ([]byte, error) {
	var msg dnsmessage.Message
	if err := msg.Unpack(query); err != nil {
		return nil, err
	}
	if msg.Response || msg.OpCode != 0 || len(msg.Questions) != 1 {
		return dnsError(query, dnsmessage.RCodeFormatError), nil
	}
	question := msg.Questions[0]
	name := strings.ToLower(question.Name.String())
	if name != z.domain && !strings.HasSuffix(name, "."+z.domain) {
		ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
		defer cancel()
		response, err := forwardDNS(ctx, network, z.upstream, query)
		if err != nil {
			return dnsError(query, dnsmessage.RCodeServerFailure), nil
		}
		return response, nil
	}

	response := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:                 msg.Header.ID,
			Response:           true,
			OpCode:             msg.Header.OpCode,
			Authoritative:      true,
			RecursionDesired:   msg.Header.RecursionDesired,
			RecursionAvailable: true,
		},
		Questions: msg.Questions,
	}
	addr, exists := z.records[name]
	if !exists {
		response.Header.RCode = dnsmessage.RCodeNameError
		return response.Pack()
	}
	if question.Class != dnsmessage.ClassINET {
		response.Header.RCode = dnsmessage.RCodeNotImplemented
		return response.Pack()
	}
	resource := dnsmessage.ResourceHeader{
		Name:  question.Name,
		Type:  question.Type,
		Class: dnsmessage.ClassINET,
		TTL:   30,
	}
	switch {
	case question.Type == dnsmessage.TypeA && addr.Is4():
		response.Answers = []dnsmessage.Resource{{
			Header: resource,
			Body:   &dnsmessage.AResource{A: addr.As4()},
		}}
	case question.Type == dnsmessage.TypeAAAA && addr.Is6():
		response.Answers = []dnsmessage.Resource{{
			Header: resource,
			Body:   &dnsmessage.AAAAResource{AAAA: addr.As16()},
		}}
	}
	return response.Pack()
}

func dnsError(query []byte, code dnsmessage.RCode) []byte {
	var request dnsmessage.Message
	if err := request.Unpack(query); err != nil {
		return nil
	}
	response := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:                 request.Header.ID,
			Response:           true,
			OpCode:             request.Header.OpCode,
			RecursionDesired:   request.Header.RecursionDesired,
			RecursionAvailable: true,
			RCode:              code,
		},
		Questions: request.Questions,
	}
	packed, _ := response.Pack()
	return packed
}

func forwardDNS(ctx context.Context, network, upstream string, query []byte) ([]byte, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, upstream)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(dnsTimeout)
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if network == "tcp" {
		if err := binary.Write(conn, binary.BigEndian, uint16(len(query))); err != nil {
			return nil, err
		}
	}
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	if network == "tcp" {
		var size uint16
		if err := binary.Read(conn, binary.BigEndian, &size); err != nil {
			return nil, err
		}
		response := make([]byte, int(size))
		_, err := io.ReadFull(conn, response)
		return response, err
	}
	response := make([]byte, 65535)
	n, err := conn.Read(response)
	return response[:n], err
}
