//go:build lab

package faultserver

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Behavior selects how the server misbehaves.
type Behavior string

// The behaviors.
const (
	OK              Behavior = "ok"                // valid TLS + HTTP/1.1, serves N bytes
	RSTOnSYN        Behavior = "rst-on-syn"        // closed port (refused)
	BlackholeSYN    Behavior = "blackhole-syn"     // accept queue never served → client connect timeout (best effort on localhost)
	RSTAfterHello   Behavior = "rst-after-hello"   // read ClientHello, then RST
	DropHello       Behavior = "drop-hello"        // read ClientHello, never answer
	TLSAlert        Behavior = "tls-alert"         // send a TLS alert record
	Garbage         Behavior = "garbage"           // send non-TLS bytes after hello
	WrongCert       Behavior = "wrong-cert"        // cert for another host
	SelfSignedCert  Behavior = "self-signed"       // cert no CA signed
	StallAfter      Behavior = "stall-after"       // stop moving data after N bytes (either direction)
	RSTAfterRequest Behavior = "rst-after-request" // RST after the first HTTP request
	HTTPStub        Behavior = "http-stub"         // plain HTTP 302 to a stub URL
	ConnLimit       Behavior = "conn-limit"        // reset handshakes after N concurrent conns
	SNIBlock        Behavior = "sni-block"         // valid TLS server that RSTs ClientHellos whose SNI is BlockSNI
)

// Spec describes one server.
type Spec struct {
	Behavior  Behavior
	Bytes     int    // OK: response size; StallAfter: byte count (wire bytes, both directions, TLS overhead included)
	Direction string // StallAfter: "rx" (server stops reading) | "tx" (server stops writing)
	AlertCode uint8  // TLSAlert
	StubURL   string // HTTPStub
	Limit     int    // ConnLimit
	BlockSNI  string // SNIBlock: the server name that gets a RST
	// RejectUnknownSNI (SNIBlock): answer names outside Hosts with a TLS alert, like a CDN edge that does not host them.
	RejectUnknownSNI bool
	Hosts            []string
}

// Certs supplies the certificates a server presents (implemented by testca.CA; defined here because
// a test-only module cannot import another one from non-test code).
type Certs interface {
	RootPool() *x509.CertPool
	Issue(hosts ...string) (tls.Certificate, error)
	IssueWrongHost(hosts ...string) (tls.Certificate, error)
	IssueSelfSigned(hosts ...string) (tls.Certificate, error)
}

// Server is a running fault server.
type Server struct {
	Addr netip.AddrPort
	Pool *x509.CertPool // trust this in the client under test

	spec   Spec
	hosts  map[string]bool
	ln     net.Listener
	cert   tls.Certificate
	closed chan struct{}
	once   sync.Once
	wg     sync.WaitGroup
	active atomic.Int32

	mu    sync.Mutex
	conns []net.Conn
}

// Start runs a server bound to 127.0.0.1:0; it is closed via t.Cleanup.
func Start(t testing.TB, s Spec, ca Certs) *Server {
	t.Helper()
	srv, err := Run(context.Background(), "127.0.0.1:0", s, ca)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv
}

// Run is the non-testing form for the lab. It stops when ctx is done or Close is called.
func Run(ctx context.Context, listen string, s Spec, ca Certs) (*Server, error) {
	hosts := s.Hosts
	if len(hosts) == 0 {
		hosts = []string{"example.test", "localhost", "127.0.0.1"}
	}
	srv := &Server{spec: s, Pool: ca.RootPool(), closed: make(chan struct{}), hosts: map[string]bool{}}
	for _, h := range hosts {
		srv.hosts[h] = true
	}
	var err error
	switch s.Behavior {
	case WrongCert:
		srv.cert, err = ca.IssueWrongHost(hosts...)
	case SelfSignedCert:
		srv.cert, err = ca.IssueSelfSigned(hosts...)
	default:
		srv.cert, err = ca.Issue(hosts...)
	}
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	srv.ln = ln
	srv.Addr = netip.MustParseAddrPort(ln.Addr().String())
	switch s.Behavior {
	case RSTOnSYN:
		// Nothing listens any more: connects are refused. The address stays reserved only until
		// another process grabs the port, which is fine for a short test.
		ln.Close()
		return srv, nil
	case BlackholeSYN:
		// The kernel completes the handshake but nobody ever serves the connection.
		return srv, nil
	}
	srv.wg.Add(1)
	go srv.acceptLoop()
	go func() {
		select {
		case <-ctx.Done():
			srv.Close()
		case <-srv.closed:
		}
	}()
	return srv, nil
}

// Close stops the server and drops every open connection.
func (s *Server) Close() {
	s.once.Do(func() {
		close(s.closed)
		s.ln.Close()
		s.mu.Lock()
		for _, c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, c)
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer c.Close()
			s.handle(c)
		}()
	}
}

// rst closes c with a TCP RST.
func rst(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetLinger(0)
	}
	c.Close()
}

// readHello reads whatever the client sends first (the ClientHello).
func readHello(c net.Conn) error {
	buf := make([]byte, 16<<10)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := c.Read(buf)
	c.SetReadDeadline(time.Time{})
	return err
}

func (s *Server) handle(c net.Conn) {
	n := s.active.Add(1)
	defer s.active.Add(-1)
	switch s.spec.Behavior {
	case RSTAfterHello:
		if readHello(c) == nil {
			rst(c)
		}
	case DropHello:
		if readHello(c) == nil {
			<-s.closed
		}
	case TLSAlert:
		if readHello(c) == nil {
			code := s.spec.AlertCode
			if code == 0 {
				code = 40 // handshake_failure
			}
			c.Write([]byte{21, 3, 3, 0, 2, 2, code})
		}
	case Garbage:
		if readHello(c) == nil {
			c.Write([]byte("HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n"))
		}
	case ConnLimit:
		if int(n) > s.spec.Limit {
			readHello(c)
			rst(c)
			return
		}
		s.serveTLS(c)
	case StallAfter:
		s.serveTLS(&wire{Conn: c, spec: s.spec, closed: s.closed})
	default: // OK, WrongCert, SelfSignedCert, RSTAfterRequest, HTTPStub
		s.serveTLS(c)
	}
}

func (s *Server) serveTLS(c net.Conn) {
	if s.spec.Behavior == HTTPStub {
		// Plain HTTP (stub learning) is answered without TLS; the first TLS record starts with 0x16.
		br := bufio.NewReader(c)
		if b, err := br.Peek(1); err != nil {
			return
		} else if b[0] != 0x16 {
			s.serveHTTP(c, br)
			return
		}
		c = &prefixConn{Conn: c, r: br}
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{s.cert}, NextProtos: []string{"http/1.1"}}
	if s.spec.Behavior == SNIBlock {
		cfg.GetConfigForClient = func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			if h.ServerName == s.spec.BlockSNI {
				rst(underlying(c))
				return nil, errors.New("faultserver: SNI blocked")
			}
			if s.spec.RejectUnknownSNI && !s.hosts[h.ServerName] {
				return nil, errors.New("faultserver: unrecognized name")
			}
			return nil, nil
		}
	}
	tc := tls.Server(c, cfg)
	if err := tc.Handshake(); err != nil {
		return
	}
	s.serveHTTP(tc, bufio.NewReader(tc))
}

// prefixConn reads through a bufio.Reader that already peeked at the stream.
type prefixConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *prefixConn) Read(b []byte) (int, error) { return p.r.Read(b) }

func (s *Server) serveHTTP(w net.Conn, br *bufio.Reader) {
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		switch s.spec.Behavior {
		case RSTAfterRequest:
			rst(underlying(w))
			return
		case HTTPStub:
			fmt.Fprintf(w, "HTTP/1.1 302 Found\r\nLocation: %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", s.spec.StubURL)
			return
		}
		size := s.spec.Bytes
		if s.spec.Behavior == StallAfter {
			size = 1 << 20 // large enough that the stall, not the end of the object, stops the transfer
		} else if size == 0 {
			size = 1024
		}
		switch req.Method {
		case http.MethodHead:
			fmt.Fprintf(w, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nContent-Type: application/octet-stream\r\n\r\n", size)
		case http.MethodPost:
			io.Copy(io.Discard, req.Body)
			io.WriteString(w, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
		default:
			if _, err := fmt.Fprintf(w, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nContent-Type: application/octet-stream\r\n\r\n", size); err != nil {
				return
			}
			chunk := make([]byte, 4096)
			for i := range chunk {
				chunk[i] = 'x'
			}
			for left := size; left > 0; {
				n := min(left, len(chunk))
				if _, err := w.Write(chunk[:n]); err != nil {
					return
				}
				left -= n
			}
		}
		if req.Close {
			return
		}
	}
}

// underlying returns the raw TCP connection below a TLS connection (or c itself).
func underlying(c net.Conn) net.Conn {
	if tc, ok := c.(*tls.Conn); ok {
		c = tc.NetConn()
	}
	switch x := c.(type) {
	case *wire:
		return x.Conn
	case *prefixConn:
		return x.Conn
	}
	return c
}

// wire counts bytes on the raw connection and freezes the flow after the configured amount.
type wire struct {
	net.Conn
	spec   Spec
	closed <-chan struct{}
	n      atomic.Int64
}

func (w *wire) freeze() error {
	<-w.closed
	return errors.New("faultserver: stalled")
}

func (w *wire) Read(b []byte) (int, error) {
	if w.spec.Direction == "rx" && w.n.Load() >= int64(w.spec.Bytes) {
		return 0, w.freeze()
	}
	n, err := w.Conn.Read(b)
	w.n.Add(int64(n))
	return n, err
}

func (w *wire) Write(b []byte) (int, error) {
	if w.spec.Direction != "rx" {
		left := int64(w.spec.Bytes) - w.n.Load()
		if left <= 0 {
			return 0, w.freeze()
		}
		if int64(len(b)) > left {
			n, err := w.Conn.Write(b[:left])
			w.n.Add(int64(n))
			if err != nil {
				return n, err
			}
			return n, w.freeze()
		}
	}
	n, err := w.Conn.Write(b)
	w.n.Add(int64(n))
	return n, err
}
