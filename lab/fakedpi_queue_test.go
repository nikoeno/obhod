//go:build linux && lab

package lab

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/faultserver"
	"github.com/nikoejento/obhod/internal/lib/testca"
	"github.com/nikoejento/obhod/lab/fakedpi"
	"github.com/nikoejento/obhod/lab/netns"
)

// The fakedpi queue path (lab L4b): cli ── isp (forward chain → NFQUEUE → fakedpi) ── srv.
const (
	qClientAddr = "192.168.90.2"
	qSiteAddr   = "203.0.113.10"
	qQueueNum   = 7
	qBodySize   = 64 << 10
)

func TestFakeDPIQueue(t *testing.T) {
	queuePreflight(t)
	prefix := "obq" + queueRandHex(t, 4)
	before := rootSnapshot(t)
	topo := netns.New(prefix)
	t.Cleanup(func() {
		if err := topo.Teardown(); err != nil {
			t.Errorf("Teardown: %v", err)
		}
		before.compare(t, rootSnapshot(t))
	})

	cli, isp, srv := queueTopology(t, topo)
	ctx := context.Background()

	// Queue every forwarded packet of isp (both directions), without bypass.
	for _, argv := range [][]string{
		{"nft", "add", "table", "inet", "oblq"},
		{"nft", "add", "chain", "inet", "oblq", "qfwd", "{", "type", "filter", "hook", "forward", "priority", "0", ";", "}"},
		{"nft", "add", "rule", "inet", "oblq", "qfwd", "queue", "num", strconv.Itoa(qQueueNum)},
	} {
		if out, err := topo.Exec(ctx, isp, argv...); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}

	eng := fakedpi.New(nil)
	var q *fakedpi.Queue
	if err := isp.Do(func() error {
		var err error
		q, err = eng.Listen(qQueueNum)
		return err
	}); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	go func() { runDone <- q.Run(runCtx) }()
	t.Cleanup(func() {
		cancelRun()
		select {
		case err := <-runDone:
			if err != nil {
				t.Logf("Queue.Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Queue.Run did not return after cancel")
		}
		if err := q.Close(); err != nil {
			t.Errorf("Queue.Close: %v", err)
		}
	})

	ca, err := testca.New()
	if err != nil {
		t.Fatal(err)
	}
	var site *faultserver.Server
	srvCtx, cancelSrv := context.WithCancel(ctx)
	if err := srv.Do(func() error {
		var err error
		site, err = faultserver.Run(srvCtx, net.JoinHostPort(qSiteAddr, "443"), faultserver.Spec{
			Behavior: faultserver.OK,
			Bytes:    qBodySize,
			Hosts:    []string{"allowed.test", "blocked.test", "sub.blocked.test"},
		}, ca)
		return err
	}); err != nil {
		cancelSrv()
		t.Fatalf("faultserver.Run: %v", err)
	}
	t.Cleanup(func() {
		cancelSrv()
		site.Close()
	})

	c := &queueClient{t: t, ns: cli, pool: ca.Pool}

	t.Run("no rules", func(t *testing.T) {
		c.t = t
		c.mustOK("allowed.test")
		c.mustOK("blocked.test")
		for _, sni := range []string{"allowed.test", "blocked.test"} {
			if !slices.ContainsFunc(site.Conns(), func(x faultserver.Conn) bool {
				return x.Remote.Addr() == netip.MustParseAddr(qClientAddr) && x.SNI == sni
			}) {
				t.Errorf("server log has no connection from %s with SNI %s: %+v", qClientAddr, sni, site.Conns())
			}
			if f, ok := seenFlow(eng, sni); !ok {
				t.Errorf("fakedpi saw no flow with SNI %s: %+v", sni, eng.Seen())
			} else if f.Proto != "tcp" || f.Src.Addr() != netip.MustParseAddr(qClientAddr) ||
				f.Dst != netip.AddrPortFrom(netip.MustParseAddr(qSiteAddr), 443) || f.Verdict != "" && f.Verdict != fakedpi.Accept {
				t.Errorf("flow %s = %+v", sni, f)
			}
		}
	})

	t.Run("naive sni", func(t *testing.T) {
		c.t = t
		eng.SetRules([]fakedpi.Rule{{Kind: fakedpi.NaiveSNI, Domains: []string{"blocked.test"}}})
		c.mustReset("blocked.test")
		c.mustReset("sub.blocked.test")
		c.mustOK("allowed.test")
		if n := eng.Stats()[fakedpi.NaiveSNI]; n == 0 {
			t.Errorf("Stats[NaiveSNI] = 0: %v", eng.Stats())
		}
		if f, ok := seenFlow(eng, "sub.blocked.test"); !ok || f.Verdict != fakedpi.Reset || f.Rule != fakedpi.NaiveSNI {
			t.Errorf("flow sub.blocked.test = %+v (found %v)", f, ok)
		}
	})

	t.Run("ip block", func(t *testing.T) {
		c.t = t
		eng.SetRules([]fakedpi.Rule{{Kind: fakedpi.IPBlock, Prefix: netip.MustParsePrefix(qSiteAddr + "/32")}})
		_, err := c.dial(2 * time.Second)
		var ne net.Error
		if err == nil || !errors.As(err, &ne) || !ne.Timeout() {
			t.Errorf("dial with IPBlock: want a timeout, got %v", err)
		}
		if n := eng.Stats()[fakedpi.IPBlock]; n == 0 {
			t.Errorf("Stats[IPBlock] = 0: %v", eng.Stats())
		}
	})

	t.Run("stall", func(t *testing.T) {
		c.t = t
		eng.SetRules([]fakedpi.Rule{{Kind: fakedpi.Stall, Domains: []string{"blocked.test"}, Bytes: 2000}})
		body, raw, err := c.fetch("blocked.test", 3*time.Second)
		var ne net.Error
		if err == nil || !errors.Is(err, os.ErrDeadlineExceeded) && !(errors.As(err, &ne) && ne.Timeout()) {
			t.Errorf("fetch with Stall: want a deadline error, got body %d, err %v", body, err)
		}
		if raw >= 4096 {
			t.Errorf("fetch with Stall received %d raw bytes, want < 4096", raw)
		}
		if n := eng.Stats()[fakedpi.Stall]; n == 0 {
			t.Errorf("Stats[Stall] = 0: %v", eng.Stats())
		}
		c.mustOK("allowed.test")
	})

	t.Run("rules cleared", func(t *testing.T) {
		c.t = t
		eng.SetRules(nil)
		for _, h := range []string{"allowed.test", "blocked.test", "sub.blocked.test"} {
			c.mustOK(h)
		}
	})
}

// queueTopology builds cli(eth0 192.168.90.2) ── isp(eth0 .90.1, eth1 203.0.113.1) ── srv(eth0 203.0.113.10).
func queueTopology(t *testing.T, topo *netns.Topology) (cli, isp, srv *netns.NS) {
	t.Helper()
	var err error
	for _, p := range []struct {
		ns   **netns.NS
		name string
	}{{&cli, "cli"}, {&isp, "isp"}, {&srv, "srv"}} {
		if *p.ns, err = topo.Add(p.name); err != nil {
			t.Fatal(err)
		}
	}
	if err := topo.Link(cli, isp, "eth0", "eth0",
		netip.MustParsePrefix(qClientAddr+"/24"), netip.MustParsePrefix("192.168.90.1/24")); err != nil {
		t.Fatal(err)
	}
	if err := topo.Link(isp, srv, "eth1", "eth0",
		netip.MustParsePrefix("203.0.113.1/24"), netip.MustParsePrefix(qSiteAddr+"/24")); err != nil {
		t.Fatal(err)
	}
	if err := topo.Route(cli, netip.MustParsePrefix("0.0.0.0/0"), netip.MustParseAddr("192.168.90.1")); err != nil {
		t.Fatal(err)
	}
	if err := topo.Route(srv, netip.MustParsePrefix("0.0.0.0/0"), netip.MustParseAddr("203.0.113.1")); err != nil {
		t.Fatal(err)
	}
	if err := topo.Forwarding(isp, true); err != nil {
		t.Fatal(err)
	}
	return cli, isp, srv
}

// queueClient dials from the client namespace and speaks TLS + HTTP/1.1 outside it (the socket
// keeps its namespace).
type queueClient struct {
	t    *testing.T
	ns   *netns.NS
	pool *x509.CertPool
}

func (c *queueClient) dial(timeout time.Duration) (net.Conn, error) {
	var conn net.Conn
	err := c.ns.Do(func() error {
		var err error
		conn, err = (&net.Dialer{Timeout: timeout}).Dial("tcp", net.JoinHostPort(qSiteAddr, "443"))
		return err
	})
	return conn, err
}

// fetch does GET / on host and returns the body length, the raw bytes read from TCP and the error.
func (c *queueClient) fetch(host string, timeout time.Duration) (int64, int64, error) {
	conn, err := c.dial(timeout)
	if err != nil {
		return 0, 0, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	cc := &countingConn{Conn: conn}
	conn.SetDeadline(time.Now().Add(timeout))
	tc := tls.Client(cc, &tls.Config{
		RootCAs:    c.pool,
		ServerName: host,
		MinVersion: tls.VersionTLS12,
		// X25519 only: the default hybrid post-quantum key share makes the ClientHello larger than
		// one segment, and NaiveSNI by design only reads a hello that fits one segment.
		CurvePreferences: []tls.CurveID{tls.X25519},
	})
	if err := tc.Handshake(); err != nil {
		return 0, cc.n, fmt.Errorf("handshake: %w", err)
	}
	if _, err := io.WriteString(tc, "GET / HTTP/1.1\r\nHost: "+host+"\r\nConnection: close\r\n\r\n"); err != nil {
		return 0, cc.n, fmt.Errorf("write request: %w", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
	if err != nil {
		return 0, cc.n, fmt.Errorf("read response: %w", err)
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return n, cc.n, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return n, cc.n, fmt.Errorf("status %s", resp.Status)
	}
	return n, cc.n, nil
}

func (c *queueClient) mustOK(host string) {
	c.t.Helper()
	n, _, err := c.fetch(host, 5*time.Second)
	if err != nil {
		c.t.Errorf("GET %s: %v", host, err)
	} else if n != qBodySize {
		c.t.Errorf("GET %s: body %d bytes, want %d", host, n, qBodySize)
	}
}

func (c *queueClient) mustReset(host string) {
	c.t.Helper()
	_, _, err := c.fetch(host, 5*time.Second)
	if err == nil || !errors.Is(err, syscall.ECONNRESET) && !strings.Contains(err.Error(), "connection reset") {
		c.t.Errorf("GET %s: want a connection reset, got %v", host, err)
	}
}

type countingConn struct {
	net.Conn
	n int64
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.n += int64(n)
	return n, err
}

func seenFlow(e *fakedpi.Engine, sni string) (fakedpi.Flow, bool) {
	for _, f := range e.Seen() {
		if f.SNI == sni {
			return f, true
		}
	}
	return fakedpi.Flow{}, false
}

// queuePreflight skips the test unless it runs as root with ip, nft and the NFQUEUE modules.
func queuePreflight(t *testing.T) {
	t.Helper()
	var missing []string
	if os.Geteuid() != 0 {
		missing = append(missing, "root")
	}
	for _, bin := range []string{"ip", "nft"} {
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}
	mods, _ := os.ReadFile("/proc/modules")
	for _, m := range []string{"nfnetlink_queue", "nft_queue"} {
		listed := slices.ContainsFunc(strings.Split(string(mods), "\n"), func(l string) bool {
			return strings.HasPrefix(l, m+" ")
		})
		if _, err := os.Stat("/sys/module/" + m); !listed && err != nil {
			missing = append(missing, "kernel module "+m)
		}
	}
	if len(missing) > 0 {
		t.Skipf("needs %s (setup: modprobe nfnetlink_queue nft_queue; apt-get install nftables)", strings.Join(missing, ", "))
	}
}

func queueRandHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)[:n]
}

// rootState is what the lab must leave untouched in the root namespace.
type rootState struct {
	route, route6, nft string
	links, netns       []string // names, without other lab runs' transient ones
}

// transientName reports names that lab runs (this one or concurrent ones) create and remove.
func transientName(s string) bool {
	for _, p := range []string{"obt", "obl", "obq"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func rootSnapshot(t *testing.T) rootState {
	t.Helper()
	run := func(argv ...string) string {
		out, err := exec.Command(argv[0], argv[1:]...).Output()
		if err != nil {
			t.Fatalf("%s: %v", strings.Join(argv, " "), err)
		}
		return string(out)
	}
	var s rootState
	s.route = run("ip", "route")
	s.route6 = run("ip", "-6", "route")
	s.nft = run("nft", "list", "ruleset")
	for _, line := range strings.Split(run("ip", "-o", "link", "show"), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimSuffix(f[1], ":"), "@")
		if !transientName(name) {
			s.links = append(s.links, name)
		}
	}
	for _, line := range strings.Split(run("ip", "netns", "list"), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && !transientName(f[0]) {
			s.netns = append(s.netns, f[0])
		}
	}
	slices.Sort(s.links)
	slices.Sort(s.netns)
	return s
}

func (s rootState) compare(t *testing.T, after rootState) {
	t.Helper()
	for _, c := range []struct{ what, a, b string }{
		{"ip route", s.route, after.route},
		{"ip -6 route", s.route6, after.route6},
		{"nft list ruleset", s.nft, after.nft},
	} {
		if c.a != c.b {
			t.Errorf("root %s changed:\n%s\n---\n%s", c.what, c.a, c.b)
		}
	}
	if !slices.Equal(s.links, after.links) {
		t.Errorf("root links changed: %q -> %q", s.links, after.links)
	}
	if !slices.Equal(s.netns, after.netns) {
		t.Errorf("root namespaces changed: %q -> %q", s.netns, after.netns)
	}
}
