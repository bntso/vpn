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
	mu       sync.Mutex
	packet   net.PacketConn
	listener net.Listener
	bound    string // admin VPN address the listeners are bound to
	dir      string // config/state directory, for zone reloads and rebinds
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
	s := &dnsService{
		dir:     dir,
		errors:  make(chan error, 2),
		closing: make(chan struct{}),
	}
	if err := s.bind(z.adminAddr); err != nil {
		return nil, err
	}
	return s, nil
}

// bind installs listeners on the admin VPN address and starts serving them.
// A previous generation of listeners, if any, is closed and its serve loops
// pick up the new sockets. A failed bind leaves the previous generation
// intact so a transient rebind failure never takes DNS down.
func (s *dnsService) bind(addr netip.Addr) error {
	hostPort := net.JoinHostPort(addr.String(), "53")
	udpNetwork, tcpNetwork := "udp6", "tcp6"
	if addr.Is4() {
		udpNetwork, tcpNetwork = "udp4", "tcp4"
	}
	lc := net.ListenConfig{Control: freebind}
	packet, err := lc.ListenPacket(context.Background(), udpNetwork, hostPort)
	if err != nil {
		return fmt.Errorf("DNS listen %s %s: %w", udpNetwork, hostPort, err)
	}
	listener, err := lc.Listen(context.Background(), tcpNetwork, hostPort)
	if err != nil {
		packet.Close()
		return fmt.Errorf("DNS listen %s %s: %w", tcpNetwork, hostPort, err)
	}
	s.mu.Lock()
	oldPacket, oldListener := s.packet, s.listener
	s.packet, s.listener, s.bound = packet, listener, addr.String()
	s.mu.Unlock()
	if oldPacket != nil {
		oldPacket.Close()
	}
	if oldListener != nil {
		oldListener.Close()
	}
	go s.serveUDP(s.dir)
	go s.serveTCP(s.dir)
	return nil
}

// watchRebind reloads the zone periodically and rebinds the listeners when
// the admin node's VPN address changed (e.g. apply re-allocated it without a
// serve restart). Record data needs no rebind: every query reloads the zone.
func (s *dnsService) watchRebind() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.closing:
			return
		case <-ticker.C:
		}
		z, err := loadDNSZone(s.dir)
		if err != nil {
			fmt.Printf("warning: could not reload private DNS zone: %v (keeping current listeners)\n", err)
			continue
		}
		if z == nil {
			fmt.Println("note: dns_domain removed from config — private DNS stays up until bntsovpn is restarted; restart it to disable DNS")
			continue
		}
		s.mu.Lock()
		bound := s.bound
		s.mu.Unlock()
		if z.adminAddr.String() == bound {
			continue
		}
		fmt.Printf("note: admin VPN address changed from %s to %s — rebinding private DNS\n", bound, z.adminAddr)
		if err := s.bind(z.adminAddr); err != nil {
			fmt.Printf("warning: could not rebind private DNS to %s: %v (retrying on next check)\n", z.adminAddr, err)
		}
	}
}

func (s *dnsService) Close() {
	s.close.Do(func() {
		close(s.closing)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.packet != nil {
			s.packet.Close()
		}
		if s.listener != nil {
			s.listener.Close()
		}
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

func (s *dnsService) currentPacket() net.PacketConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.packet
}

func (s *dnsService) currentListener() net.Listener {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listener
}

func (s *dnsService) serveUDP(dir string) {
	buf := make([]byte, 65535)
	for {
		packet := s.currentPacket()
		if packet == nil {
			return
		}
		n, addr, err := packet.ReadFrom(buf)
		if err != nil {
			select {
			case <-s.closing:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				continue // rebind: pick up the new socket
			}
			s.report(fmt.Errorf("DNS UDP listener: %w", err))
			return
		}
		query := append([]byte(nil), buf[:n]...)
		go func() {
			response, err := answerDNS(dir, "udp", query)
			if err == nil && response != nil {
				_, _ = packet.WriteTo(response, addr)
			}
		}()
	}
}

func (s *dnsService) serveTCP(dir string) {
	for {
		listener := s.currentListener()
		if listener == nil {
			return
		}
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-s.closing:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				continue // rebind: pick up the new socket
			}
			s.report(fmt.Errorf("DNS TCP listener: %w", err))
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
