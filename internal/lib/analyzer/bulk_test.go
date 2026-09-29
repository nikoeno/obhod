package analyzer

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
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer/dialer"
	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
)

// stallConn freezes the flow once limit wire bytes (both directions) have moved.
type stallConn struct {
	net.Conn
	limit  int64
	dir    string // "tx": stop writing; "rx": stop reading
	done   <-chan struct{}
	mu     sync.Mutex
	n      int64
	frozen bool
}

func (s *stallConn) count(n int) {
	s.mu.Lock()
	s.n += int64(n)
	s.mu.Unlock()
}

func (s *stallConn) left() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.limit - s.n
}

func (s *stallConn) freeze() error {
	<-s.done
	return errors.New("stalled")
}

func (s *stallConn) Read(b []byte) (int, error) {
	if s.limit > 0 && s.dir == "rx" && s.left() <= 0 {
		return 0, s.freeze()
	}
	n, err := s.Conn.Read(b)
	s.count(n)
	return n, err
}

func (s *stallConn) Write(b []byte) (int, error) {
	if s.limit > 0 && s.dir == "tx" {
		left := s.left()
		if left <= 0 {
			return 0, s.freeze()
		}
		if int64(len(b)) > left {
			n, err := s.Conn.Write(b[:left])
			s.count(n)
			if err != nil {
				return n, err
			}
			return n, s.freeze()
		}
	}
	n, err := s.Conn.Write(b)
	s.count(n)
	return n, err
}

// bulkServer serves HEAD, GET (size bytes) and POST on a pipe, stalling after limit wire bytes.
func bulkServer(t testing.TB, host string, size int, limit int64, dir string) (net.Conn, *x509.CertPool) {
	t.Helper()
	cert, pool := selfSigned(t, host)
	c, s := net.Pipe()
	done := make(chan struct{})
	t.Cleanup(func() { close(done); c.Close(); s.Close() })
	go func() {
		tc := tls.Server(&stallConn{Conn: s, limit: limit, dir: dir, done: done}, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}})
		if tc.Handshake() != nil {
			return
		}
		br := bufio.NewReader(tc)
		for {
			req, err := http.ReadRequest(br)
			if err != nil {
				return
			}
			switch req.Method {
			case http.MethodHead:
				fmt.Fprintf(tc, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", size)
			case http.MethodPost:
				if _, err := io.Copy(io.Discard, req.Body); err != nil {
					return
				}
				io.WriteString(tc, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
			default:
				fmt.Fprintf(tc, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", size)
				chunk := make([]byte, 1024)
				for left := size; left > 0; left -= len(chunk) {
					if _, err := tc.Write(chunk[:min(left, len(chunk))]); err != nil {
						return
					}
				}
			}
		}
	}()
	return c, pool
}

func TestBulk(t *testing.T) {
	cases := []struct {
		name  string
		mode  string
		size  int   // object size the server serves
		limit int64 // stall after this many wire bytes (0 = never)
		dir   string
		bulk  int // Bulk.Size
		want  Verdict
	}{
		{"download ok", "download", 200_000, 0, "tx", 64 << 10, verdict.OK},
		{"download small object", "download", 500, 0, "tx", 64 << 10, verdict.OK},
		{"download stall at 16 KB", "download", 200_000, 16_000, "tx", 64 << 10, verdict.Stall1620},
		{"download stall at 50 KB", "download", 500_000, 50_000, "tx", 128 << 10, verdict.ReadTimeout},
		{"download stall at 2 KB", "download", 200_000, 6_000, "tx", 64 << 10, verdict.ReadTimeout},
		{"upload ok", "upload", 0, 0, "rx", 64 << 10, verdict.OK},
		{"upload stall at 16 KB", "upload", 0, 16_000, "rx", 64 << 10, verdict.Stall1620},
		{"upload stall at 50 KB", "upload", 0, 50_000, "rx", 128 << 10, verdict.ReadTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn, pool := bulkServer(t, "example.test", c.size, c.limit, c.dir)
			o := testOpts(StageAll)
			o.Bulk.Mode, o.Bulk.Size = c.mode, c.bulk
			o.Timeouts.Bulk = 800 * time.Millisecond
			r := testProber(t, pool, nil).Probe(context.Background(), testTarget, dialer.NewFake("direct", dialer.Step{Conn: conn}), o)
			if r.Verdict != c.want || r.Stage != verdict.StageBulk {
				t.Fatalf("got %s at %s, want %s at bulk\n%+v", r.Verdict, r.Stage, c.want, r.Evidence)
			}
			if c.want == verdict.OK && r.Evidence.StageTimes[verdict.StageBulk] <= 0 && r.Evidence.RxBytes == 0 {
				t.Fatalf("no bulk evidence: %+v", r.Evidence)
			}
			if c.want != verdict.OK && r.Evidence.Err == "" {
				t.Fatalf("no error recorded: %+v", r.Evidence)
			}
			if c.limit > 0 {
				if got := r.Evidence.RxBytes + r.Evidence.TxBytes; got < c.limit-2000 || got > c.limit+5000 {
					t.Errorf("stalled at %d wire bytes, want about %d", got, c.limit)
				}
			}
		})
	}
}

func TestBulkTimeout(t *testing.T) {
	if got := bulkTimeout(10*time.Second, 10*time.Millisecond); got != 2500*time.Millisecond {
		t.Errorf("fast link: %v", got)
	}
	if got := bulkTimeout(10*time.Second, time.Second); got != 6*time.Second {
		t.Errorf("slow link: %v", got)
	}
	if got := bulkTimeout(2*time.Second, 5*time.Second); got != 2*time.Second {
		t.Errorf("capped: %v", got)
	}
}
