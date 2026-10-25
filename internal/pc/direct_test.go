package pc

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/route"
)

type fakeDial struct {
	ifc  string
	fail bool
	hits *[]string
}

func (f fakeDial) Route() route.Route { return route.Direct }
func (f fakeDial) DialContext(context.Context, string, string) (net.Conn, error) {
	*f.hits = append(*f.hits, f.ifc)
	if f.fail {
		return nil, errors.New("network is unreachable")
	}
	c, _ := net.Pipe()
	return c, nil
}

type directFixture struct {
	mu     sync.Mutex
	cur    netif.Interface
	built  []string
	hits   []string
	failOn map[string]bool // interfaces whose dialer fails
	clk    *clock.Fake
	l      *lazyDirect
}

func newDirectFixture() *directFixture {
	f := &directFixture{cur: netif.Interface{Name: "eth0", Index: 2}, failOn: map[string]bool{}, clk: clock.NewFake(time.Unix(1000, 0))}
	f.l = newLazyDirect(
		func(context.Context) (netif.Interface, error) { f.mu.Lock(); defer f.mu.Unlock(); return f.cur, nil },
		func(i netif.Interface) (analyzer.Dialer, error) {
			f.built = append(f.built, i.Name)
			return fakeDial{ifc: i.Name, fail: f.failOn[i.Name], hits: &f.hits}, nil
		}, f.clk)
	return f
}

func (f *directFixture) switchTo(name string, idx int) {
	f.mu.Lock()
	f.cur = netif.Interface{Name: name, Index: idx}
	f.mu.Unlock()
}

func (f *directFixture) dial(t *testing.T) error {
	t.Helper()
	c, err := f.l.DialContext(context.Background(), "tcp", "192.0.2.1:443")
	if c != nil {
		c.Close()
	}
	return err
}

func TestLazyDirectCachesWithinTTL(t *testing.T) {
	f := newDirectFixture()
	for i := 0; i < 3; i++ {
		if err := f.dial(t); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.built) != 1 {
		t.Fatalf("built %v", f.built)
	}
}

// A switch is picked up by the next dial after the TTL, with no reset event.
func TestLazyDirectPicksUpSwitchAfterTTL(t *testing.T) {
	f := newDirectFixture()
	_ = f.dial(t)
	f.switchTo("wlan0", 3)
	f.clk.Advance(directUplinkTTL + time.Millisecond)
	if err := f.dial(t); err != nil {
		t.Fatal(err)
	}
	if got := f.hits[len(f.hits)-1]; got != "wlan0" {
		t.Fatalf("dialed from %s, built %v", got, f.built)
	}
}

// The stale-binding race: reset ran before the route changed, so the dialer
// was rebuilt for the old interface. The failed dial must heal it at once.
func TestLazyDirectFailedDialRebindsWithoutEvent(t *testing.T) {
	f := newDirectFixture()
	f.failOn["eth0"] = true
	_ = f.dial(t) // fails: nothing else to try
	f.switchTo("wlan0", 3)
	if err := f.dial(t); err != nil {
		t.Fatalf("dial after switch: %v", err)
	}
	if got := f.hits[len(f.hits)-1]; got != "wlan0" {
		t.Fatalf("dialed from %s", got)
	}
}

// Switch inside the TTL: the stale dialer fails, the retry uses the new uplink.
func TestLazyDirectRetriesOnChangedUplink(t *testing.T) {
	f := newDirectFixture()
	f.failOn["eth0"] = true
	f.switchTo("eth0", 2)
	_ = f.dial(t)
	f.switchTo("wlan0", 3)
	if err := f.dial(t); err != nil {
		t.Fatalf("dial: %v", err)
	}
	if want := []string{"eth0", "eth0", "wlan0"}; len(f.hits) != 3 || f.hits[2] != want[2] {
		t.Fatalf("hits %v", f.hits)
	}
}

func TestLazyDirectNoRetryOnSameUplink(t *testing.T) {
	f := newDirectFixture()
	f.failOn["eth0"] = true
	if err := f.dial(t); err == nil {
		t.Fatal("expected error")
	}
	if len(f.hits) != 1 {
		t.Fatalf("hits %v", f.hits)
	}
}

func TestLazyDirectResetRebinds(t *testing.T) {
	f := newDirectFixture()
	_ = f.dial(t)
	f.switchTo("wlan0", 3)
	f.l.reset()
	_ = f.dial(t)
	if f.hits[len(f.hits)-1] != "wlan0" {
		t.Fatalf("hits %v", f.hits)
	}
	// reset with the same uplink keeps the dialer
	f.l.reset()
	_ = f.dial(t)
	if len(f.built) != 2 {
		t.Fatalf("built %v", f.built)
	}
}

func TestLazyDirectUplinkError(t *testing.T) {
	f := newDirectFixture()
	boom := errors.New("no uplink")
	f.l.uplink = func(context.Context) (netif.Interface, error) { return netif.Interface{}, boom }
	if err := f.dial(t); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

func TestDoHResolverDialsThroughLazyDirect(t *testing.T) {
	f := newDirectFixture()
	f.failOn["eth0"] = true
	cfg := analyzer.DefaultConfig()
	r, err := analyzer.NewDoHResolver(cfg, f.l)
	if err != nil || r == nil {
		t.Fatalf("resolver %v, err %v", r, err)
	}
	if _, err := r.LookupTXT(context.Background(), "example.test"); err == nil {
		t.Fatal("lookup succeeded with a failing dialer")
	}
	if len(f.hits) == 0 || f.hits[0] != "eth0" {
		t.Fatalf("lazy direct dialer not used: hits %v", f.hits)
	}
}
