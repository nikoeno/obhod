package analyzer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
	"github.com/nikoejento/obhod/internal/lib/version"
)

// Batch is what a Runner probes.
type Batch struct {
	Targets []Target
	Dialers []Dialer // currently exactly one: direct
	Options Options  // zero → Config.Options
}

// Runner probes a batch politely: bounded concurrency, a per-IP limit and jitter, after a calibration.
type Runner interface {
	Run(ctx context.Context, b Batch) (Report, error)
}

type runner struct {
	p      *prober
	jitter func(lo, hi time.Duration) time.Duration
}

// NewRunner returns a Runner. It fails only on an invalid config.
func NewRunner(cfg Config, deps Deps) (Runner, error) {
	p, err := newProber(cfg, deps)
	if err != nil {
		return nil, err
	}
	return &runner{p: p, jitter: uniform}, nil
}

func uniform(lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	return lo + rand.N(hi-lo)
}

// Run implements Runner. Concurrency limits and jitter are enforced here, not by callers. It fails only on an
// invalid batch or a cancelled context (the partial report is returned with the error in that case).
func (r *runner) Run(ctx context.Context, b Batch) (Report, error) {
	if len(b.Dialers) != 1 {
		return Report{}, fmt.Errorf("analyzer: batch needs exactly one dialer, got %d", len(b.Dialers))
	}
	d := b.Dialers[0]
	targets := make([]Target, len(b.Targets))
	for i, t := range b.Targets {
		nt, err := t.Normalized()
		if err != nil {
			return Report{}, fmt.Errorf("analyzer: target %d: %w", i, err)
		}
		targets[i] = nt
	}
	o := r.p.fill(b.Options)
	if err := o.validate(); err != nil {
		return Report{}, err
	}
	p := r.p
	rep := Report{Schema: ReportSchema, Version: version.Version, StartedAt: p.clk.Now(), Results: []ProbeResult{}}

	rep.Calibration = p.calibrate(ctx, d, o)
	rep.Calibration.Warnings = append(rep.Calibration.Warnings, p.bypassWarnings(ctx)...)
	rep.Network = p.networkInfo(ctx, d, &rep)
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	if rep.Calibration.Internet == CheckFailed {
		rep.Warnings = append(rep.Warnings, "calibration failed: no control domain reachable; results and suggestions are not trustworthy")
	}

	results := make([]ProbeResult, len(targets))
	done := make([]bool, len(targets))
	gates := newIPGates(p.cfg.PerIPConcurrency)
	pp := *p
	pp.gate = gates.acquire

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(p.cfg.Concurrency, max(len(targets), 1)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if j := r.jitter(p.cfg.Jitter[0], p.cfg.Jitter[1]); j > 0 {
					if p.clk.Sleep(ctx, j) != nil {
						return
					}
				}
				res := pp.Probe(ctx, targets[i], d, o)
				if ctx.Err() != nil {
					return
				}
				applySuggestion(&res, rep.Calibration)
				results[i], done[i] = res, true
			}
		}()
	}
feed:
	for i := range targets {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	for i := range results {
		if done[i] {
			rep.Results = append(rep.Results, results[i])
		}
	}
	return rep, ctx.Err()
}

// ipGates limits concurrent probes per destination address.
type ipGates struct {
	mu    sync.Mutex
	limit int
	sems  map[netip.Addr]chan struct{}
}

func newIPGates(limit int) *ipGates {
	return &ipGates{limit: limit, sems: map[netip.Addr]chan struct{}{}}
}

// acquire waits for a slot on ip and returns its release func; on a cancelled ctx it returns nil.
func (g *ipGates) acquire(ctx context.Context, ip netip.Addr) func() {
	g.mu.Lock()
	sem := g.sems[ip]
	if sem == nil {
		sem = make(chan struct{}, g.limit)
		g.sems[ip] = sem
	}
	g.mu.Unlock()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }
	case <-ctx.Done():
		return nil
	}
}

// networkInfo finds the public IP (when an echo endpoint is configured) and its ASN. Failures become warnings.
func (p *prober) networkInfo(ctx context.Context, d Dialer, rep *Report) NetworkInfo {
	var n NetworkInfo
	if p.cfg.PublicIPEndpoint == "" {
		return n
	}
	ip, err := p.fetchPublicIP(ctx, d)
	if err != nil {
		rep.Warnings = append(rep.Warnings, "public IP lookup failed: "+err.Error())
		return n
	}
	n.PublicIP = ip
	if p.res == nil {
		return n
	}
	asn, err := LookupASN(ctx, p.res, ip)
	if err != nil {
		rep.Warnings = append(rep.Warnings, "ASN lookup failed: "+err.Error())
		return n
	}
	n.ASN = asn
	return n
}

// fetchPublicIP asks Config.PublicIPEndpoint (an https URL answering with the caller's address as plain text).
// The endpoint's name is resolved through DoH, never the system resolver.
func (p *prober) fetchPublicIP(ctx context.Context, d Dialer) (netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, p.cfg.Options.Timeouts.HTTP)
	defer cancel()
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if _, err := netip.ParseAddr(host); err != nil {
				if p.res == nil {
					return nil, verdict.Local(errors.New("no resolver for the public IP endpoint"))
				}
				ips, err := p.res.LookupIP(ctx, host)
				if err != nil || len(ips) == 0 {
					return nil, fmt.Errorf("resolve %s: %v", host, err)
				}
				host = pickIP(ips).String()
			}
			return d.DialContext(ctx, network, net.JoinHostPort(host, port))
		},
		TLSClientConfig:   &tls.Config{RootCAs: p.roots},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.PublicIPEndpoint, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return netip.Addr{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return netip.Addr{}, err
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("endpoint did not answer with an IP address: %w", err)
	}
	return ip.Unmap(), nil
}
