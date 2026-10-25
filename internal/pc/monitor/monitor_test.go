package monitor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/lib/clashapi"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func byTag(c clashapi.Connection) route.Route {
	if len(c.Chains) == 0 {
		return route.Direct
	}
	r, err := route.Parse(c.Chains[len(c.Chains)-1])
	if err != nil {
		return route.Direct
	}
	return r
}

type sinkLog struct {
	mu sync.Mutex
	fs []Failure
}

func (s *sinkLog) add(f Failure) { s.mu.Lock(); s.fs = append(s.fs, f); s.mu.Unlock() }
func (s *sinkLog) get() []Failure {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Failure(nil), s.fs...)
}

// scenario drives poll() with scripted snapshots on a fake clock.
type scenario struct {
	t    *testing.T
	fc   *clock.Fake
	mon  *Monitor
	sink *sinkLog
}

type scriptedAPI struct {
	mu   sync.Mutex
	snap clashapi.Snapshot
	err  error
}

func (a *scriptedAPI) Connections(context.Context) (clashapi.Snapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snap, a.err
}
func (a *scriptedAPI) Version(context.Context) (string, error) { return "", nil }
func (a *scriptedAPI) set(s clashapi.Snapshot, err error) {
	a.mu.Lock()
	a.snap, a.err = s, err
	a.mu.Unlock()
}

func newScenario(t *testing.T, cfg Config) (*scenario, *scriptedAPI) {
	t.Helper()
	fc := clock.NewFake(t0)
	api := &scriptedAPI{}
	sink := &sinkLog{}
	return &scenario{t: t, fc: fc, mon: New(cfg, api, byTag, fc, sink.add), sink: sink}, api
}

// step sets the snapshot, advances the clock by d and polls once.
func (s *scenario) step(api *scriptedAPI, d time.Duration, conns ...clashapi.Connection) {
	s.t.Helper()
	s.fc.Advance(d)
	api.set(clashapi.Snapshot{At: s.fc.Now(), Connections: conns}, nil)
	if err := s.mon.poll(context.Background()); err != nil {
		s.t.Fatal(err)
	}
}

func conn(id, host, network, tag string, up, down int64) clashapi.Connection {
	return clashapi.Connection{ID: id, Host: host, Network: network, DstPort: "443", Upload: up, Download: down, Chains: []string{tag}}
}

func (s *scenario) wantFailures(want ...analyzer.Verdict) {
	s.t.Helper()
	got := s.sink.get()
	if len(got) != len(want) {
		s.t.Fatalf("failures = %+v, want verdicts %v", got, want)
	}
	for i := range want {
		if got[i].Verdict != want[i] {
			s.t.Fatalf("failure %d = %+v, want %s", i, got[i], want[i])
		}
	}
}

func TestVerdictConstantsAreValid(t *testing.T) {
	for _, v := range []analyzer.Verdict{verdictTLSRST, verdictStall1620, verdictTCPTimeout} {
		if !v.Valid() {
			t.Errorf("%q is not a known analyzer verdict", v)
		}
	}
}

func TestTLSResetDetected(t *testing.T) {
	s, api := newScenario(t, Config{})
	s.step(api, 0, conn("a", "blocked.example", "tcp", "direct", 700, 0))
	s.step(api, 2*time.Second) // gone: closed with ~0 download after a small upload
	s.wantFailures(verdictTLSRST)
	f := s.sink.get()[0]
	if f.Domain != "blocked.example" || f.Route != route.Direct || !f.At.Equal(s.fc.Now()) {
		t.Fatalf("failure = %+v", f)
	}
	if got := s.mon.Stats().Failures[route.Direct]; got != 1 {
		t.Fatalf("failure stat = %d", got)
	}
}

func TestStallDetectedOnceWhileOpen(t *testing.T) {
	s, api := newScenario(t, Config{})
	s.step(api, 0, conn("a", "slow.example", "tcp", "desync", 900, 5000))
	s.step(api, 2*time.Second, conn("a", "slow.example", "tcp", "desync", 900, 18000))
	for range 4 { // 8 s frozen at 18 KB: not yet
		s.step(api, 2*time.Second, conn("a", "slow.example", "tcp", "desync", 900, 18000))
	}
	s.wantFailures()
	s.step(api, 2*time.Second, conn("a", "slow.example", "tcp", "desync", 900, 18000)) // 10 s
	s.wantFailures(verdictStall1620)
	s.step(api, 2*time.Second, conn("a", "slow.example", "tcp", "desync", 900, 18000))
	s.step(api, 2*time.Second) // closes later: still only one failure for this id
	s.wantFailures(verdictStall1620)
	if got := s.sink.get()[0]; got.Route != route.Desync {
		t.Fatalf("route = %s", got.Route)
	}
}

func TestTCPTimeoutDetected(t *testing.T) {
	s, api := newScenario(t, Config{})
	s.step(api, 0, conn("a", "dead.example", "tcp", "direct", 0, 0))
	s.step(api, 2*time.Second, conn("a", "dead.example", "tcp", "direct", 0, 0))
	s.step(api, 2*time.Second, conn("a", "dead.example", "tcp", "direct", 0, 0))
	s.step(api, 2*time.Second) // closed after 4 s: too early for a connect timeout
	s.wantFailures()

	s.step(api, 0, conn("b", "dead.example", "tcp", "direct", 0, 0))
	for range 3 {
		s.step(api, 2*time.Second, conn("b", "dead.example", "tcp", "direct", 0, 0))
	}
	s.step(api, 2*time.Second)
	s.wantFailures(verdictTCPTimeout)
}

func TestNoFalsePositives(t *testing.T) {
	t.Run("long normal download", func(t *testing.T) {
		s, api := newScenario(t, Config{})
		var down int64
		for range 60 { // two minutes of steady progress through the stall window and beyond
			down += 30000
			s.step(api, 2*time.Second, conn("a", "cdn.example", "tcp", "tunnel", 2000, down))
		}
		s.step(api, 2*time.Second)
		s.wantFailures()
	})
	t.Run("download that crosses the window quickly", func(t *testing.T) {
		s, api := newScenario(t, Config{})
		s.step(api, 0, conn("a", "cdn.example", "tcp", "direct", 900, 2000))
		s.step(api, 2*time.Second, conn("a", "cdn.example", "tcp", "direct", 900, 80000))
		for range 10 {
			s.step(api, 2*time.Second, conn("a", "cdn.example", "tcp", "direct", 900, 80000))
		}
		s.wantFailures()
	})
	t.Run("short normal request", func(t *testing.T) {
		s, api := newScenario(t, Config{})
		s.step(api, 0, conn("a", "api.example", "tcp", "direct", 600, 4200))
		s.step(api, 2*time.Second)
		s.wantFailures()
	})
	t.Run("udp flow", func(t *testing.T) {
		s, api := newScenario(t, Config{})
		s.step(api, 0, conn("a", "dns.example", "udp", "direct", 80, 0))
		s.step(api, 2*time.Second)
		s.step(api, 0, conn("b", "q.example", "udp", "direct", 0, 0))
		for range 10 {
			s.step(api, 4*time.Second, conn("b", "q.example", "udp", "direct", 0, 0))
		}
		s.step(api, 2*time.Second)
		s.wantFailures()
	})
	t.Run("long lived idle connection", func(t *testing.T) {
		s, api := newScenario(t, Config{})
		s.step(api, 0, conn("a", "push.example", "tcp", "direct", 0, 0))
		for range 40 {
			s.step(api, 4*time.Second, conn("a", "push.example", "tcp", "direct", 0, 0))
		}
		s.step(api, 2*time.Second)
		s.wantFailures()
	})
}

func TestAgeFromConnectionStart(t *testing.T) {
	s, api := newScenario(t, Config{})
	c := conn("a", "old.example", "tcp", "direct", 700, 0)
	c.Start = s.fc.Now().Add(-time.Minute) // already a minute old when first seen
	s.step(api, 0, c)
	s.step(api, 2*time.Second)
	s.wantFailures() // too old to be a reset handshake
}

func TestStatsAccounting(t *testing.T) {
	s, api := newScenario(t, Config{})
	s.step(api, 0,
		conn("a", "a.example", "tcp", "tunnel", 100, 1000),
		conn("b", "b.example", "tcp", "direct", 10, 20))
	s.step(api, 2*time.Second,
		conn("a", "a.example", "tcp", "tunnel", 150, 5000), // +50 +4000
		conn("b", "b.example", "tcp", "direct", 10, 20),    // unchanged
		conn("c", "c.example", "udp", "desync", 5, 5))      // new
	s.step(api, 2*time.Second,
		conn("a", "a.example", "tcp", "tunnel", 10, 10), // counters went down: no growth
		conn("c", "c.example", "udp", "desync", 5, 5))   // b closed

	st := s.mon.Stats()
	if st.Since != t0 {
		t.Fatalf("since = %v", st.Since)
	}
	wantBytes := map[route.Route]int64{route.Tunnel: 100 + 1000 + 50 + 4000, route.Direct: 30, route.Desync: 10}
	wantConns := map[route.Route]int64{route.Tunnel: 1, route.Direct: 1, route.Desync: 1}
	for r, w := range wantBytes {
		if st.Bytes[r] != w {
			t.Errorf("bytes[%s] = %d, want %d", r, st.Bytes[r], w)
		}
	}
	for r, w := range wantConns {
		if st.Conns[r] != w {
			t.Errorf("conns[%s] = %d, want %d", r, st.Conns[r], w)
		}
	}
	for _, r := range route.All() {
		if _, ok := st.Failures[r]; !ok {
			t.Errorf("failures lacks route %s", r)
		}
	}
	st.Bytes[route.Tunnel] = -1 // the copy must not alias internal state
	if s.mon.Stats().Bytes[route.Tunnel] == -1 {
		t.Fatal("Stats returned internal maps")
	}
}

func TestFailureNeedsHost(t *testing.T) {
	s, api := newScenario(t, Config{})
	s.step(api, 0, conn("a", "", "tcp", "direct", 700, 0))
	s.step(api, 2*time.Second)
	s.wantFailures()
	if s.mon.Stats().Failures[route.Direct] != 1 {
		t.Fatal("a nameless failure still counts in the stats")
	}
}

func TestHostSniffedLater(t *testing.T) {
	s, api := newScenario(t, Config{})
	s.step(api, 0, conn("a", "", "tcp", "direct", 700, 0))
	s.step(api, 1*time.Second, conn("a", "late.example", "tcp", "direct", 700, 0))
	s.step(api, 1*time.Second)
	s.wantFailures(verdictTLSRST)
	if d := s.sink.get()[0].Domain; d != "late.example" {
		t.Fatalf("domain = %q", d)
	}
}

func TestAPIErrorDoesNotCloseEverything(t *testing.T) {
	s, api := newScenario(t, Config{})
	s.step(api, 0, conn("a", "x.example", "tcp", "direct", 700, 0))
	api.set(clashapi.Snapshot{}, errors.New("down"))
	if err := s.mon.poll(context.Background()); err == nil {
		t.Fatal("poll must report the API error")
	}
	s.wantFailures() // the outage is not a mass close
	s.step(api, time.Second, conn("a", "x.example", "tcp", "direct", 700, 9000))
	s.wantFailures()
}

func TestRunRecoversAndStops(t *testing.T) {
	fc := clock.NewFake(t0)
	api := &scriptedAPI{}
	api.set(clashapi.Snapshot{}, errors.New("sing-box not up yet"))
	var polls int
	var mu sync.Mutex
	counting := clashapi.Client(countingAPI{api, func() { mu.Lock(); polls++; mu.Unlock() }})
	m := New(Config{}, counting, byTag, fc, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	bctx, bcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer bcancel()
	count := func() int { mu.Lock(); defer mu.Unlock(); return polls }
	waitSleep := func() {
		if err := fc.BlockUntil(bctx, 1); err != nil {
			t.Fatal(err)
		}
	}
	waitSleep()
	if count() != 1 {
		t.Fatalf("polls = %d", count())
	}
	// Backoff after an error: 4 s (twice the 2 s interval), not 2 s.
	fc.Advance(2 * time.Second)
	time.Sleep(50 * time.Millisecond)
	if count() != 1 {
		t.Fatal("retried before the backoff elapsed")
	}
	fc.Advance(2 * time.Second)
	waitFor(t, func() bool { return count() == 2 })
	waitSleep()
	// Recovery: back to the normal interval.
	api.set(clashapi.Snapshot{At: fc.Now(), Connections: []clashapi.Connection{conn("a", "x.example", "tcp", "direct", 1, 1)}}, nil)
	fc.Advance(8 * time.Second)
	waitFor(t, func() bool { return count() == 3 })
	waitSleep()
	fc.Advance(2 * time.Second)
	waitFor(t, func() bool { return count() == 4 })

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
	if got := m.Stats().Conns[route.Direct]; got != 1 {
		t.Fatalf("conns = %d", got)
	}
}

type countingAPI struct {
	*scriptedAPI
	hit func()
}

func (c countingAPI) Connections(ctx context.Context) (clashapi.Snapshot, error) {
	c.hit()
	return c.scriptedAPI.Connections(ctx)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestDetectPure(t *testing.T) {
	cfg := Config{}.withDefaults()
	if _, ok := detectOpen(obs{network: "tcp", download: 11 << 10, age: time.Minute, idle: time.Minute}, cfg); ok {
		t.Error("below the stall window")
	}
	if _, ok := detectOpen(obs{network: "tcp", download: 37 << 10, age: time.Minute, idle: time.Minute}, cfg); ok {
		t.Error("above the stall window")
	}
	if v, ok := detectOpen(obs{network: "tcp", download: 20 << 10, age: 11 * time.Second, idle: 10 * time.Second}, cfg); !ok || v != verdictStall1620 {
		t.Error("stall not detected")
	}
	if _, ok := detectClosed(obs{network: "tcp", upload: 5000, download: 0, age: time.Second}); ok {
		t.Error("an upload that large is not a ClientHello")
	}
	if _, ok := detectClosed(obs{network: "tcp", upload: 700, download: 5000, age: time.Second}); ok {
		t.Error("a connection that got an answer was not reset")
	}
}

// TestKnownLimitsD2 pins the false positives listed in docs/REVIEW-NOTES.md,
// "Input for decisions" D2. They stay until D2 is decided; a change of the
// heuristics must update these cases on purpose. The negative cases right
// next to each window are real guarantees.
func TestKnownLimitsD2(t *testing.T) {
	t.Run("request seen only mid-handshake is judged by its last sighting", func(t *testing.T) {
		s, api := newScenario(t, Config{})
		s.step(api, 0, conn("a", "ok.example", "tcp", "direct", 700, 0))
		s.step(api, 2*time.Second)    // finished normally between two polls
		s.wantFailures(verdictTLSRST) // D2: false positive
	})
	t.Run("keep-alive idle inside the stall window", func(t *testing.T) {
		s, api := newScenario(t, Config{})
		s.step(api, 0, conn("a", "page.example", "tcp", "direct", 900, 20<<10))
		for range 8 { // 16 s idle after a 20 KB page
			s.step(api, 2*time.Second, conn("a", "page.example", "tcp", "direct", 900, 20<<10))
		}
		s.wantFailures(verdictStall1620) // D2: false positive
	})
	t.Run("keep-alive idle below and above the stall window", func(t *testing.T) {
		s, api := newScenario(t, Config{})
		s.step(api, 0,
			conn("a", "small.example", "tcp", "direct", 900, 8<<10),
			conn("b", "big.example", "tcp", "direct", 900, 40<<10))
		for range 15 {
			s.step(api, 2*time.Second,
				conn("a", "small.example", "tcp", "direct", 900, 8<<10),
				conn("b", "big.example", "tcp", "direct", 900, 40<<10))
		}
		s.wantFailures()
	})
	t.Run("unused preconnect closed inside the timeout window", func(t *testing.T) {
		s, api := newScenario(t, Config{})
		s.step(api, 0, conn("a", "pre.example", "tcp", "direct", 0, 0))
		for range 5 {
			s.step(api, 2*time.Second, conn("a", "pre.example", "tcp", "direct", 0, 0))
		}
		s.step(api, 2*time.Second)        // closed after 10 s with no byte
		s.wantFailures(verdictTCPTimeout) // D2: false positive
	})
	t.Run("silent connection closed after the timeout window", func(t *testing.T) {
		s, api := newScenario(t, Config{})
		s.step(api, 0, conn("a", "idle.example", "tcp", "direct", 0, 0))
		for range 18 {
			s.step(api, 2*time.Second, conn("a", "idle.example", "tcp", "direct", 0, 0))
		}
		s.step(api, 2*time.Second) // closed after 36 s
		s.wantFailures()
	})
}

// TestBytesAreALowerBound pins the documented Stats.Bytes limit: the Clash
// API lists open connections only, so the bytes after the last sighting and a
// connection that lives between two polls are never seen.
func TestBytesAreALowerBound(t *testing.T) {
	s, api := newScenario(t, Config{})
	s.step(api, 0, conn("a", "a.example", "tcp", "tunnel", 100, 1000))
	s.step(api, 2*time.Second) // closed after moving more bytes; the API no longer lists it
	if got := s.mon.Stats().Bytes[route.Tunnel]; got != 1100 {
		t.Fatalf("bytes = %d, want the 1100 seen at the last sighting", got)
	}
}
