package fakedns

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

// TXTFunc answers TXT queries the zone does not hold (the lab wires fakeasn.TXT here); nil → NXDOMAIN.
type TXTFunc func(name string) []string

// Config describes what a server answers.
type Config struct {
	Zone     map[string][]netip.Addr // name → A/AAAA answers (exact names, lowercase, no trailing dot)
	Poisoned map[string]netip.Addr   // plain mode only: answer this address instead (suffix match)
	TXT      TXTFunc
	TTL      uint32 // default 60
}

// Query is one logged DNS question.
type Query struct {
	Name string // lowercase, no trailing dot
	Type string // "A", "AAAA", "TXT", "HTTPS", ...
	From netip.AddrPort
	Via  string // "doh" | "udp" | "tcp"
	At   time.Time
}

// Server is a running DoH or plain DNS server.
type Server struct {
	Addr netip.AddrPort

	zone     atomic.Pointer[map[string][]netip.Addr]
	poisoned map[string]netip.Addr // nil in DoH mode
	txt      TXTFunc
	ttl      uint32

	mu      sync.Mutex
	queries []Query
	conns   map[net.Conn]struct{}
	closing bool

	closers   []func()
	wg        sync.WaitGroup
	done      chan struct{}
	closeOnce sync.Once
}

const (
	defaultTTL  = 60
	maxMsg      = 64 << 10
	tcpIdle     = 10 * time.Second
	portRetries = 8
	dohPath     = "/dns-query"
	dohType     = "application/dns-message"
)

// RunDoH serves RFC 8484 (GET and POST on /dns-query) over HTTPS with cert. It stops when ctx is done.
// The listener exists when RunDoH returns. Poisoning is never applied in this mode.
func RunDoH(ctx context.Context, listen string, cert tls.Certificate, cfg Config) (*Server, error) {
	s, err := newServer(cfg, false)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("fakedns: listen: %w", err)
	}
	if s.Addr, err = addrPort(ln.Addr()); err != nil {
		ln.Close()
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc(dohPath, s.serveDoH)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){}, // HTTP/1.1 only
	}
	tln := tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
	s.closers = append(s.closers, func() { srv.Close(); ln.Close() })
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		srv.Serve(tln)
	}()
	s.watch(ctx)
	return s, nil
}

// RunPlain serves UDP and TCP DNS on the same address. It stops when ctx is done.
// With port 0 the UDP port is picked first and TCP binds the same one (retried a few times on a
// conflict). Both listeners exist when RunPlain returns.
func RunPlain(ctx context.Context, listen string, cfg Config) (*Server, error) {
	s, err := newServer(cfg, true)
	if err != nil {
		return nil, err
	}
	pc, ln, err := listenPair(listen)
	if err != nil {
		return nil, err
	}
	if s.Addr, err = addrPort(pc.LocalAddr()); err != nil {
		pc.Close()
		ln.Close()
		return nil, err
	}
	s.closers = append(s.closers, func() { pc.Close() }, func() { ln.Close() })
	s.wg.Add(2)
	go s.serveUDP(pc)
	go s.serveTCP(ln)
	s.watch(ctx)
	return s, nil
}

// listenPair binds UDP and TCP on the same address and port.
func listenPair(listen string) (net.PacketConn, net.Listener, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return nil, nil, fmt.Errorf("fakedns: listen address: %w", err)
	}
	tries := 1
	if port == "0" || port == "" {
		tries = portRetries
	}
	var lastErr error
	for range tries {
		pc, err := net.ListenPacket("udp", listen)
		if err != nil {
			return nil, nil, fmt.Errorf("fakedns: listen udp: %w", err)
		}
		ua, ok := pc.LocalAddr().(*net.UDPAddr)
		if !ok {
			pc.Close()
			return nil, nil, errors.New("fakedns: unexpected UDP address type")
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(ua.Port)))
		if err == nil {
			return pc, ln, nil
		}
		pc.Close()
		lastErr = err
	}
	return nil, nil, fmt.Errorf("fakedns: listen tcp: %w", lastErr)
}

func newServer(cfg Config, plain bool) (*Server, error) {
	zone, err := normalizeZone(cfg.Zone, true)
	if err != nil {
		return nil, err
	}
	s := &Server{txt: cfg.TXT, ttl: cfg.TTL, conns: map[net.Conn]struct{}{}, done: make(chan struct{})}
	if s.ttl == 0 {
		s.ttl = defaultTTL
	}
	s.zone.Store(&zone)
	if plain {
		s.poisoned = map[string]netip.Addr{}
		for name, a := range cfg.Poisoned {
			n, ok := normalizeName(name)
			if !ok {
				return nil, fmt.Errorf("fakedns: invalid poisoned name %q", name)
			}
			if !a.IsValid() {
				return nil, fmt.Errorf("fakedns: invalid poisoned address for %q", name)
			}
			s.poisoned[n] = a.Unmap()
		}
	}
	return s, nil
}

// normalizeZone copies z with lowercase names without a trailing dot. When strict, an invalid name
// or address is an error; otherwise it is dropped.
func normalizeZone(z map[string][]netip.Addr, strict bool) (map[string][]netip.Addr, error) {
	out := make(map[string][]netip.Addr, len(z))
	for name, addrs := range z {
		n, ok := normalizeName(name)
		if !ok {
			if strict {
				return nil, fmt.Errorf("fakedns: invalid zone name %q", name)
			}
			continue
		}
		list := make([]netip.Addr, 0, len(addrs))
		for _, a := range addrs {
			if !a.IsValid() {
				if strict {
					return nil, fmt.Errorf("fakedns: invalid address for %q", name)
				}
				continue
			}
			list = append(list, a.Unmap())
		}
		out[n] = append(out[n], list...)
	}
	return out, nil
}

// normalizeName lowercases name and strips one trailing dot; ok is false for an invalid name.
func normalizeName(name string) (string, bool) {
	n := strings.TrimSuffix(strings.ToLower(name), ".")
	if n == "" {
		return "", false
	}
	if _, ok := dns.IsDomainName(n); !ok {
		return "", false
	}
	return n, true
}

func addrPort(a net.Addr) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("fakedns: listener address: %w", err)
	}
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), nil
}

// watch closes s when ctx is done.
func (s *Server) watch(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-s.done:
		}
	}()
}

// Queries returns every logged query, oldest first (a copy).
func (s *Server) Queries() []Query {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Query(nil), s.queries...)
}

// SetZone atomically replaces the zone, for sites added during a scenario. Names are normalized
// like Config.Zone; invalid names and addresses are dropped.
func (s *Server) SetZone(z map[string][]netip.Addr) {
	zone, _ := normalizeZone(z, false)
	s.zone.Store(&zone)
}

// Close stops the server and waits for its goroutines. It is idempotent.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.mu.Lock()
		s.closing = true
		for c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
		for _, f := range s.closers {
			f()
		}
		s.wg.Wait()
	})
}

func (s *Server) log(q Query) {
	s.mu.Lock()
	s.queries = append(s.queries, q)
	s.mu.Unlock()
}

// answer builds the reply to req; via is "doh", "udp" or "tcp".
func (s *Server) answer(req *dns.Msg, from netip.AddrPort, via string) *dns.Msg {
	resp := new(dns.Msg)
	if req.Opcode != dns.OpcodeQuery {
		return resp.SetRcode(req, dns.RcodeNotImplemented)
	}
	if len(req.Question) != 1 {
		return resp.SetRcode(req, dns.RcodeFormatError)
	}
	resp.SetReply(req)
	resp.Authoritative = true
	resp.RecursionAvailable = true
	q := req.Question[0]
	name := strings.TrimSuffix(strings.ToLower(q.Name), ".")
	typ, ok := dns.TypeToString[q.Qtype]
	if !ok {
		typ = fmt.Sprintf("TYPE%d", q.Qtype)
	}
	s.log(Query{Name: name, Type: typ, From: netip.AddrPortFrom(from.Addr().Unmap(), from.Port()), Via: via, At: time.Now()})

	hdr := func(t uint16) dns.RR_Header {
		return dns.RR_Header{Name: q.Name, Rrtype: t, Class: dns.ClassINET, Ttl: s.ttl}
	}
	addAddrs := func(addrs []netip.Addr) {
		for _, a := range addrs {
			switch {
			case q.Qtype == dns.TypeA && a.Is4():
				resp.Answer = append(resp.Answer, &dns.A{Hdr: hdr(dns.TypeA), A: a.AsSlice()})
			case q.Qtype == dns.TypeAAAA && a.Is6():
				resp.Answer = append(resp.Answer, &dns.AAAA{Hdr: hdr(dns.TypeAAAA), AAAA: a.AsSlice()})
			}
		}
	}
	zone := *s.zone.Load()
	addrs, inZone := zone[name]
	switch q.Qtype {
	case dns.TypeA, dns.TypeAAAA:
		if a, ok := s.poison(name); ok {
			addAddrs([]netip.Addr{a})
			return resp
		}
		if !inZone {
			resp.Rcode = dns.RcodeNameError
			return resp
		}
		addAddrs(addrs)
	case dns.TypeTXT:
		if s.txt != nil {
			if txt := s.txt(name); len(txt) > 0 {
				for _, t := range txt {
					resp.Answer = append(resp.Answer, &dns.TXT{Hdr: hdr(dns.TypeTXT), Txt: chunk(t)})
				}
				return resp
			}
		}
		if !inZone {
			resp.Rcode = dns.RcodeNameError
		}
	}
	return resp
}

// poison returns the stub address for name in plain mode (longest suffix wins).
func (s *Server) poison(name string) (netip.Addr, bool) {
	best, found := "", false
	var addr netip.Addr
	for k, a := range s.poisoned {
		if (name == k || strings.HasSuffix(name, "."+k)) && (!found || len(k) > len(best)) {
			best, addr, found = k, a, true
		}
	}
	return addr, found
}

// chunk splits s into DNS character-strings of at most 255 bytes.
func chunk(s string) []string {
	var out []string
	for len(s) > 255 {
		out = append(out, s[:255])
		s = s[255:]
	}
	return append(out, s)
}

func (s *Server) serveDoH(w http.ResponseWriter, r *http.Request) {
	var wire []byte
	switch r.Method {
	case http.MethodGet:
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(r.URL.Query().Get("dns"), "="))
		if err != nil || len(b) == 0 {
			http.Error(w, "bad dns parameter", http.StatusBadRequest)
			return
		}
		wire = b
	case http.MethodPost:
		if ct := r.Header.Get("Content-Type"); !strings.EqualFold(strings.TrimSpace(strings.Split(ct, ";")[0]), dohType) {
			http.Error(w, "unsupported content type", http.StatusUnsupportedMediaType)
			return
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, maxMsg+1))
		if err != nil || len(b) == 0 || len(b) > maxMsg {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		wire = b
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req := new(dns.Msg)
	if err := req.Unpack(wire); err != nil {
		http.Error(w, "bad DNS message", http.StatusBadRequest)
		return
	}
	from, _ := netip.ParseAddrPort(r.RemoteAddr)
	out, err := s.answer(req, from, "doh").Pack()
	if err != nil {
		http.Error(w, "pack failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", dohType)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(out)
}

func (s *Server) serveUDP(pc net.PacketConn) {
	defer s.wg.Done()
	buf := make([]byte, maxMsg)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue // e.g. ICMP-induced errors on Windows (WSAECONNRESET)
		}
		req := new(dns.Msg)
		if req.Unpack(buf[:n]) != nil {
			continue
		}
		ap, _ := addrPort(addr)
		resp := s.answer(req, ap, "udp")
		size := dns.MinMsgSize
		if opt := req.IsEdns0(); opt != nil && int(opt.UDPSize()) > size {
			size = int(opt.UDPSize())
		}
		resp.Truncate(size)
		if out, err := resp.Pack(); err == nil {
			pc.WriteTo(out, addr)
		}
	}
}

func (s *Server) serveTCP(ln net.Listener) {
	defer s.wg.Done()
	for {
		c, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			time.Sleep(10 * time.Millisecond) // transient accept error
			continue
		}
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			c.Close()
			return
		}
		s.conns[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serveConn(c)
	}
}

func (s *Server) serveConn(c net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		c.Close()
	}()
	from, _ := addrPort(c.RemoteAddr())
	var lenBuf [2]byte
	for {
		c.SetDeadline(time.Now().Add(tcpIdle))
		if _, err := io.ReadFull(c, lenBuf[:]); err != nil {
			return
		}
		msg := make([]byte, binary.BigEndian.Uint16(lenBuf[:]))
		if _, err := io.ReadFull(c, msg); err != nil {
			return
		}
		req := new(dns.Msg)
		if req.Unpack(msg) != nil {
			return
		}
		out, err := s.answer(req, from, "tcp").Pack()
		if err != nil || len(out) > 0xffff {
			return
		}
		frame := make([]byte, 2+len(out))
		binary.BigEndian.PutUint16(frame, uint16(len(out)))
		copy(frame[2:], out)
		if _, err := c.Write(frame); err != nil {
			return
		}
	}
}
