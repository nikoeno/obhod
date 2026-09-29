package analyzer

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer/dialer"
	"github.com/nikoejento/obhod/internal/lib/analyzer/dnscheck"
	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
)

func selfSigned(t testing.TB, hosts ...string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: hosts[0]}, DNSNames: hosts,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// pipeServer serves one HTTPS connection on a net.Pipe and returns the client end.
func pipeServer(t testing.TB, cert tls.Certificate, handle func(net.Conn)) net.Conn {
	t.Helper()
	c, s := net.Pipe()
	go func() {
		defer s.Close()
		handle(s)
	}()
	t.Cleanup(func() { c.Close() })
	_ = cert
	return c
}

func httpsHandler(cert tls.Certificate, status int, location string) func(net.Conn) {
	return func(s net.Conn) {
		tc := tls.Server(s, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}})
		if tc.Handshake() != nil {
			return
		}
		req, err := http.ReadRequest(bufio.NewReader(tc))
		if err != nil {
			return
		}
		_ = req
		resp := "HTTP/1.1 " + itoa(status) + " " + http.StatusText(status) + "\r\nContent-Length: 0\r\n"
		if location != "" {
			resp += "Location: " + location + "\r\n"
		}
		tc.Write([]byte(resp + "\r\n"))
		// linger so the client can read
		time.Sleep(50 * time.Millisecond)
	}
}

func itoa(n int) string {
	return string(rune('0'+n/100)) + string(rune('0'+n/10%10)) + string(rune('0'+n%10))
}

func testProber(t testing.TB, roots *x509.CertPool, res Resolver) *prober {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DoH = nil // never reach the real DoH endpoints from unit tests
	p, err := newProber(cfg, Deps{Resolver: res})
	if err != nil {
		t.Fatal(err)
	}
	p.roots = roots
	return p
}

var testTarget = Target{Domain: "example.test", IP: netip.MustParseAddr("192.0.2.10")}

func testOpts(stages StageSet) Options {
	o := DefaultConfig().Options
	o.Stages, o.Repeats, o.Fingerprint, o.SNISwap = stages, 1, "go", false
	o.Timeouts.TLS, o.Timeouts.HTTP = 2*time.Second, 2*time.Second
	return o
}

func TestProbeOK(t *testing.T) {
	cert, pool := selfSigned(t, "example.test")
	d := dialer.NewFake("direct", dialer.Step{Addr: "192.0.2.10:443", Conn: pipeServer(t, cert, httpsHandler(cert, 200, ""))})
	r := testProber(t, pool, nil).Probe(context.Background(), testTarget, d, testOpts(StageTCP|StageTLS|StageHTTP))
	if r.Verdict != verdict.OK || r.Stage != verdict.StageHTTP || r.Blocking != verdict.BlockingNone {
		t.Fatalf("%+v", r)
	}
	if r.Evidence.HTTPStatus != 200 || r.Evidence.TLSVersion == "" || r.Evidence.CertSubject == "" || r.Evidence.RTT <= 0 && r.Evidence.StageTimes[verdict.StageTCP] < 0 {
		t.Fatalf("evidence: %+v", r.Evidence)
	}
	if r.Route != "direct" || r.Target.Port != 443 || r.Duration < 0 {
		t.Fatalf("%+v", r)
	}
}

func TestProbeRedirect(t *testing.T) {
	cert, pool := selfSigned(t, "example.test")
	d := dialer.NewFake("direct", dialer.Step{Conn: pipeServer(t, cert, httpsHandler(cert, 301, "https://www.example.test/"))})
	r := testProber(t, pool, nil).Probe(context.Background(), testTarget, d, testOpts(StageTCP|StageTLS|StageHTTP))
	if r.Verdict != verdict.OKRedirect || r.Evidence.Location != "https://www.example.test/" {
		t.Fatalf("%+v", r)
	}
}

func TestProbeStub(t *testing.T) {
	cert, pool := selfSigned(t, "example.test")
	d := dialer.NewFake("direct", dialer.Step{Conn: pipeServer(t, cert, httpsHandler(cert, 302, "http://stub.isp.test/blocked"))})
	p := testProber(t, pool, nil)
	p.stubs.AddHost("stub.isp.test")
	r := p.Probe(context.Background(), testTarget, d, testOpts(StageTCP|StageTLS|StageHTTP))
	if r.Verdict != verdict.HTTPStub || r.Blocking != verdict.BlockingConfirmed {
		t.Fatalf("%+v", r)
	}
}

func TestProbeMITM(t *testing.T) {
	cert, _ := selfSigned(t, "example.test")
	d := dialer.NewFake("direct", dialer.Step{Conn: pipeServer(t, cert, httpsHandler(cert, 200, ""))})
	r := testProber(t, nil, nil).Probe(context.Background(), testTarget, d, testOpts(StageTCP|StageTLS|StageHTTP))
	if r.Verdict != verdict.TLSMITM || r.Stage != verdict.StageVerify || r.Evidence.CertError == "" {
		t.Fatalf("%+v", r)
	}
}

func TestProbeTCPFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want Verdict
	}{
		"refused": {&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, verdict.TCPRefused},
		"timeout": {context.DeadlineExceeded, verdict.TCPTimeout},
		"local":   {verdict.Local(net.UnknownNetworkError("no uplink")), verdict.LocalError},
	} {
		d := dialer.NewFake("direct", dialer.Step{Err: tc.err})
		r := testProber(t, nil, nil).Probe(context.Background(), testTarget, d, testOpts(StageAll))
		if r.Verdict != tc.want || r.Stage != verdict.StageTCP || r.Evidence.Err == "" {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestProbeHelloDropped(t *testing.T) {
	// The peer reads the ClientHello and closes: the client sees EOF after sending → TLS_RST.
	d := dialer.NewFake("direct", dialer.Step{Conn: pipeServer(t, tls.Certificate{}, func(s net.Conn) {
		buf := make([]byte, 4096)
		s.Read(buf)
	})})
	r := testProber(t, nil, nil).Probe(context.Background(), testTarget, d, testOpts(StageTCP|StageTLS))
	if r.Verdict != verdict.TLSRST || r.Stage != verdict.StageTLS || r.Evidence.TxBytes == 0 {
		t.Fatalf("%+v", r)
	}
}

func TestProbeTCPOnly(t *testing.T) {
	c, s := net.Pipe()
	defer s.Close()
	r := testProber(t, nil, nil).Probe(context.Background(), testTarget, dialer.NewFake("direct", dialer.Step{Conn: c}), testOpts(StageTCP))
	if r.Verdict != verdict.OK || r.Stage != verdict.StageTCP {
		t.Fatalf("%+v", r)
	}
}

func TestProbeResolve(t *testing.T) {
	tg := Target{Domain: "example.test"}
	d := dialer.NewFake("direct")

	r := testProber(t, nil, nil).Probe(context.Background(), tg, d, testOpts(StageAll))
	if r.Verdict != verdict.LocalError {
		t.Fatalf("no resolver: %+v", r)
	}
	r = testProber(t, nil, dnscheck.NewFake(nil, nil)).Probe(context.Background(), tg, d, testOpts(StageAll))
	if r.Verdict != verdict.DNSFail || r.Stage != verdict.StageResolve {
		t.Fatalf("nxdomain: %+v", r)
	}
	// Resolved address is used for the dial.
	ip := netip.MustParseAddr("192.0.2.77")
	res := dnscheck.NewFake(map[string][]netip.Addr{"example.test": {ip}}, nil)
	d = dialer.NewFake("direct", dialer.Step{Addr: "192.0.2.77:443", Err: context.DeadlineExceeded})
	r = testProber(t, nil, res).Probe(context.Background(), tg, d, testOpts(StageAll))
	if r.Verdict != verdict.TCPTimeout || r.IP != ip {
		t.Fatalf("resolved: %+v", r)
	}
}

func TestProbeInvalid(t *testing.T) {
	d := dialer.NewFake("direct")
	r := testProber(t, nil, nil).Probe(context.Background(), Target{Domain: "https://bad"}, d, testOpts(StageAll))
	if r.Verdict != verdict.LocalError {
		t.Fatalf("%+v", r)
	}
	bad := testOpts(StageAll)
	bad.Fingerprint = "opera"
	r = testProber(t, nil, nil).Probe(context.Background(), testTarget, d, bad)
	if r.Verdict != verdict.LocalError {
		t.Fatalf("%+v", r)
	}
}

func TestProbeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := testProber(t, nil, nil).Probe(ctx, testTarget, dialer.NewFake("direct", dialer.Step{Err: context.Canceled}), testOpts(StageAll))
	if r.Verdict != verdict.LocalError {
		t.Fatalf("%+v", r)
	}
}

func TestRootClassify(t *testing.T) {
	if v := Classify(verdict.StageTCP, &net.OpError{Err: syscall.ECONNREFUSED}, 0, 0); v != verdict.TCPRefused {
		t.Fatal(v)
	}
}

func fakeAnswers(host string, ips ...string) Resolver {
	var as []netip.Addr
	for _, s := range ips {
		as = append(as, netip.MustParseAddr(s))
	}
	return dnscheck.NewFake(map[string][]netip.Addr{host: as}, nil)
}

func TestProbeDNSPoisoned(t *testing.T) {
	tg := Target{Domain: "example.test"}
	for name, sys := range map[string]Resolver{
		"bogon":    fakeAnswers("example.test", "10.10.10.10"),
		"nxdomain": dnscheck.NewFake(nil, nil),
	} {
		p := testProber(t, nil, fakeAnswers("example.test", "192.0.2.10"))
		p.sys = sys
		r := p.Probe(context.Background(), tg, dialer.NewFake("direct"), testOpts(StageAll))
		if r.Verdict != verdict.DNSPoisoned || r.Stage != verdict.StageResolve || r.IP.String() != "192.0.2.10" || r.Blocking != verdict.BlockingLikely || r.Evidence.Err == "" {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestProbeDNSFakeIPAndCDN(t *testing.T) {
	tg := Target{Domain: "example.test"}
	for name, sysIP := range map[string]string{"fakeip": "198.18.0.7", "cdn": "192.0.2.99"} {
		p := testProber(t, nil, fakeAnswers("example.test", "192.0.2.10"))
		p.sys = fakeAnswers("example.test", sysIP)
		// The DoH address is dialled and refuses; the point is that DNS did not stop the probe.
		d := dialer.NewFake("direct", dialer.Step{Addr: "192.0.2.10:443", Err: context.DeadlineExceeded})
		if name == "cdn" {
			// A differing answer is checked once on plain HTTP; the fake server has nothing to say.
			d = dialer.NewFake("direct", dialer.Step{Addr: "192.0.2.99:80", Err: context.DeadlineExceeded}, dialer.Step{Addr: "192.0.2.10:443", Err: context.DeadlineExceeded})
		}
		r := p.Probe(context.Background(), tg, d, testOpts(StageAll))
		if r.Verdict != verdict.TCPTimeout {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestProbeDNSLearnsStub(t *testing.T) {
	c, s := net.Pipe()
	go func() {
		defer s.Close()
		s.Read(make([]byte, 1024))
		io.WriteString(s, "HTTP/1.1 302 Found\r\nLocation: http://block.isp.test/x\r\nContent-Length: 0\r\n\r\n")
	}()
	p := testProber(t, nil, fakeAnswers("example.test", "192.0.2.10"))
	p.sys = fakeAnswers("example.test", "192.0.2.99")
	d := dialer.NewFake("direct", dialer.Step{Addr: "192.0.2.99:80", Conn: c})
	r := p.Probe(context.Background(), Target{Domain: "example.test"}, d, testOpts(StageAll))
	if r.Verdict != verdict.DNSPoisoned || !p.stubs.HasHost("block.isp.test") || !p.stubs.HasIP(netip.MustParseAddr("192.0.2.99")) {
		t.Fatalf("%+v", r)
	}
}

func TestLookupASNRoot(t *testing.T) {
	r := dnscheck.NewFake(nil, map[string][]string{"10.2.0.192.origin.asn.cymru.com": {"64500 | 192.0.2.0/24 | ZZ | test | 2020-01-01"}})
	a, err := LookupASN(context.Background(), r, netip.MustParseAddr("192.0.2.10"))
	if err != nil || a.Number != 64500 {
		t.Fatalf("%+v %v", a, err)
	}
}

func newFakeResolver(m map[string][]netip.Addr) Resolver { return dnscheck.NewFake(m, nil) }
