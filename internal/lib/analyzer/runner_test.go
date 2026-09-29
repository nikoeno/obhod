package analyzer

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer/dialer"
	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/proc"
	"github.com/nikoejento/obhod/internal/lib/route"
)

func testRunner(t testing.TB, p *prober) *runner {
	t.Helper()
	p.cfg.Jitter = [2]time.Duration{}
	return &runner{p: p, jitter: func(lo, hi time.Duration) time.Duration { return lo }}
}

func tcpOnly() Options {
	o := testOpts(StageTCP)
	return o
}

// countingDialer counts concurrent dials overall and per address, holding each briefly so overlaps show up.
type countingDialer struct {
	cur, maxAll atomic.Int32
	mu          sync.Mutex
	perIP       map[string]int
	maxPerIP    map[string]int
}

func newCountingDialer() *countingDialer {
	return &countingDialer{perIP: map[string]int{}, maxPerIP: map[string]int{}}
}

func (c *countingDialer) Route() route.Route { return route.Direct }

func (c *countingDialer) DialContext(ctx context.Context, _, addr string) (net.Conn, error) {
	n := c.cur.Add(1)
	defer c.cur.Add(-1)
	for {
		m := c.maxAll.Load()
		if n <= m || c.maxAll.CompareAndSwap(m, n) {
			break
		}
	}
	c.mu.Lock()
	c.perIP[addr]++
	c.maxPerIP[addr] = max(c.maxPerIP[addr], c.perIP[addr])
	c.mu.Unlock()
	time.Sleep(5 * time.Millisecond) // widen the window in which a limit violation would be seen
	c.mu.Lock()
	c.perIP[addr]--
	c.mu.Unlock()
	a, b := net.Pipe()
	b.Close()
	return a, nil
}

func manyTargets(n int, sameIP bool) ([]Target, Resolver) {
	var ts []Target
	m := map[string]string{}
	for i := 0; i < n; i++ {
		d := fmt.Sprintf("h%d.test", i)
		ip := fmt.Sprintf("192.0.2.%d", i+1)
		if sameIP {
			ip = "192.0.2.1"
		}
		m[d] = ip
		ts = append(ts, Target{Domain: d})
	}
	return ts, dnsMap(m)
}

func TestRunnerLimits(t *testing.T) {
	t.Run("global concurrency", func(t *testing.T) {
		ts, res := manyTargets(12, false)
		p := testProber(t, nil, res)
		p.cfg.Concurrency, p.cfg.PerIPConcurrency = 3, 1
		p.cfg.ControlDomains, p.cfg.CanaryDomains = nil, nil
		d := newCountingDialer()
		rep, err := testRunner(t, p).Run(context.Background(), Batch{Targets: ts, Dialers: []Dialer{d}, Options: tcpOnly()})
		if err != nil || len(rep.Results) != 12 {
			t.Fatalf("%v %d", err, len(rep.Results))
		}
		if got := d.maxAll.Load(); got > 3 {
			t.Fatalf("%d concurrent dials with Concurrency 3", got)
		}
	})
	t.Run("per IP", func(t *testing.T) {
		ts, res := manyTargets(8, true)
		p := testProber(t, nil, res)
		p.cfg.Concurrency, p.cfg.PerIPConcurrency = 8, 1
		p.cfg.ControlDomains, p.cfg.CanaryDomains = nil, nil
		d := newCountingDialer()
		if _, err := testRunner(t, p).Run(context.Background(), Batch{Targets: ts, Dialers: []Dialer{d}, Options: tcpOnly()}); err != nil {
			t.Fatal(err)
		}
		if got := d.maxPerIP["192.0.2.1:443"]; got != 1 {
			t.Fatalf("%d concurrent probes to one IP with PerIPConcurrency 1", got)
		}
	})
}

func TestRunnerOrderAndJitter(t *testing.T) {
	ts, res := manyTargets(5, false)
	fake := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := DefaultConfig()
	cfg.DoH = nil
	cfg.Concurrency = 2
	cfg.ControlDomains, cfg.CanaryDomains = nil, nil
	p, err := newProber(cfg, Deps{Clock: fake, Resolver: res})
	if err != nil {
		t.Fatal(err)
	}
	var sleeps atomic.Int32
	r := &runner{p: p, jitter: func(lo, hi time.Duration) time.Duration { sleeps.Add(1); return 250 * time.Millisecond }}

	done := make(chan struct{})
	var rep Report
	go func() {
		defer close(done)
		rep, err = r.Run(context.Background(), Batch{Targets: ts, Dialers: []Dialer{newCountingDialer()}, Options: tcpOnly()})
	}()
	// Advance the fake clock whenever a worker waits on jitter.
	for running := true; running; {
		select {
		case <-done:
			running = false
		default:
			if fake.Waiters() > 0 {
				fake.Advance(250 * time.Millisecond)
			} else {
				time.Sleep(time.Millisecond)
			}
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if sleeps.Load() != 5 {
		t.Errorf("jitter drawn %d times, want once per target", sleeps.Load())
	}
	if len(rep.Results) != 5 {
		t.Fatalf("%d results", len(rep.Results))
	}
	for i, res := range rep.Results {
		if res.Target.Domain != ts[i].Domain {
			t.Errorf("result %d is %s, want %s (order of Targets)", i, res.Target.Domain, ts[i].Domain)
		}
	}
	if !rep.StartedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) || rep.Schema != ReportSchema || rep.Version == "" {
		t.Errorf("%+v", rep)
	}
}

func TestRunnerEndToEnd(t *testing.T) {
	cert, pool := selfSigned(t, "c1.test", "k1.test", "ok.test", "rst.test")
	good := func() net.Conn { return pipeServer(t, cert, httpsHandler(cert, 200, "")) }
	bad := func() net.Conn { return resetServer(t) }
	res := dnsMap(map[string]string{"c1.test": "192.0.2.1", "k1.test": "192.0.2.2", "ok.test": "192.0.2.3", "rst.test": "192.0.2.4", "gone.test": "192.0.2.5"})
	p := testProber(t, pool, res)
	p.cfg.ControlDomains, p.cfg.CanaryDomains = []string{"c1.test"}, []string{"k1.test"}
	d := muxDialer{map[string]func() net.Conn{
		"192.0.2.1:443": good, "192.0.2.2:443": bad, "192.0.2.3:443": good, "192.0.2.4:443": bad,
		// 192.0.2.5 does not answer: TCP timeout
	}}
	o := testOpts(StageTCP | StageTLS | StageHTTP)
	rep, err := testRunner(t, p).Run(context.Background(), Batch{
		Targets: []Target{{Domain: "ok.test"}, {Domain: "rst.test"}, {Domain: "gone.test"}, {Domain: "nx.test"}},
		Dialers: []Dialer{d}, Options: o})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Calibration.Internet != CheckOK || rep.Calibration.DPIPresent != CheckYes {
		t.Fatalf("calibration %+v", rep.Calibration)
	}
	want := []struct {
		v   Verdict
		sug route.Route
	}{{verdict.OK, route.Direct}, {verdict.TLSRST, route.Tunnel}, {verdict.TCPTimeout, route.Tunnel}, {verdict.DNSFail, ""}}
	if len(rep.Results) != len(want) {
		t.Fatalf("%d results", len(rep.Results))
	}
	for i, w := range want {
		if r := rep.Results[i]; r.Verdict != w.v || r.Suggestion != w.sug {
			t.Errorf("%s: %s → %q, want %s → %q", r.Target.Domain, r.Verdict, r.Suggestion, w.v, w.sug)
		}
	}
}

func TestRunnerCalibrationFailed(t *testing.T) {
	res := dnsMap(map[string]string{"c1.test": "192.0.2.1", "ok.test": "192.0.2.3"})
	p := testProber(t, nil, res)
	p.cfg.ControlDomains, p.cfg.CanaryDomains = []string{"c1.test"}, nil
	rep, err := testRunner(t, p).Run(context.Background(), Batch{Targets: []Target{{Domain: "ok.test"}}, Dialers: []Dialer{muxDialer{}}, Options: tcpOnly()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Calibration.Internet != CheckFailed || len(rep.Warnings) == 0 {
		t.Fatalf("%+v", rep)
	}
	if rep.Results[0].Suggestion != "" {
		t.Fatalf("suggestion despite failed calibration: %+v", rep.Results[0])
	}
}

func TestRunnerCalibrateOnly(t *testing.T) {
	p := testProber(t, nil, dnsMap(map[string]string{"c1.test": "192.0.2.1"}))
	p.cfg.ControlDomains, p.cfg.CanaryDomains = []string{"c1.test"}, nil
	rep, err := testRunner(t, p).Run(context.Background(), Batch{Dialers: []Dialer{muxDialer{}}, Options: tcpOnly()})
	if err != nil || len(rep.Results) != 0 || rep.Calibration.Internet != CheckFailed {
		t.Fatalf("%v %+v", err, rep)
	}
}

func TestRunnerInvalidBatch(t *testing.T) {
	p := testProber(t, nil, nil)
	r := testRunner(t, p)
	d := dialer.NewFake(route.Direct)
	for name, b := range map[string]Batch{
		"no dialer":   {Targets: []Target{{Domain: "a.test"}}},
		"two dialers": {Targets: []Target{{Domain: "a.test"}}, Dialers: []Dialer{d, d}},
		"bad domain":  {Targets: []Target{{Domain: "https://a.test"}}, Dialers: []Dialer{d}},
		"bad options": {Targets: []Target{{Domain: "a.test"}}, Dialers: []Dialer{d}, Options: Options{Fingerprint: "opera"}},
	} {
		if _, err := r.Run(context.Background(), b); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestRunnerCancelled(t *testing.T) {
	ts, res := manyTargets(4, false)
	p := testProber(t, nil, res)
	p.cfg.ControlDomains, p.cfg.CanaryDomains = nil, nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep, err := testRunner(t, p).Run(ctx, Batch{Targets: ts, Dialers: []Dialer{newCountingDialer()}, Options: tcpOnly()})
	if err == nil {
		t.Fatal("cancelled run must fail")
	}
	if len(rep.Results) == len(ts) {
		t.Fatal("cancelled run produced every result")
	}
}

func TestNewRunnerConfigErrors(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Concurrency = 0
	if _, err := NewRunner(cfg, Deps{}); err == nil {
		t.Error("NewRunner accepted an invalid config")
	}
	if _, err := NewProber(cfg, Deps{}); err == nil {
		t.Error("NewProber accepted an invalid config")
	}
	if _, err := NewRunner(DefaultConfig(), Deps{}); err != nil {
		t.Errorf("default config: %v", err)
	}
}

func echoHandler(cert tls.Certificate, body string) func(net.Conn) {
	return func(s net.Conn) {
		tc := tls.Server(s, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}})
		if tc.Handshake() != nil {
			return
		}
		if _, err := http.ReadRequest(bufio.NewReader(tc)); err != nil {
			return
		}
		fmt.Fprintf(tc, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
		time.Sleep(50 * time.Millisecond)
	}
}

func TestPublicIPAndASN(t *testing.T) {
	cert, pool := selfSigned(t, "echo.test")
	serve := func(body string) muxDialer {
		return muxDialer{map[string]func() net.Conn{"192.0.2.50:443": func() net.Conn { return pipeServer(t, cert, echoHandler(cert, body)) }}}
	}
	p := testProber(t, pool, nil)
	p.cfg.PublicIPEndpoint = "https://echo.test/ip"
	p.res = withTXT{newFakeResolver(map[string][]netip.Addr{"echo.test": {netip.MustParseAddr("192.0.2.50")}}), map[string][]string{
		"9.113.0.203.origin.asn.cymru.com": {"64500 | 203.0.113.0/24 | ZZ | test | 2020-01-01"},
		"as64500.asn.cymru.com":            {"64500 | ZZ | test | 2020-01-01 | EXAMPLE-NET, ZZ"},
	}}

	var rep Report
	n := p.networkInfo(context.Background(), serve("203.0.113.9\n"), &rep)
	if n.PublicIP.String() != "203.0.113.9" || n.ASN.Number != 64500 || n.ASN.Name != "EXAMPLE-NET, ZZ" || len(rep.Warnings) != 0 {
		t.Fatalf("%+v %v", n, rep.Warnings)
	}

	rep = Report{}
	if n := p.networkInfo(context.Background(), serve("<html>not an ip</html>"), &rep); n.PublicIP.IsValid() || len(rep.Warnings) != 1 {
		t.Fatalf("garbage answer: %+v %v", n, rep.Warnings)
	}

	rep = Report{}
	if n := p.networkInfo(context.Background(), muxDialer{}, &rep); n.PublicIP.IsValid() || len(rep.Warnings) != 1 {
		t.Fatalf("unreachable endpoint: %+v %v", n, rep.Warnings)
	}

	// Public IP known, ASN not resolvable: keep the IP and warn.
	p.res = withTXT{newFakeResolver(map[string][]netip.Addr{"echo.test": {netip.MustParseAddr("192.0.2.50")}}), nil}
	rep = Report{}
	if n := p.networkInfo(context.Background(), serve("203.0.113.9"), &rep); n.PublicIP.String() != "203.0.113.9" || n.ASN.Number != 0 || len(rep.Warnings) != 1 {
		t.Fatalf("no ASN: %+v %v", n, rep.Warnings)
	}

	// No endpoint configured: nothing is asked.
	p.cfg.PublicIPEndpoint = ""
	rep = Report{}
	if n := p.networkInfo(context.Background(), muxDialer{}, &rep); n.PublicIP.IsValid() || len(rep.Warnings) != 0 {
		t.Fatalf("%+v %v", n, rep.Warnings)
	}
}

// withTXT adds TXT answers to a resolver.
type withTXT struct {
	Resolver
	txt map[string][]string
}

func (w withTXT) LookupTXT(_ context.Context, name string) ([]string, error) {
	if v, ok := w.txt[strings.ToLower(name)]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("lookup %s: no such domain", name)
}

// errLister fails the process scan.
type errLister struct{}

func (errLister) List(context.Context) ([]proc.Process, error) {
	return nil, fmt.Errorf("access denied")
}

func TestBypassWarnings(t *testing.T) {
	p := testProber(t, nil, nil)
	p.cfg.BypassProcesses = []string{"winws", "sing-box"}

	if got := p.bypassWarnings(context.Background()); got != nil {
		t.Fatalf("no lister configured: %v", got)
	}
	p.procs = proc.NewFake(proc.Process{PID: 10, Name: "chrome"}, proc.Process{PID: 4242, Name: "winws"}, proc.Process{PID: 77, Name: "sing-box"})
	got := p.bypassWarnings(context.Background())
	if len(got) != 2 || !strings.Contains(got[0], "winws (pid 4242)") || !strings.Contains(got[1], "sing-box (pid 77)") {
		t.Fatalf("%v", got)
	}
	p.procs = proc.NewFake(proc.Process{PID: 10, Name: "chrome"})
	if got := p.bypassWarnings(context.Background()); len(got) != 0 {
		t.Fatalf("clean system: %v", got)
	}
	p.procs = errLister{}
	if got := p.bypassWarnings(context.Background()); len(got) != 1 || !strings.Contains(got[0], "process scan failed") {
		t.Fatalf("scan failure: %v", got)
	}
	p.procs = proc.NewFake(proc.Process{PID: 1, Name: "winws"})
	p.cfg.BypassProcesses = nil
	if got := p.bypassWarnings(context.Background()); got != nil {
		t.Fatalf("empty list: %v", got)
	}
}

func TestRunnerReportsBypassTool(t *testing.T) {
	p := testProber(t, nil, dnsMap(map[string]string{"c1.test": "192.0.2.1"}))
	p.cfg.ControlDomains, p.cfg.CanaryDomains = []string{"c1.test"}, nil
	p.cfg.BypassProcesses = []string{"winws"}
	p.procs = proc.NewFake(proc.Process{PID: 4242, Name: "winws"})
	rep, err := testRunner(t, p).Run(context.Background(), Batch{Dialers: []Dialer{muxDialer{}}, Options: tcpOnly()})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range rep.Calibration.Warnings {
		found = found || strings.Contains(w, "winws (pid 4242)")
	}
	if !found {
		t.Fatalf("bypass warning missing: %+v", rep.Calibration)
	}
}
