//go:build lab

package faultserver

import (
	"crypto/tls"
	"errors"
	"github.com/nikoejento/obhod/internal/lib/testca"
	"io"
	"net"
	"strings"
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
