package monitor

import (
	"context"
	"sync"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/lib/clashapi"
)

const (
	defaultPollInterval  = 2 * time.Second
	defaultStallMinBytes = 12 << 10
	defaultStallMaxBytes = 36 << 10
	defaultStallMinAge   = 10 * time.Second
	maxErrorBackoff      = 30 * time.Second
)

// Failure is a suspected blocking symptom of one live connection.
type Failure struct {
	Domain  string
	Route   route.Route
	Verdict analyzer.Verdict // TLS_RST, STALL_16_20, TCP_TIMEOUT… (best-effort)
	At      time.Time
}

// Stats are traffic and failure totals per route (never per domain).
type Stats struct {
	Since    time.Time
	Bytes    map[route.Route]int64 // up+down
	Conns    map[route.Route]int64
	Failures map[route.Route]int64
}

// RouteOf maps a Clash connection to a route by its outbound tag (last element of Chains);
// the tags direct, desync, tunnel and proxy match the route names.
type RouteOf func(c clashapi.Connection) route.Route

// Config tunes the heuristics. Zero fields take their defaults.
type Config struct {
	PollInterval  time.Duration `yaml:"poll_interval"`   // default 2s
	StallMinBytes int64         `yaml:"stall_min_bytes"` // default 12 KB
	StallMaxBytes int64         `yaml:"stall_max_bytes"` // default 36 KB
	StallMinAge   time.Duration `yaml:"stall_min_age"`   // default 10s
}

func (c Config) withDefaults() Config {
	if c.PollInterval <= 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.StallMinBytes <= 0 {
		c.StallMinBytes = defaultStallMinBytes
	}
	if c.StallMaxBytes <= 0 {
		c.StallMaxBytes = defaultStallMaxBytes
	}
	if c.StallMinAge <= 0 {
		c.StallMinAge = defaultStallMinAge
	}
	return c
}

// tracked is what the monitor remembers about a connection between polls.
type tracked struct {
	host     string
	route    route.Route
	network  string
	upload   int64
	download int64
	born     time.Time // in the monitor's clock
	lastSeen time.Time
	lastGrow time.Time // when the download last grew
	failed   bool      // a failure was already reported for this id
}

// Monitor watches live connections through the Clash API.
type Monitor struct {
	cfg     Config
	api     clashapi.Client
	routeOf RouteOf
	clk     clock.Clock
	sink    func(Failure)

	mu    sync.Mutex
	conns map[string]*tracked
	stats Stats
}

// New returns a Monitor. sink is called, outside any lock, for every detected failure; it may be nil.
func New(cfg Config, api clashapi.Client, routeOf RouteOf, c clock.Clock, sink func(Failure)) *Monitor {
	return &Monitor{
		cfg:     cfg.withDefaults(),
		api:     api,
		routeOf: routeOf,
		clk:     c,
		sink:    sink,
		conns:   make(map[string]*tracked),
		stats: Stats{
			Since:    c.Now(),
			Bytes:    zeroed(),
			Conns:    zeroed(),
			Failures: zeroed(),
		},
	}
}

func zeroed() map[route.Route]int64 {
	m := make(map[route.Route]int64)
	for _, r := range route.All() {
		m[r] = 0
	}
	return m
}

// Stats returns a copy of the totals.
func (m *Monitor) Stats() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := func(in map[route.Route]int64) map[route.Route]int64 {
		out := make(map[route.Route]int64, len(in))
		for k, v := range in {
			out[k] = v
		}
		return out
	}
	return Stats{Since: m.stats.Since, Bytes: cp(m.stats.Bytes), Conns: cp(m.stats.Conns), Failures: cp(m.stats.Failures)}
}

// Run polls until ctx is done. API errors are retried with a growing delay
// (up to 30 s); Run returns only the context's error.
func (m *Monitor) Run(ctx context.Context) error {
	delay := m.cfg.PollInterval
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.poll(ctx); err != nil {
			delay = min(delay*2, max(maxErrorBackoff, m.cfg.PollInterval))
		} else {
			delay = m.cfg.PollInterval
		}
		if err := m.clk.Sleep(ctx, delay); err != nil {
			return err
		}
	}
}

// poll takes one snapshot and updates the state. A failed poll changes nothing,
// so an API outage is not mistaken for every connection closing.
func (m *Monitor) poll(ctx context.Context) error {
	snap, err := m.api.Connections(ctx)
	if err != nil {
		return err
	}
	now := m.clk.Now()

	m.mu.Lock()
	var failures []Failure
	report := func(t *tracked, v analyzer.Verdict) {
		t.failed = true
		m.stats.Failures[t.route]++
		if t.host != "" {
			failures = append(failures, Failure{Domain: t.host, Route: t.route, Verdict: v, At: now})
		}
	}

	seen := make(map[string]struct{}, len(snap.Connections))
	for _, c := range snap.Connections {
		seen[c.ID] = struct{}{}
		t, ok := m.conns[c.ID]
		if !ok {
			t = &tracked{
				host:     c.Host,
				route:    m.routeOf(c),
				network:  c.Network,
				born:     birth(now, snap.At, c.Start),
				lastGrow: now,
			}
			m.conns[c.ID] = t
			m.stats.Conns[t.route]++
		}
		if c.Host != "" {
			t.host = c.Host // sniffing may complete after the first snapshot
		}
		m.stats.Bytes[t.route] += delta(c.Upload, t.upload) + delta(c.Download, t.download)
		if c.Download > t.download {
			t.lastGrow = now
		}
		t.upload, t.download, t.lastSeen = c.Upload, c.Download, now

		if !t.failed {
			if v, ok := detectOpen(t.obs(now), m.cfg); ok {
				report(t, v)
			}
		}
	}
	for id, t := range m.conns {
		if _, ok := seen[id]; ok {
			continue
		}
		delete(m.conns, id)
		if !t.failed {
			if v, ok := detectClosed(t.obs(t.lastSeen)); ok {
				report(t, v)
			}
		}
	}
	m.mu.Unlock()

	if m.sink != nil {
		for _, f := range failures {
			m.sink(f)
		}
	}
	return nil
}

func (t *tracked) obs(at time.Time) obs {
	return obs{
		network:  t.network,
		upload:   t.upload,
		download: t.download,
		age:      at.Sub(t.born),
		idle:     at.Sub(t.lastGrow),
	}
}

// birth converts the connection's Start (sing-box's clock) into the monitor's
// clock by its age in the snapshot, so a fake clock and a real API agree.
func birth(now, snapAt, start time.Time) time.Time {
	if start.IsZero() || snapAt.IsZero() || start.After(snapAt) {
		return now
	}
	return now.Add(-snapAt.Sub(start))
}

// delta is the growth of a counter; a counter that went down counts as no growth.
func delta(cur, prev int64) int64 {
	if cur < prev {
		return 0
	}
	return cur - prev
}
