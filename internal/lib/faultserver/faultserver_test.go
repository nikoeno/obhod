//go:build lab

package faultserver

import (
	"crypto/tls"
	"errors"
	"github.com/nikoejento/obhod/internal/lib/testca"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func start(t testing.TB, s Spec) *Server {
	t.Helper()
	ca, err := testca.New()
	if err != nil {
		t.Fatal(err)
	}
	return Start(t, s, ca)
}

func dial(t *testing.T, s *Server) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", s.Addr.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(2 * time.Second))
	return c
}

func TestRefused(t *testing.T) {
	s := start(t, Spec{Behavior: RSTOnSYN})
	if _, err := net.DialTimeout("tcp", s.Addr.String(), time.Second); err == nil {
		t.Fatal("connected to a closed port")
	}
}

func TestRSTAfterHello(t *testing.T) {
	c := dial(t, start(t, Spec{Behavior: RSTAfterHello}))
	c.Write([]byte("client hello"))
	_, err := c.Read(make([]byte, 1))
	if err == nil || (!errors.Is(err, syscall.ECONNRESET) && !strings.Contains(err.Error(), "reset") && !strings.Contains(err.Error(), "forcibly")) {
		t.Fatalf("want a reset, got %v", err)
	}
}

func TestDropHello(t *testing.T) {
	c := dial(t, start(t, Spec{Behavior: DropHello}))
	c.SetDeadline(time.Now().Add(200 * time.Millisecond))
	c.Write([]byte("client hello"))
	_, err := c.Read(make([]byte, 1))
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("want a timeout, got %v", err)
	}
}

func TestAlertAndGarbage(t *testing.T) {
	c := dial(t, start(t, Spec{Behavior: TLSAlert, AlertCode: 70}))
	c.Write([]byte("hello"))
	b := make([]byte, 7)
	if _, err := io.ReadFull(c, b); err != nil || b[0] != 21 || b[6] != 70 {
		t.Fatalf("alert record: %v %v", b, err)
	}
	c = dial(t, start(t, Spec{Behavior: Garbage}))
	c.Write([]byte("hello"))
	b = make([]byte, 5)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "HTTP/" {
		t.Fatalf("garbage: %q %v", b, err)
	}
}

func TestCertsAndHTTP(t *testing.T) {
	for name, tc := range map[string]struct {
		b     Behavior
		trust bool // the client trusts srv.Pool and checks the name
		ok    bool
	}{
		"valid": {OK, true, true}, "wrong host": {WrongCert, true, false}, "self signed": {SelfSignedCert, true, false},
	} {
		s := start(t, Spec{Behavior: tc.b})
		conn, err := tls.Dial("tcp", s.Addr.String(), &tls.Config{RootCAs: s.Pool, ServerName: "example.test"})
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v", name, err)
		}
		if err == nil {
			conn.Close()
		}
	}
}

func TestOKServesBytes(t *testing.T) {
	s := start(t, Spec{Behavior: OK, Bytes: 5000})
	conn, err := tls.Dial("tcp", s.Addr.String(), &tls.Config{RootCAs: s.Pool, ServerName: "example.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: example.test\r\nConnection: close\r\n\r\n")
	all, _ := io.ReadAll(conn)
	if i := strings.Index(string(all), "\r\n\r\n"); i < 0 || len(all)-i-4 != 5000 {
		t.Fatalf("body length wrong, total %d", len(all))
	}
}

func TestStubPlainHTTP(t *testing.T) {
	s := start(t, Spec{Behavior: HTTPStub, StubURL: "http://stub.test/x"})
	c := dial(t, s)
	io.WriteString(c, "GET / HTTP/1.1\r\nHost: blocked.test\r\n\r\n")
	all, _ := io.ReadAll(c)
	if !strings.Contains(string(all), "302") || !strings.Contains(string(all), "Location: http://stub.test/x") {
		t.Fatalf("%q", all)
	}
}

func TestStallAfter(t *testing.T) {
	s := start(t, Spec{Behavior: StallAfter, Direction: "tx", Bytes: 20000})
	conn, err := tls.Dial("tcp", s.Addr.String(), &tls.Config{RootCAs: s.Pool, ServerName: "example.test"})
	if err != nil {
		t.Skipf("handshake already exceeds the stall size: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: example.test\r\n\r\n")
	n, _ := io.Copy(io.Discard, conn)
	if n == 0 || n >= 20000 {
		t.Fatalf("received %d application bytes, want a partial body", n)
	}
}

func TestConnLimit(t *testing.T) {
	s := start(t, Spec{Behavior: ConnLimit, Limit: 1})
	first := dial(t, s)
	_ = first
	time.Sleep(50 * time.Millisecond)
	second := dial(t, s)
	second.Write([]byte("hello"))
	if _, err := second.Read(make([]byte, 1)); err == nil {
		t.Fatal("second connection was served")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	s := start(t, Spec{Behavior: OK})
	s.Close()
	s.Close()
}

// tlsDial connects raw, then runs a TLS handshake for name; it returns the connection and the
// handshake error (the connection stays usable for asserting on what the server logged).
func tlsDial(t *testing.T, s *Server, name string) (*tls.Conn, error) {
	t.Helper()
	raw := dial(t, s)
	c := tls.Client(raw, &tls.Config{RootCAs: s.Pool, ServerName: name})
	t.Cleanup(func() { c.Close() })
	return c, c.Handshake()
}

func localAddr(t *testing.T, c net.Conn) netip.AddrPort {
	t.Helper()
	return c.LocalAddr().(*net.TCPAddr).AddrPort()
}

// onlyConn returns the single logged connection.
func onlyConn(t *testing.T, s *Server) Conn {
	t.Helper()
	cs := s.Conns()
	if len(cs) != 1 {
		t.Fatalf("logged %d connections, want 1: %+v", len(cs), cs)
	}
	return cs[0]
}

func TestConnsOK(t *testing.T) {
	s := start(t, Spec{Behavior: OK, Bytes: 5000})
	before := time.Now()
	c, err := tlsDial(t, s, "example.test")
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(c, "GET / HTTP/1.1\r\nHost: example.test\r\nConnection: close\r\n\r\n")
	n, _ := io.Copy(io.Discard, c) // EOF: the server wrote everything and closed
	got := onlyConn(t, s)
	if got.Remote != localAddr(t, c) || got.SNI != "example.test" {
		t.Fatalf("logged %+v, want remote %v and SNI example.test", got, localAddr(t, c))
	}
	if got.At.Before(before.Add(-time.Second)) || got.At.After(time.Now()) {
		t.Fatalf("accept time %v out of range", got.At)
	}
	if got.Bytes <= n || got.Bytes < 5000 {
		t.Fatalf("Bytes = %d, want more than the %d plaintext bytes received", got.Bytes, n)
	}
}

func TestConnsReset(t *testing.T) {
	for _, spec := range []Spec{{Behavior: RSTAfterHello}, {Behavior: SNIBlock, BlockSNI: "blocked.test"}} {
		t.Run(string(spec.Behavior), func(t *testing.T) {
			s := start(t, spec)
			c, err := tlsDial(t, s, "blocked.test")
			if err == nil {
				t.Fatal("handshake succeeded")
			}
			got := onlyConn(t, s)
			if got.Remote != localAddr(t, c) || got.SNI != "blocked.test" || got.Bytes != 0 {
				t.Fatalf("logged %+v, want remote %v, SNI blocked.test, no bytes", got, localAddr(t, c))
			}
		})
	}
}

func TestConnsStalled(t *testing.T) {
	s := start(t, Spec{Behavior: StallAfter, Direction: "tx", Bytes: 20000})
	c, err := tlsDial(t, s, "example.test")
	if err != nil {
		t.Skipf("handshake already exceeds the stall size: %v", err)
	}
	c.SetDeadline(time.Now().Add(300 * time.Millisecond))
	io.WriteString(c, "GET / HTTP/1.1\r\nHost: example.test\r\n\r\n")
	io.Copy(io.Discard, c)
	got := onlyConn(t, s)
	if got.Remote != localAddr(t, c) || got.SNI != "example.test" {
		t.Fatalf("logged %+v, want remote %v and SNI example.test", got, localAddr(t, c))
	}
	// wire counts both directions toward the stall, so the server wrote at most Bytes.
	if got.Bytes <= 0 || got.Bytes > 20000 {
		t.Fatalf("Bytes = %d, want 1..20000", got.Bytes)
	}
}

func TestConnsPlainHTTPHasNoSNI(t *testing.T) {
	s := start(t, Spec{Behavior: HTTPStub, StubURL: "http://stub.test/x"})
	c := dial(t, s)
	io.WriteString(c, "GET / HTTP/1.1\r\nHost: blocked.test\r\n\r\n")
	io.ReadAll(c)
	if got := onlyConn(t, s); got.SNI != "" || got.Remote != localAddr(t, c) || got.Bytes == 0 {
		t.Fatalf("logged %+v", got)
	}
}

func TestConnsReturnsCopy(t *testing.T) {
	s := start(t, Spec{Behavior: RSTAfterHello})
	c, _ := tlsDial(t, s, "a.test")
	cs := s.Conns()
	cs[0].SNI = "changed"
	cs[0].Bytes = 42
	if got := onlyConn(t, s); got.SNI != "a.test" || got.Bytes != 0 || got.Remote != localAddr(t, c) {
		t.Fatalf("the log changed through a returned slice: %+v", got)
	}
}

func TestConnsConcurrent(t *testing.T) {
	const n = 20
	var hosts []string
	for i := range n {
		hosts = append(hosts, string(rune('a'+i))+".example.test")
	}
	s := start(t, Spec{Behavior: OK, Bytes: 100, Hosts: hosts})
	want := make(map[netip.AddrPort]string)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := hosts[i]
			raw, err := net.DialTimeout("tcp", s.Addr.String(), time.Second)
			if err != nil {
				t.Error(err)
				return
			}
			defer raw.Close()
			raw.SetDeadline(time.Now().Add(2 * time.Second))
			c := tls.Client(raw, &tls.Config{RootCAs: s.Pool, ServerName: name})
			if err := c.Handshake(); err != nil {
				t.Error(err)
				return
			}
			io.WriteString(c, "GET / HTTP/1.1\r\nHost: "+name+"\r\nConnection: close\r\n\r\n")
			io.Copy(io.Discard, c)
			mu.Lock()
			want[raw.LocalAddr().(*net.TCPAddr).AddrPort()] = name
			mu.Unlock()
			s.Conns() // reads race with writers
		}()
	}
	wg.Wait()
	cs := s.Conns()
	if len(cs) != n {
		t.Fatalf("logged %d connections, want %d", len(cs), n)
	}
	for i, c := range cs {
		if want[c.Remote] != c.SNI || c.Bytes == 0 {
			t.Errorf("conn %d: %+v, want SNI %q and bytes", i, c, want[c.Remote])
		}
		if i > 0 && c.At.Before(cs[i-1].At) {
			t.Errorf("conn %d accepted before conn %d", i, i-1)
		}
	}
}
