package analyzer

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer/dialer"
	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
	"github.com/nikoejento/obhod/internal/lib/route"
)

// resetServer reads the ClientHello and closes: the client sees EOF after sending, i.e. TLS_RST.
func resetServer(t testing.TB) net.Conn {
	return pipeServer(t, tls.Certificate{}, func(s net.Conn) { s.Read(make([]byte, 4096)) })
}

func TestSNISwapConfirms(t *testing.T) {
	// The neutral-SNI handshake completes (with a certificate for another name): only the SNI is blocked.
	cert, pool := selfSigned(t, "other.test")
	d := dialer.NewFake("direct",
		dialer.Step{Conn: resetServer(t)},
		dialer.Step{Conn: pipeServer(t, cert, httpsHandler(cert, 200, ""))})
	p := testProber(t, pool, nil)
	r := p.Probe(context.Background(), testTarget, d, withSwap(testOpts(StageTCP|StageTLS|StageHTTP)))
	if r.Verdict != verdict.TLSRST || r.Blocking != verdict.BlockingConfirmed {
		t.Fatalf("%+v", r)
	}
	sw := r.Evidence.SNISwap
	if sw == nil || sw.Verdict != verdict.OK || sw.SNI != p.cfg.NeutralSNI[0] {
		t.Fatalf("swap: %+v", sw)
	}
}

func TestSNISwapAlsoFails(t *testing.T) {
	d := dialer.NewFake("direct", dialer.Step{Conn: resetServer(t)}, dialer.Step{Conn: resetServer(t)})
	r := testProber(t, nil, nil).Probe(context.Background(), testTarget, d, withSwap(testOpts(StageTCP|StageTLS)))
	if r.Verdict != verdict.TLSRST || r.Blocking != verdict.BlockingLikely || r.Evidence.SNISwap == nil || r.Evidence.SNISwap.Verdict != verdict.TLSRST {
		t.Fatalf("%+v", r)
	}
}

func TestSNISwapOffAndNotApplicable(t *testing.T) {
	d := dialer.NewFake("direct", dialer.Step{Conn: resetServer(t)})
	r := testProber(t, nil, nil).Probe(context.Background(), testTarget, d, testOpts(StageTCP|StageTLS))
	if r.Evidence.SNISwap != nil {
		t.Fatalf("swap ran although disabled: %+v", r.Evidence.SNISwap)
	}
	// A TCP failure is not swappable, and no extra dial may happen (the script has one step).
	d = dialer.NewFake("direct", dialer.Step{Err: context.DeadlineExceeded})
	r = testProber(t, nil, nil).Probe(context.Background(), testTarget, d, withSwap(testOpts(StageAll)))
	if r.Verdict != verdict.TCPTimeout || r.Evidence.SNISwap != nil {
		t.Fatalf("%+v", r)
	}
}

func withSwap(o Options) Options { o.SNISwap = true; return o }

func TestRepeatsMajority(t *testing.T) {
	cert, pool := selfSigned(t, "example.test")
	okConn := func() dialer.Step {
		return dialer.Step{Conn: pipeServer(t, cert, httpsHandler(cert, 200, ""))}
	}
	o := testOpts(StageTCP | StageTLS | StageHTTP)
	o.Repeats = 2

	r := testProber(t, pool, nil).Probe(context.Background(), testTarget, dialer.NewFake("direct", okConn(), okConn()), o)
	if r.Verdict != verdict.OK {
		t.Fatalf("two OK: %+v", r)
	}
	r = testProber(t, nil, nil).Probe(context.Background(), testTarget, dialer.NewFake("direct", dialer.Step{Err: context.DeadlineExceeded}, dialer.Step{Err: context.DeadlineExceeded}), o)
	if r.Verdict != verdict.TCPTimeout {
		t.Fatalf("two timeouts: %+v", r)
	}
	// Our own failure on every attempt stays a local error.
	r = testProber(t, nil, nil).Probe(context.Background(), testTarget, dialer.NewFake("direct"), o)
	if r.Verdict != verdict.LocalError {
		t.Fatalf("local: %+v", r)
	}
	// One local failure is ignored when another attempt gave a real answer.
	r = testProber(t, pool, nil).Probe(context.Background(), testTarget, dialer.NewFake("direct", okConn()), o)
	if r.Verdict != verdict.OK {
		t.Fatalf("local + ok: %+v", r)
	}
}

func TestConnLimited(t *testing.T) {
	cert, pool := selfSigned(t, "example.test")
	o := testOpts(StageTCP | StageTLS | StageHTTP)
	o.Repeats = 2
	d := dialer.NewFake("direct",
		dialer.Step{Conn: pipeServer(t, cert, httpsHandler(cert, 200, ""))},
		dialer.Step{Conn: resetServer(t)})
	r := testProber(t, pool, nil).Probe(context.Background(), testTarget, d, withSwap(o))
	if r.Verdict != verdict.ConnLimited || r.Blocking != verdict.BlockingUnknown || r.Stage != verdict.StageTLS {
		t.Fatalf("%+v", r)
	}
	if r.Evidence.SNISwap != nil {
		t.Fatal("no SNI swap for a connection limit")
	}
}

// muxDialer routes each address to its own scripted server.
type muxDialer struct{ by map[string]func() net.Conn }

func (muxDialer) Route() route.Route { return route.Direct }
func (m muxDialer) DialContext(ctx context.Context, _, addr string) (net.Conn, error) {
	if f := m.by[addr]; f != nil {
		return f(), nil
	}
	return nil, &net.OpError{Op: "dial", Err: context.DeadlineExceeded}
}

func TestCalibrate(t *testing.T) {
	cert, pool := selfSigned(t, "c1.test", "c2.test", "k1.test")
	good := func() net.Conn { return pipeServer(t, cert, httpsHandler(cert, 200, "")) }
	bad := func() net.Conn { return resetServer(t) }
	res := dnsMap(map[string]string{"c1.test": "192.0.2.1", "c2.test": "192.0.2.2", "k1.test": "192.0.2.3"})
	o := testOpts(StageAll)

	setup := func(control, canary []string) *prober {
		p := testProber(t, pool, res)
		p.cfg.ControlDomains, p.cfg.CanaryDomains = control, canary
		return p
	}
	cases := []struct {
		name          string
		p             *prober
		by            map[string]func() net.Conn
		internet, dpi CheckState
		warnings      int
	}{
		{"dpi present", setup([]string{"c1.test", "c2.test"}, []string{"k1.test"}),
			map[string]func() net.Conn{"192.0.2.1:443": good, "192.0.2.2:443": good, "192.0.2.3:443": bad}, CheckOK, CheckYes, 0},
		{"no dpi", setup([]string{"c1.test"}, []string{"k1.test"}),
			map[string]func() net.Conn{"192.0.2.1:443": good, "192.0.2.3:443": good}, CheckOK, CheckNo, 0},
		{"internet down", setup([]string{"c1.test", "c2.test"}, []string{"k1.test"}),
			map[string]func() net.Conn{}, CheckFailed, CheckUnknown, 2},
		{"one control down is still ok", setup([]string{"c1.test", "c2.test"}, nil),
			map[string]func() net.Conn{"192.0.2.1:443": good}, CheckOK, CheckUnknown, 1},
		{"no controls", setup(nil, []string{"k1.test"}),
			map[string]func() net.Conn{"192.0.2.3:443": bad}, CheckUnknown, CheckYes, 1},
		{"canary unreachable at tcp is a block too", setup([]string{"c1.test"}, []string{"k1.test"}),
			map[string]func() net.Conn{"192.0.2.1:443": good}, CheckOK, CheckYes, 0},
	}
	for _, c := range cases {
		got := c.p.calibrate(context.Background(), muxDialer{c.by}, o)
		if got.Internet != c.internet || got.DPIPresent != c.dpi || len(got.Warnings) != c.warnings {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func dnsMap(m map[string]string) Resolver {
	out := map[string][]netip.Addr{}
	for h, ip := range m {
		out[h] = []netip.Addr{netip.MustParseAddr(ip)}
	}
	return newFakeResolver(out)
}

// alertServer answers every ClientHello with a TLS alert, like a CDN edge given a name it does not host.
func alertServer(t testing.TB) net.Conn {
	cert, _ := selfSigned(t, "edge.test")
	return pipeServer(t, cert, func(s net.Conn) {
		tc := tls.Server(s, &tls.Config{Certificates: []tls.Certificate{cert}, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return nil, errors.New("unrecognized name")
		}})
		tc.Handshake()
	})
}

// silentServer reads the ClientHello and never answers: the DPI drop that shows up as TLS_TIMEOUT.
func silentServer(t testing.TB) net.Conn {
	return pipeServer(t, tls.Certificate{}, func(s net.Conn) {
		buf := make([]byte, 4096)
		for {
			if _, err := s.Read(buf); err != nil {
				return
			}
		}
	})
}

// Regression for a real-network report: with DPI dropping the real SNI (TLS_TIMEOUT) and the neutral SNI
// answered by a TLS alert, the suggestion must be desync, not tunnel.
func TestSNISwapAlertMeansSNIBlock(t *testing.T) {
	for _, first := range []struct {
		name string
		conn func(testing.TB) net.Conn
		want verdict.Verdict
	}{
		{"timeout", silentServer, verdict.TLSTimeout},
		{"reset", resetServer, verdict.TLSRST},
	} {
		t.Run(first.name, func(t *testing.T) {
			d := dialer.NewFake("direct", dialer.Step{Conn: first.conn(t)}, dialer.Step{Conn: alertServer(t)})
			o := withSwap(testOpts(StageTCP | StageTLS | StageHTTP))
			o.Timeouts.TLS = 200 * time.Millisecond
			r := testProber(t, nil, nil).Probe(context.Background(), testTarget, d, o)
			if r.Verdict != first.want || r.Evidence.SNISwap == nil || r.Evidence.SNISwap.Verdict != verdict.TLSAlert {
				t.Fatalf("setup: %+v", r)
			}
			if r.Blocking != verdict.BlockingConfirmed {
				t.Errorf("blocking %s, want confirmed", r.Blocking)
			}
			applySuggestion(&r, Calibration{Internet: CheckOK})
			if r.Suggestion != route.Desync {
				t.Errorf("suggestion %q (%s), want desync", r.Suggestion, r.SuggestionNote)
			}
		})
	}
}

// The neutral SNI failing the same way as the real one is a genuine IP block: tunnel, not confirmed.
func TestSNISwapSameFailureMeansIPBlock(t *testing.T) {
	d := dialer.NewFake("direct", dialer.Step{Conn: resetServer(t)}, dialer.Step{Conn: resetServer(t)})
	r := testProber(t, nil, nil).Probe(context.Background(), testTarget, d, withSwap(testOpts(StageTCP|StageTLS)))
	applySuggestion(&r, Calibration{Internet: CheckOK})
	if r.Blocking != verdict.BlockingLikely || r.Suggestion != route.Tunnel {
		t.Fatalf("%s %q", r.Blocking, r.Suggestion)
	}
}
