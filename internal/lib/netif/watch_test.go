package netif

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/clock"
)

// fakeSnaps returns a scripted snapshot and signals every call.
type fakeSnaps struct {
	mu    sync.Mutex
	cur   snapshot
	err   error
	calls chan struct{}
}

func newFakeSnaps(s snapshot) *fakeSnaps { return &fakeSnaps{cur: s, calls: make(chan struct{}, 64)} }

func (f *fakeSnaps) set(s snapshot, err error) {
	f.mu.Lock()
	f.cur, f.err = s, err
	f.mu.Unlock()
}

func (f *fakeSnaps) Snapshot(context.Context) (snapshot, error) {
	f.mu.Lock()
	s, err := f.cur, f.err
	f.mu.Unlock()
	f.calls <- struct{}{}
	return s, err
}

func base() snapshot {
	return snapshot{Name: "eth0", Index: 2, Addrs: []string{"192.168.0.5/24"}, Gateway: netip.MustParseAddr("192.168.0.1"), LinkUp: true}
}

type harness struct {
	t      *testing.T
	clk    *clock.Fake
	src    *fakeSnaps
	out    <-chan Event
	cancel context.CancelFunc
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &harness{t: t, clk: clock.NewFake(time.Unix(1000, 0)), src: newFakeSnaps(base()), cancel: cancel}
	out, err := watch(ctx, h.clk, h.src, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	h.out = out
	<-h.src.calls // initial snapshot
	return h
}

// poll advances one interval and waits until the snapshot was taken.
func (h *harness) poll() {
	h.t.Helper()
	h.clk.Advance(2 * time.Second)
	select {
	case <-h.src.calls:
	case <-time.After(5 * time.Second):
		h.t.Fatal("poll did not happen")
	}
}

func (h *harness) expect(want EventKind) {
	h.t.Helper()
	select {
	case ev, ok := <-h.out:
		if !ok || ev.Kind != want {
			h.t.Fatalf("got %v (open=%v), want %v", ev.Kind, ok, want)
		}
		if ev.At.IsZero() {
			h.t.Error("zero event time")
		}
	case <-time.After(5 * time.Second):
		h.t.Fatalf("no %v event", want)
	}
}

func TestWatchNoChangeNoEvent(t *testing.T) {
	h := newHarness(t)
	h.poll()
	h.poll()
	// The next event must come from the real change, proving nothing was queued before.
	s := base()
	s.Addrs = []string{"192.168.0.6/24"}
	h.src.set(s, nil)
	h.poll()
	h.expect(AddressChanged)
}

func TestWatchKinds(t *testing.T) {
	sw := base()
	sw.Name, sw.Index = "wlan0", 3
	gw := base()
	gw.Gateway = netip.MustParseAddr("192.168.0.254")
	addr := base()
	addr.Addrs = []string{"192.168.0.5/24", "fe80::1/64"}
	for name, tc := range map[string]struct {
		next snapshot
		want EventKind
	}{
		"uplink switch": {sw, UplinkChanged},
		"gateway":       {gw, UplinkChanged},
		"address":       {addr, AddressChanged},
		"link down":     {snapshot{Name: "eth0", Index: 2, LinkUp: false}, LinkDown},
		"uplink gone":   {snapshot{}, LinkDown},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.src.set(tc.next, nil)
			h.poll()
			h.expect(tc.want)
		})
	}
}

func TestWatchRecovery(t *testing.T) {
	h := newHarness(t)
	h.src.set(snapshot{}, nil)
	h.poll()
	h.expect(LinkDown)
	h.poll() // still down: no repeat
	h.src.set(base(), nil)
	h.poll()
	h.expect(UplinkChanged)
}

func TestWatchTransientErrorIgnored(t *testing.T) {
	h := newHarness(t)
	h.src.set(snapshot{}, errors.New("boom"))
	h.poll()
	h.src.set(base(), nil)
	h.poll() // back to the same state: no event
	s := base()
	s.Index = 9
	h.src.set(s, nil)
	h.poll()
	h.expect(UplinkChanged)
}

func TestWatchCancelClosesChannel(t *testing.T) {
	h := newHarness(t)
	h.cancel()
	select {
	case _, ok := <-h.out:
		if ok {
			t.Fatal("unexpected event")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed")
	}
}

func TestWatchCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Watch(ctx); err == nil {
		t.Fatal("want error for cancelled context")
	}
}

func TestEventKindString(t *testing.T) {
	for k, want := range map[EventKind]string{UplinkChanged: "UplinkChanged", AddressChanged: "AddressChanged", LinkDown: "LinkDown", 0: "EventKind(0)"} {
		if k.String() != want {
			t.Errorf("%d: %q", int(k), k.String())
		}
	}
}

// Smoke test against the real OS: must not fail on a machine without an uplink.
func TestRealUplinkSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ifc, err := Uplink(ctx)
	if errors.Is(err, ErrNoUplink) {
		t.Skip("no uplink on this machine")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("uplink %s idx=%d gw=%v mac=%v ssid=%q", ifc.Name, ifc.Index, ifc.Gateway, ifc.GatewayMAC, ifc.SSID)
	if _, err := (osSnapshotter{}).Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
}
