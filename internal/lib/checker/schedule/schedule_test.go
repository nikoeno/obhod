package schedule

import (
	"context"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/route"
)

var t0 = time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)

const netA = "asn:1"

type harness struct {
	s      *Scheduler
	clk    *clock.Fake
	cancel context.CancelFunc
	exited chan struct{}
}

// start runs a scheduler on a fake clock and returns once its periodic timer is armed.
func start(t *testing.T, cfg Config) *harness {
	t.Helper()
	clk := clock.NewFake(t0)
	s := newWithRand(cfg, clk, rand.New(rand.NewSource(1)))
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{s: s, clk: clk, cancel: cancel, exited: make(chan struct{})}
	go func() { s.Run(ctx); close(h.exited) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.exited:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	h.waiters(t, 1)
	return h
}

func (h *harness) waiters(t *testing.T, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.clk.BlockUntil(ctx, n); err != nil {
		t.Fatalf("waiting for %d timers: %v", n, err)
	}
}

func (h *harness) recv(t *testing.T) Trigger {
	t.Helper()
	select {
	case tr := <-h.s.Triggers():
		return tr
	case <-time.After(5 * time.Second):
		t.Fatal("no trigger")
	}
	return Trigger{}
}

func (h *harness) none(t *testing.T) {
	t.Helper()
	select {
	case tr := <-h.s.Triggers():
		t.Fatalf("unexpected trigger %+v", tr)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestJitterBounds(t *testing.T) {
	s := newWithRand(Config{Interval: time.Hour, Jitter: 10 * time.Minute}, clock.NewFake(t0), rand.New(rand.NewSource(7)))
	lo, hi := time.Duration(1<<62), time.Duration(0)
	for i := 0; i < 5000; i++ {
		d := s.interval()
		if d < 50*time.Minute || d > 70*time.Minute {
			t.Fatalf("interval %v out of bounds", d)
		}
		lo, hi = min(lo, d), max(hi, d)
	}
	if lo > 51*time.Minute || hi < 69*time.Minute {
		t.Errorf("jitter does not cover the range: %v..%v", lo, hi)
	}
}

func TestConfigDefaultsAndClamp(t *testing.T) {
	c := Config{}.withDefaults()
	if c.Interval != time.Hour || c.Jitter != 10*time.Minute || c.MinGap != 5*time.Minute || c.FailThreshold != 3 || c.FailWindow != 5*time.Minute {
		t.Errorf("defaults: %+v", c)
	}
	c = Config{Interval: 10 * time.Minute, Jitter: 10 * time.Minute, MinGap: 5 * time.Minute}.withDefaults()
	if c.Interval-c.Jitter < c.MinGap {
		t.Errorf("Interval-Jitter < MinGap: %+v", c)
	}
	c = Config{Interval: time.Minute, MinGap: 5 * time.Minute}.withDefaults()
	if c.Interval < c.MinGap || c.Jitter != 0 {
		t.Errorf("Interval below MinGap: %+v", c)
	}
}

func TestStartupTrigger(t *testing.T) {
	h := start(t, Config{})
	tr := h.recv(t)
	if tr.Kind != Startup || tr.Domains != nil || !tr.At.Equal(t0) {
		t.Errorf("startup = %+v", tr)
	}
}

func TestMergeSemantics(t *testing.T) {
	s := newWithRand(Config{}, clock.NewFake(t0), rand.New(rand.NewSource(1)))
	s.NetworkChanged(netA)
	s.mu.Lock()
	s.pending = nil
	s.mu.Unlock()

	s.Expired(netA, []string{"b.com", "a.com"})
	s.Expired(netA, []string{"c.com", "a.com"})
	if p := s.pending; p.Kind != Expired || !reflect.DeepEqual(p.Domains, []string{"a.com", "b.com", "c.com"}) || p.Network != netA {
		t.Errorf("union: %+v", p)
	}
	s.Manual([]string{"z.com"})
	if p := s.pending; p.Kind != Manual || len(p.Domains) != 4 {
		t.Errorf("manual: %+v", p)
	}
	s.Expired(netA, nil) // nil absorbs everything
	if p := s.pending; p.Domains != nil || p.Kind != Manual {
		t.Errorf("nil must absorb: %+v", p)
	}
	s.Manual([]string{"q.com"}) // and stays absorbed
	if s.pending.Domains != nil {
		t.Error("full trigger narrowed by a later targeted one")
	}
	s.NetworkChanged("asn:2")
	if p := s.pending; p.Kind != NetworkChange || p.Network != "asn:2" {
		t.Errorf("network change: %+v", p)
	}
}

func TestMinGap(t *testing.T) {
	h := start(t, Config{})
	tr := h.recv(t) // startup, a full check at t0
	h.s.Done(tr)

	h.s.Expired("", nil) // a full check right after: must wait for MinGap
	h.waiters(t, 2)
	h.none(t)
	h.clk.Advance(4 * time.Minute)
	h.none(t)
	h.clk.Advance(time.Minute)
	if got := h.recv(t); got.Kind != Expired {
		t.Errorf("got %+v", got)
	}
}

func TestNetworkChangeBypassesMinGapAndTargetedRechecksToo(t *testing.T) {
	h := start(t, Config{})
	h.s.Done(h.recv(t))

	h.s.NetworkChanged(netA)
	tr := h.recv(t)
	if tr.Kind != NetworkChange || tr.Network != netA || tr.Domains != nil {
		t.Errorf("got %+v", tr)
	}
	h.s.Done(tr)

	h.s.Expired(netA, []string{"a.com"}) // targeted: not a full check
	if got := h.recv(t); !reflect.DeepEqual(got.Domains, []string{"a.com"}) {
		t.Errorf("got %+v", got)
	}
}

func TestNoTriggersWhileInFlight(t *testing.T) {
	h := start(t, Config{})
	first := h.recv(t)
	h.s.NetworkChanged(netA)
	h.s.Manual([]string{"a.com"})
	h.none(t)
	h.s.Done(first)
	tr := h.recv(t)
	if tr.Kind != NetworkChange || tr.Domains != nil || tr.Network != netA {
		t.Errorf("merged trigger = %+v", tr)
	}
	h.s.Done(tr)
	h.none(t)
}

func TestPeriodicTimer(t *testing.T) {
	h := start(t, Config{Interval: time.Hour, Jitter: 10 * time.Minute})
	h.s.Done(h.recv(t))
	if d := h.s.Next().Sub(t0); d < 50*time.Minute || d > 70*time.Minute {
		t.Errorf("Next = +%v", d)
	}
	h.clk.Advance(71 * time.Minute)
	if tr := h.recv(t); tr.Kind != Timer || tr.Domains != nil {
		t.Errorf("got %+v", tr)
	}
}

func TestFailureThresholdAndWindow(t *testing.T) {
	s := newWithRand(Config{FailThreshold: 3, FailWindow: 5 * time.Minute}, clock.NewFake(t0), rand.New(rand.NewSource(1)))
	pending := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.pending != nil
	}
	s.Failure(netA, "a.com", route.Direct, t0)
	s.Failure(netA, "a.com", route.Direct, t0.Add(time.Minute))
	if pending() {
		t.Fatal("triggered below the threshold")
	}
	s.Failure(netA, "a.com", route.Direct, t0.Add(7*time.Minute)) // first two fell out of the window
	if pending() {
		t.Fatal("triggered although the window expired")
	}
	s.Failure(netA, "a.com", route.Tunnel, t0.Add(7*time.Minute)) // other route counts separately
	s.Failure(netA, "b.com", route.Direct, t0.Add(7*time.Minute))
	if pending() {
		t.Fatal("counters leaked across routes/domains")
	}
	s.Failure(netA, "a.com", route.Direct, t0.Add(8*time.Minute))
	s.Failure(netA, "a.com", route.Direct, t0.Add(9*time.Minute))
	if !pending() {
		t.Fatal("no trigger at the threshold")
	}
	if p := s.pending; p.Kind != Failures || !reflect.DeepEqual(p.Domains, []string{"a.com"}) || p.Network != netA {
		t.Errorf("trigger = %+v", p)
	}

	// The counter reset after the trigger: two more failures are not enough.
	s.mu.Lock()
	s.pending = nil
	s.mu.Unlock()
	s.Failure(netA, "a.com", route.Direct, t0.Add(10*time.Minute))
	s.Failure(netA, "a.com", route.Direct, t0.Add(11*time.Minute))
	if pending() {
		t.Error("counter was not reset after a trigger")
	}
}

func TestStaleNetworkEventsIgnored(t *testing.T) {
	s := newWithRand(Config{FailThreshold: 1}, clock.NewFake(t0), rand.New(rand.NewSource(1)))
	s.NetworkChanged("asn:2")
	s.mu.Lock()
	s.pending = nil
	s.mu.Unlock()
	s.Expired(netA, []string{"a.com"})
	s.Failure(netA, "a.com", route.Direct, t0)
	if s.pending != nil {
		t.Errorf("event for an old network produced %+v", s.pending)
	}
}
