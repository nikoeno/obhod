//go:build lab

package analyzer

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
	"github.com/nikoejento/obhod/internal/lib/faultserver"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/lib/testca"
)

func startFault(t testing.TB, spec faultserver.Spec) *faultserver.Server {
	t.Helper()
	ca, err := testca.New()
	if err != nil {
		t.Fatal(err)
	}
	return faultserver.Start(t, spec, ca)
}

func labTarget(srv *faultserver.Server) Target {
	return Target{Domain: "example.test", IP: srv.Addr.Addr(), Port: srv.Addr.Port()}
}

func labOpts(stages StageSet) Options {
	o := DefaultConfig().Options
	o.Stages, o.Repeats, o.Fingerprint = stages, 1, "chrome"
	o.SNISwap = false
	o.Timeouts.TCP = time.Second
	o.Timeouts.TLS = 500 * time.Millisecond
	o.Timeouts.HTTP = 500 * time.Millisecond
	o.Timeouts.Bulk = time.Second
	return o
}

func labProbe(t *testing.T, spec faultserver.Spec, o Options, stubs ...string) ProbeResult {
	t.Helper()
	srv := startFault(t, spec)
	p := testProber(t, srv.Pool, nil)
	for _, h := range stubs {
		p.stubs.AddHost(h)
	}
	d, err := NewDirectDialer(directOptionsForTests())
	if err != nil {
		t.Fatal(err)
	}
	return p.Probe(context.Background(), labTarget(srv), d, o)
}

// TestFaultServerVerdicts reproduces each wire-level verdict with a localhost fault server.
func TestFaultServerVerdicts(t *testing.T) {
	const all = StageTCP | StageTLS | StageHTTP
	cases := []struct {
		name  string
		spec  faultserver.Spec
		stubs []string
		want  Verdict
		stage Stage
	}{
		{"ok", faultserver.Spec{Behavior: faultserver.OK}, nil, verdict.OK, verdict.StageHTTP},
		{"refused", faultserver.Spec{Behavior: faultserver.RSTOnSYN}, nil, verdict.TCPRefused, verdict.StageTCP},
		{"blackhole accepts but never answers", faultserver.Spec{Behavior: faultserver.BlackholeSYN}, nil, verdict.TLSTimeout, verdict.StageTLS},
		{"rst after hello", faultserver.Spec{Behavior: faultserver.RSTAfterHello}, nil, verdict.TLSRST, verdict.StageTLS},
		{"drop hello", faultserver.Spec{Behavior: faultserver.DropHello}, nil, verdict.TLSTimeout, verdict.StageTLS},
		{"tls alert", faultserver.Spec{Behavior: faultserver.TLSAlert}, nil, verdict.TLSAlert, verdict.StageTLS},
		{"garbage", faultserver.Spec{Behavior: faultserver.Garbage}, nil, verdict.TLSSpoof, verdict.StageTLS},
		{"wrong cert", faultserver.Spec{Behavior: faultserver.WrongCert}, nil, verdict.TLSMITM, verdict.StageVerify},
		{"self signed", faultserver.Spec{Behavior: faultserver.SelfSignedCert}, nil, verdict.TLSMITM, verdict.StageVerify},
		{"http stub", faultserver.Spec{Behavior: faultserver.HTTPStub, StubURL: "http://stub.isp.test/blocked"}, []string{"stub.isp.test"}, verdict.HTTPStub, verdict.StageHTTP},
		{"redirect elsewhere", faultserver.Spec{Behavior: faultserver.HTTPStub, StubURL: "https://www.example.test/"}, []string{"stub.isp.test"}, verdict.OKRedirect, verdict.StageHTTP},
		{"rst after request", faultserver.Spec{Behavior: faultserver.RSTAfterRequest}, nil, verdict.HTTPRST, verdict.StageHTTP},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := labProbe(t, c.spec, labOpts(all), c.stubs...)
			if r.Verdict != c.want || r.Stage != c.stage {
				t.Fatalf("got %s at %s, want %s at %s\nevidence: %+v", r.Verdict, r.Stage, c.want, c.stage, r.Evidence)
			}
			if c.want != verdict.OK && r.Evidence.Err == "" && r.Evidence.CertError == "" && r.Evidence.Location == "" {
				t.Fatalf("no evidence kept: %+v", r.Evidence)
			}
		})
	}
}

func TestFaultServerFingerprints(t *testing.T) {
	for _, fp := range []string{"chrome", "firefox", "safari", "go"} {
		t.Run(fp, func(t *testing.T) {
			o := labOpts(StageTCP | StageTLS | StageHTTP)
			o.Fingerprint = fp
			r := labProbe(t, faultserver.Spec{Behavior: faultserver.OK}, o)
			if r.Verdict != verdict.OK || r.Evidence.HTTPStatus != 200 {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func TestFaultServerConnLimitHeld(t *testing.T) {
	srv := startFault(t, faultserver.Spec{Behavior: faultserver.ConnLimit, Limit: 1})
	hold, err := net.Dial("tcp", srv.Addr.String()) // takes the only slot
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	time.Sleep(50 * time.Millisecond)
	p := testProber(t, srv.Pool, nil)
	d, _ := NewDirectDialer(directOptionsForTests())
	r := p.Probe(context.Background(), labTarget(srv), d, labOpts(StageTCP|StageTLS))
	if r.Verdict != verdict.TLSRST {
		t.Fatalf("%+v", r)
	}
}

func TestFaultServerBulk(t *testing.T) {
	cases := []struct {
		name string
		spec faultserver.Spec
		size int
		want Verdict
	}{
		{"full download", faultserver.Spec{Behavior: faultserver.OK, Bytes: 200_000}, 64 << 10, verdict.OK},
		{"stall at 16 KB", faultserver.Spec{Behavior: faultserver.StallAfter, Direction: "tx", Bytes: 16_000}, 64 << 10, verdict.Stall1620},
		{"stall at 50 KB", faultserver.Spec{Behavior: faultserver.StallAfter, Direction: "tx", Bytes: 50_000}, 128 << 10, verdict.ReadTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := startFault(t, c.spec)
			o := labOpts(StageAll)
			o.Timeouts.Bulk = 800 * time.Millisecond
			o.Bulk.Size = c.size
			p := testProber(t, srv.Pool, nil)
			d, _ := NewDirectDialer(directOptionsForTests())
			r := p.Probe(context.Background(), labTarget(srv), d, o)
			if r.Verdict != c.want || (c.want != verdict.OK && r.Stage != verdict.StageBulk) {
				t.Fatalf("got %s at %s, want %s\n%+v", r.Verdict, r.Stage, c.want, r.Evidence)
			}
		})
	}
}

func TestFaultServerSNIBlock(t *testing.T) {
	srv := startFault(t, faultserver.Spec{Behavior: faultserver.SNIBlock, BlockSNI: "example.test", Hosts: []string{"example.test", "other.test"}})
	p := testProber(t, srv.Pool, nil)
	d, _ := NewDirectDialer(directOptionsForTests())
	o := labOpts(StageTCP | StageTLS | StageHTTP)
	o.SNISwap = true

	r := p.Probe(context.Background(), labTarget(srv), d, o)
	if r.Verdict != verdict.TLSRST || r.Blocking != verdict.BlockingConfirmed {
		t.Fatalf("blocked SNI: %+v", r)
	}
	if sw := r.Evidence.SNISwap; sw == nil || sw.Verdict != verdict.OK {
		t.Fatalf("swap: %+v", sw)
	}

	// Another name on the same server passes untouched.
	tg := labTarget(srv)
	tg.Domain = "other.test"
	if r := p.Probe(context.Background(), tg, d, o); r.Verdict != verdict.OK {
		t.Fatalf("unblocked SNI: %+v", r)
	}
}

// holdOnSecond takes the server's only connection slot before the second dial, as a busy client would.
type holdOnSecond struct {
	Dialer
	n    int
	hold net.Conn
}

func (h *holdOnSecond) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	h.n++
	if h.n == 2 {
		h.hold, _ = net.Dial(network, addr)
		time.Sleep(50 * time.Millisecond)
	}
	return h.Dialer.DialContext(ctx, network, addr)
}

func TestFaultServerConnLimited(t *testing.T) {
	srv := startFault(t, faultserver.Spec{Behavior: faultserver.ConnLimit, Limit: 1})
	base, _ := NewDirectDialer(directOptionsForTests())
	h := &holdOnSecond{Dialer: base}
	defer func() {
		if h.hold != nil {
			h.hold.Close()
		}
	}()
	o := labOpts(StageTCP | StageTLS)
	o.Repeats = 2
	r := testProber(t, srv.Pool, nil).Probe(context.Background(), labTarget(srv), h, o)
	if r.Verdict != verdict.ConnLimited || r.Blocking != verdict.BlockingUnknown {
		t.Fatalf("%+v", r)
	}
}

// Regression: a CDN edge answers a neutral SNI it does not host with a TLS alert. That proves the address is
// reachable, so a blocked real SNI is an SNI block (desync), not an IP block (tunnel).
func TestFaultServerSNIBlockNeutralAlert(t *testing.T) {
	srv := startFault(t, faultserver.Spec{Behavior: faultserver.SNIBlock, BlockSNI: "example.test", RejectUnknownSNI: true})
	p := testProber(t, srv.Pool, nil)
	d, _ := NewDirectDialer(directOptionsForTests())
	o := labOpts(StageTCP | StageTLS | StageHTTP)
	o.SNISwap = true

	r := p.Probe(context.Background(), labTarget(srv), d, o)
	if r.Verdict != verdict.TLSRST || r.Evidence.SNISwap == nil || r.Evidence.SNISwap.Verdict != verdict.TLSAlert {
		t.Fatalf("setup: %+v", r)
	}
	if r.Blocking != verdict.BlockingConfirmed {
		t.Errorf("blocking %s, want confirmed", r.Blocking)
	}
	applySuggestion(&r, Calibration{Internet: CheckOK})
	if r.Suggestion != route.Desync {
		t.Errorf("suggestion %q, want desync (note %q)", r.Suggestion, r.SuggestionNote)
	}
}
