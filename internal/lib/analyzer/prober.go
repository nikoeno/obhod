package analyzer

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	utls "github.com/refraction-networking/utls"

	"github.com/nikoejento/obhod/internal/lib/analyzer/classify"
	"github.com/nikoejento/obhod/internal/lib/analyzer/dialer"
	"github.com/nikoejento/obhod/internal/lib/analyzer/dnscheck"
	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/proc"
)

// Re-exported from children.
type (
	// Dialer opens probe connections for one route.
	Dialer = dialer.Dialer
	// Resolver looks up names.
	Resolver = dnscheck.Resolver
	// ASN describes the origin AS of an address.
	ASN = dnscheck.ASN
)

// NewSystemResolver returns the OS resolver, to be used as Deps.System for the poisoning comparison.
func NewSystemResolver() Resolver { return dnscheck.NewSystem() }

// LookupASN resolves the ASN of ip via DNS (Team Cymru) using r.
func LookupASN(ctx context.Context, r Resolver, ip netip.Addr) (ASN, error) {
	return dnscheck.LookupASN(ctx, r, ip)
}

// DirectOptions configure the direct dialer.
type DirectOptions = dialer.DirectOptions

// DialerStep is one scripted dial of a fake dialer.
type DialerStep = dialer.Step

// NewDirectDialer returns a dialer that connects straight from the physical uplink.
func NewDirectDialer(opts DirectOptions) (Dialer, error) { return dialer.NewDirect(opts) }

// NewFakeDialer returns a scripted dialer for tests (route direct).
func NewFakeDialer(script ...DialerStep) Dialer { return dialer.NewFake("direct", script...) }

// Deps are the injected collaborators.
type Deps struct {
	Clock    clock.Clock // nil → clock.Real()
	Resolver Resolver    // DoH; nil → built from Config.DoH (none configured → targets without an IP fail with LOCAL_ERROR)
	System   Resolver    // OS resolver for the poisoning comparison; nil → no comparison
	Procs    proc.Lister // process list for the foreign bypass-tool warning; nil → no scan
}

// SystemProcs returns the OS process lister, to be used as Deps.Procs.
func SystemProcs() proc.Lister { return proc.System() }

// Prober probes one target through one dialer.
type Prober interface {
	Probe(ctx context.Context, t Target, d Dialer, o Options) ProbeResult
}

type prober struct {
	cfg   Config
	clk   clock.Clock
	res   Resolver
	sys   Resolver
	procs proc.Lister
	roots *x509.CertPool  // nil → system roots; set by tests
	stubs *dnscheck.Stubs // known ISP stubs, learned while probing

	// gate, set by the Runner, limits concurrent probes per address. It returns nil when ctx ends first.
	gate func(ctx context.Context, ip netip.Addr) (release func())
}

// NewProber returns a Prober. It fails only on an invalid config.
func NewProber(cfg Config, deps Deps) (Prober, error) { return newProber(cfg, deps) }

// NewDoHResolver builds the DoH resolver for cfg.DoH (with cfg.Options.Fingerprint) that reaches the
// endpoints through d.DialContext. A caller outside the analyzer (e.g. the checker's network
// identification) passes its direct dialer here, so the lookups bypass a TUN client.
func NewDoHResolver(cfg Config, d Dialer) (Resolver, error) {
	if len(cfg.DoH) == 0 {
		return nil, errors.New("analyzer: no DoH endpoints in config")
	}
	if d == nil {
		return nil, errors.New("analyzer: nil dialer")
	}
	return dnscheck.NewDoH(cfg.DoH, d.DialContext, cfg.Options.Fingerprint)
}

func newProber(cfg Config, deps Deps) (*prober, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	p := &prober{cfg: cfg, clk: deps.Clock, res: deps.Resolver, sys: deps.System, procs: deps.Procs, stubs: dnscheck.NewStubs()}
	if p.clk == nil {
		p.clk = clock.Real()
	}
	if p.res == nil && len(cfg.DoH) > 0 {
		r, err := dnscheck.NewDoH(cfg.DoH, p.dohDial(), cfg.Options.Fingerprint)
		if err != nil {
			return nil, err
		}
		p.res = r
	}
	return p, nil
}

// dohDial dials DoH endpoints straight from the uplink (bypassing a TUN client). The dialer resolves the uplink
// on first use and then follows it (dialer.NewFollowing), so constructing a prober never needs an uplink and a
// switch of uplink is picked up without any event.
func (p *prober) dohDial() dnscheck.DialFunc {
	return dialer.NewFollowing(p.clk.Now).DialContext
}

// Classify maps a connection failure seen elsewhere (e.g. the live monitor) to a verdict.
func Classify(stage Stage, err error, rxBytes, txBytes int64) Verdict {
	return classify.Classify(classify.Input{Stage: stage, Err: err, RxBytes: rxBytes, TxBytes: txBytes})
}

// fill applies the config defaults to o. A zero Options means "use the config's".
func (p *prober) fill(o Options) Options {
	d := p.cfg.Options
	if reflect.DeepEqual(o, Options{}) {
		return d
	}
	if o.Stages == 0 {
		o.Stages = d.Stages
	}
	if o.Fingerprint == "" {
		o.Fingerprint = d.Fingerprint
	}
	if o.Repeats == 0 {
		o.Repeats = d.Repeats
	}
	t, dt := &o.Timeouts, d.Timeouts
	for _, f := range []struct {
		v   *time.Duration
		def time.Duration
	}{{&t.Resolve, dt.Resolve}, {&t.TCP, dt.TCP}, {&t.TLS, dt.TLS}, {&t.HTTP, dt.HTTP}, {&t.Bulk, dt.Bulk}} {
		if *f.v == 0 {
			*f.v = f.def
		}
	}
	if o.Bulk.Mode == "" {
		o.Bulk.Mode = d.Bulk.Mode
	}
	if o.Bulk.Size == 0 {
		o.Bulk.Size = d.Bulk.Size
	}
	if o.Bulk.StallMin == 0 {
		o.Bulk.StallMin = d.Bulk.StallMin
	}
	if o.Bulk.StallMax == 0 {
		o.Bulk.StallMax = d.Bulk.StallMax
	}
	return o
}

type attemptResult struct {
	v     Verdict
	stage Stage
	ev    Evidence
}

func localResult(res ProbeResult, err error) ProbeResult {
	res.Verdict = verdict.LocalError
	res.Blocking = verdict.LocalError.DefaultBlocking()
	res.Evidence.Err = errChain(err)
	return res
}

// Probe implements Prober. It never returns an error: failures are verdicts.
func (p *prober) Probe(ctx context.Context, t Target, d Dialer, o Options) ProbeResult {
	start := p.clk.Now()
	res := ProbeResult{Target: t, Route: d.Route(), StartedAt: start, Stage: verdict.StageResolve}
	finish := func(r ProbeResult) ProbeResult { r.Duration = p.clk.Since(start); return r }

	tg, err := t.Normalized()
	if err != nil {
		return finish(localResult(res, verdict.Local(err)))
	}
	res.Target = tg
	o = p.fill(o)
	if err := o.validate(); err != nil {
		return finish(localResult(res, verdict.Local(err)))
	}

	ip := tg.IP
	times := map[Stage]time.Duration{}
	if !ip.IsValid() {
		if p.res == nil {
			return finish(localResult(res, verdict.Local(errors.New("no resolver configured and target has no IP"))))
		}
		rctx, cancel := context.WithTimeout(ctx, o.Timeouts.Resolve)
		t0 := p.clk.Now()
		ips, err := p.res.LookupIP(rctx, tg.Domain)
		cancel()
		times[verdict.StageResolve] = p.clk.Since(t0)
		if err == nil && len(ips) == 0 {
			err = errors.New("no addresses")
		}
		if err == nil {
			ip = pickIP(ips)
			if chk, poisoned := p.checkDNS(ctx, tg.Domain, ips, d); poisoned {
				res.IP = ip
				res.Verdict, res.Blocking = verdict.DNSPoisoned, verdict.DNSPoisoned.DefaultBlocking()
				res.Evidence = Evidence{Err: chk.Reason + fmt.Sprintf(" (system %v, DoH %v)", chk.System, chk.DoH), StageTimes: times}
				return finish(res)
			}
		} else {
			if ctx.Err() != nil {
				err = verdict.Local(ctx.Err())
			}
			res.Verdict = classify.Classify(classify.Input{Stage: verdict.StageResolve, Err: err})
			res.Blocking = res.Verdict.DefaultBlocking()
			res.Evidence = Evidence{Err: errChain(err), StageTimes: times}
			return finish(res)
		}
	}
	res.IP = ip
	if p.gate != nil {
		release := p.gate(ctx, ip)
		if release == nil {
			return finish(localResult(res, verdict.Local(ctx.Err())))
		}
		defer release()
	}

	var attempts []attemptResult
	var vs []Verdict
	for i := 0; i < o.Repeats; i++ {
		a := p.attempt(ctx, tg, ip, d, o, tg.Domain)
		attempts = append(attempts, a)
		vs = append(vs, a.v)
		if ctx.Err() != nil {
			break
		}
	}
	win := classify.Majority(vs)
	chosen := attempts[0]
	if limited := connLimited(attempts); limited != nil {
		win, chosen = verdict.ConnLimited, *limited
	} else {
		for _, a := range attempts {
			if a.v == win {
				chosen = a
				break
			}
		}
	}
	if o.SNISwap && o.Stages.Has(StageTLS) && swappable(win) && len(p.cfg.NeutralSNI) > 0 && ctx.Err() == nil {
		chosen.ev.SNISwap = p.sniSwap(ctx, tg, ip, d, o)
	}
	res.Verdict, res.Stage, res.Evidence = win, chosen.stage, chosen.ev
	if res.Evidence.StageTimes == nil {
		res.Evidence.StageTimes = map[Stage]time.Duration{}
	}
	for s, d := range times {
		res.Evidence.StageTimes[s] = d
	}
	res.Blocking = classify.Confirm(win, res.Evidence.SNISwap)
	return finish(res)
}

// checkDNS compares the system answer with the DoH answer. When they differ without an obvious reason, it asks the
// system's answer on plain HTTP once: an ISP block page there is learned as a stub. It reports whether DNS is poisoned.
func (p *prober) checkDNS(ctx context.Context, host string, dohIPs []netip.Addr, d Dialer) (dnscheck.Check, bool) {
	if p.sys == nil {
		return dnscheck.Check{}, false
	}
	doh := dnscheck.NewFake(map[string][]netip.Addr{host: dohIPs}, nil)
	chk := dnscheck.Compare(ctx, host, p.sys, doh, p.stubs)
	if chk.Verdict == verdict.DNSPoisoned {
		return chk, true
	}
	if !chk.FakeIP && len(chk.System) > 0 && chk.Reason != "" {
		if learned, _ := dnscheck.Learn(ctx, d.DialContext, host, chk.System[0], p.stubs); learned {
			chk.Verdict = verdict.DNSPoisoned
			chk.Reason = fmt.Sprintf("system answer %s serves an ISP block page (learned as stub)", chk.System[0])
			return chk, true
		}
	}
	return chk, false
}

// swappable reports whether v is a TLS-level failure that an SNI swap can tell apart from an IP block.
func swappable(v Verdict) bool {
	switch v {
	case verdict.TLSRST, verdict.TLSTimeout, verdict.TLSEOF, verdict.TLSAlert, verdict.TLSSpoof:
		return true
	}
	return false
}

// connLimited spots a server that accepts some connections and turns others away: an attempt that got through
// and another that was reset or refused. SNI blocking is deterministic, so this is not it. It returns the
// first failing attempt, or nil.
func connLimited(attempts []attemptResult) *attemptResult {
	var good, bad *attemptResult
	for i := range attempts {
		a := &attempts[i]
		switch {
		case a.v == verdict.OK || a.v == verdict.OKRedirect:
			good = a
		case a.v == verdict.TLSRST, a.v == verdict.TLSEOF, a.v == verdict.TLSTimeout, a.v == verdict.TCPRefused:
			if bad == nil {
				bad = a
			}
		}
	}
	if good != nil && bad != nil {
		return bad
	}
	return nil
}

// sniSwap repeats the handshake to the same IP with a neutral SNI. If that handshake completes (even with a
// certificate for another name), the IP is reachable and only the SNI is being blocked.
func (p *prober) sniSwap(ctx context.Context, t Target, ip netip.Addr, d Dialer, o Options) *verdict.SNISwapResult {
	sni := p.cfg.NeutralSNI[0]
	so := o
	so.Stages = StageTCP | StageTLS
	a := p.attempt(ctx, t, ip, d, so, sni)
	v := a.v
	if v == verdict.TLSMITM && a.stage == verdict.StageVerify {
		v = verdict.OK // handshake done; the certificate simply is not for the neutral name
	}
	return &verdict.SNISwapResult{SNI: sni, Verdict: v}
}

func pickIP(ips []netip.Addr) netip.Addr {
	for _, ip := range ips {
		if ip.Is4() {
			return ip
		}
	}
	return ips[0]
}

// countConn counts the bytes that cross the wire.
type countConn struct {
	net.Conn
	rx, tx atomic.Int64
}

func (c *countConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.rx.Add(int64(n))
	return n, err
}

func (c *countConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.tx.Add(int64(n))
	return n, err
}

func clientHelloID(fp string) utls.ClientHelloID {
	switch fp {
	case "firefox":
		return utls.HelloFirefox_Auto
	case "safari":
		return utls.HelloSafari_Auto
	case "go":
		return utls.HelloGolang
	}
	return utls.HelloChrome_Auto
}

// newUConn builds a uTLS client connection that offers only http/1.1 in ALPN, since the probe speaks HTTP/1.1.
func newUConn(conn net.Conn, fp, sni string) (*utls.UConn, error) {
	uc := utls.UClient(conn, &utls.Config{ServerName: sni, InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}}, clientHelloID(fp))
	if err := uc.BuildHandshakeState(); err != nil {
		return nil, verdict.Local(err)
	}
	for _, e := range uc.Extensions {
		if a, ok := e.(*utls.ALPNExtension); ok {
			a.AlpnProtocols = []string{"http/1.1"}
		}
	}
	return uc, nil
}

// attempt runs one fresh connection through tcp → tls → verify → http (→ bulk).
func (p *prober) attempt(ctx context.Context, t Target, ip netip.Addr, d Dialer, o Options, sni string) (a attemptResult) {
	a.ev.StageTimes = map[Stage]time.Duration{}
	fail := func(stage Stage, err error, in classify.Input) attemptResult {
		if err != nil && ctx.Err() != nil {
			err = verdict.Local(ctx.Err())
		}
		in.Stage, in.Err = stage, err
		a.stage = stage
		a.v = classify.Classify(in)
		a.ev.Err = errChain(err)
		return a
	}
	stageTimer := func(s Stage) func() {
		t0 := p.clk.Now()
		return func() { a.ev.StageTimes[s] = p.clk.Since(t0) }
	}

	// tcp
	done := stageTimer(verdict.StageTCP)
	dctx, cancel := context.WithTimeout(ctx, o.Timeouts.TCP)
	raw, err := d.DialContext(dctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(int(t.Port))))
	cancel()
	done()
	if err != nil {
		return fail(verdict.StageTCP, err, classify.Input{})
	}
	rtt := a.ev.StageTimes[verdict.StageTCP]
	a.ev.RTT = rtt
	conn := &countConn{Conn: raw}
	defer conn.Close()
	defer context.AfterFunc(ctx, func() { conn.Close() })()
	counts := func(in classify.Input) classify.Input {
		in.RxBytes, in.TxBytes = conn.rx.Load(), conn.tx.Load()
		a.ev.RxBytes, a.ev.TxBytes = in.RxBytes, in.TxBytes
		return in
	}
	a.stage, a.v = verdict.StageTCP, verdict.OK
	if !o.Stages.Has(StageTLS) {
		return a
	}

	// tls
	uc, err := newUConn(conn, o.Fingerprint, sni)
	if err != nil {
		return fail(verdict.StageTLS, err, counts(classify.Input{}))
	}
	done = stageTimer(verdict.StageTLS)
	conn.SetDeadline(time.Now().Add(o.Timeouts.TLS))
	err = uc.Handshake()
	done()
	if err != nil {
		return fail(verdict.StageTLS, err, counts(classify.Input{Timeout: o.Timeouts.TLS, Elapsed: a.ev.StageTimes[verdict.StageTLS]}))
	}
	conn.SetDeadline(time.Time{})
	state := uc.ConnectionState()
	a.ev.TLSVersion = tls.VersionName(state.Version)
	a.stage, a.v = verdict.StageTLS, verdict.OK

	// verify
	if len(state.PeerCertificates) > 0 {
		a.ev.CertSubject = state.PeerCertificates[0].Subject.String()
	}
	if certErr := p.verify(state.PeerCertificates, sni); certErr != nil {
		a.ev.CertError = certErr.Error()
		return fail(verdict.StageVerify, nil, counts(classify.Input{CertErr: certErr}))
	}
	a.stage = verdict.StageVerify
	if !o.Stages.Has(StageHTTP) {
		return a
	}

	// http
	done = stageTimer(verdict.StageHTTP)
	br := bufio.NewReader(uc)
	conn.SetDeadline(time.Now().Add(o.Timeouts.HTTP))
	status, location, err := headRequest(uc, br, t, sni, o.Stages.Has(StageBulk))
	done()
	in := counts(classify.Input{HTTPStatus: status, Location: location, StubHosts: p.stubs.Hosts(), Timeout: o.Timeouts.HTTP})
	a.ev.HTTPStatus, a.ev.Location = status, location
	if err != nil || status >= 300 && status < 400 {
		return fail(verdict.StageHTTP, err, in)
	}
	conn.SetDeadline(time.Time{})
	a.stage, a.v = verdict.StageHTTP, verdict.OK
	if !o.Stages.Has(StageBulk) {
		return a
	}
	return p.bulk(ctx, a, t, uc, br, conn, o, rtt)
}

// verify checks the peer chain after the handshake (which ran without verification, so a spoofed
// certificate is observed instead of aborting the handshake).
func (p *prober) verify(chain []*x509.Certificate, host string) error {
	if len(chain) == 0 {
		return errors.New("server sent no certificate")
	}
	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		inter.AddCert(c)
	}
	_, err := chain[0].Verify(x509.VerifyOptions{DNSName: host, Roots: p.roots, Intermediates: inter, CurrentTime: p.clk.Now()})
	return err
}

// headRequest sends HEAD on w and reads the status line and headers from br.
func headRequest(w net.Conn, br *bufio.Reader, t Target, host string, keepAlive bool) (status int, location string, err error) {
	conn := "close"
	if keepAlive {
		conn = "keep-alive"
	}
	req := fmt.Sprintf("HEAD %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: %s\r\nAccept: */*\r\nConnection: %s\r\n\r\n", t.Path, hostHeader(host, t.Port), userAgent, conn)
	if _, err = w.Write([]byte(req)); err != nil {
		return 0, "", err
	}
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodHead})
	if err != nil {
		return 0, "", err
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location"), nil
}

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

func hostHeader(host string, port uint16) string {
	if port == 443 {
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port)))
}

// errChain renders an error and the type at the bottom of its chain, never dropping information.
func errChain(err error) string {
	if err == nil {
		return ""
	}
	root := err
	for {
		next := errors.Unwrap(root)
		if next == nil {
			break
		}
		root = next
	}
	s := err.Error()
	if root != err && !strings.Contains(s, root.Error()) {
		s += ": " + root.Error()
	}
	return fmt.Sprintf("%s [%T]", s, root)
}
