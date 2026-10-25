package dialer

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/route"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type followFake struct {
	ifc  string
	fail bool
	fx   *followFixture
}

func (d followFake) Route() route.Route { return route.Direct }
func (d followFake) DialContext(context.Context, string, string) (net.Conn, error) {
	d.fx.mu.Lock()
	defer d.fx.mu.Unlock()
	d.fx.hits = append(d.fx.hits, d.ifc)
	if d.fail {
		return nil, errors.New("network is unreachable")
	}
	c, s := net.Pipe()
	d.fx.peers = append(d.fx.peers, s)
	return c, nil
}

type followFixture struct {
	mu     sync.Mutex
	cur    netif.Interface
	built  []string
	hits   []string
	peers  []net.Conn
	failOn map[string]bool
	clk    *fakeClock
	f      *followDialer
}

func newFollowFixture() *followFixture {
	fx := &followFixture{cur: netif.Interface{Name: "eth0", Index: 2}, failOn: map[string]bool{}, clk: &fakeClock{t: time.Unix(1000, 0)}}
	fx.f = newFollowDialer(
		func(context.Context) (netif.Interface, error) { fx.mu.Lock(); defer fx.mu.Unlock(); return fx.cur, nil },
		func(i netif.Interface) (Dialer, error) {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			fx.built = append(fx.built, i.Name)
			return followFake{ifc: i.Name, fail: fx.failOn[i.Name], fx: fx}, nil
		}, fx.clk.Now)
	return fx
}

func (fx *followFixture) switchTo(name string, idx int) {
	fx.mu.Lock()
	fx.cur = netif.Interface{Name: name, Index: idx}
	fx.mu.Unlock()
}

func (fx *followFixture) dial() (net.Conn, error) {
	return fx.f.DialContext(context.Background(), "tcp", "192.0.2.1:443")
}

func (fx *followFixture) last() string {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return fx.hits[len(fx.hits)-1]
}

func TestFollowDialerCachesWithinTTL(t *testing.T) {
	fx := newFollowFixture()
	for i := 0; i < 3; i++ {
		if c, err := fx.dial(); err != nil {
			t.Fatal(err)
		} else {
			c.Close()
		}
	}
	if len(fx.built) != 1 {
		t.Fatalf("built %v", fx.built)
	}
}

func TestFollowDialerPicksUpSwitchAfterTTL(t *testing.T) {
	fx := newFollowFixture()
	c, _ := fx.dial()
	c.Close()
	fx.switchTo("wlan0", 3)
	fx.clk.Advance(UplinkTTL + time.Millisecond)
	c, err := fx.dial()
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if fx.last() != "wlan0" {
		t.Fatalf("dialed from %s", fx.last())
	}
}

// Switch inside the TTL: the stale dialer fails, the retry uses the new uplink.
func TestFollowDialerRetriesOnChangedUplink(t *testing.T) {
	fx := newFollowFixture()
	c, _ := fx.dial()
	c.Close()
	fx.failOn["eth0"] = true
	fx.f.d = followFake{ifc: "eth0", fail: true, fx: fx} // the route died under the cached dialer
	fx.switchTo("wlan0", 3)
	c, err := fx.dial()
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.Close()
	if fx.last() != "wlan0" {
		t.Fatalf("hits %v", fx.hits)
	}
}

func TestFollowDialerFailedDialHealsOnNextDial(t *testing.T) {
	fx := newFollowFixture()
	fx.failOn["eth0"] = true
	if _, err := fx.dial(); err == nil {
		t.Fatal("expected error")
	}
	if len(fx.hits) != 1 {
		t.Fatalf("no retry on the same uplink expected: %v", fx.hits)
	}
	fx.switchTo("wlan0", 3)
	c, err := fx.dial() // no clock advance: the failure dropped the cache
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if fx.last() != "wlan0" {
		t.Fatalf("hits %v", fx.hits)
	}
}

// A pooled connection of the old interface is closed when the binding changes.
func TestFollowDialerClosesStaleConns(t *testing.T) {
	fx := newFollowFixture()
	old, _ := fx.dial()
	defer old.Close()
	fx.switchTo("wlan0", 3)
	fx.clk.Advance(UplinkTTL + time.Millisecond)
	c, err := fx.dial()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fx.mu.Lock()
	peer := fx.peers[0]
	fx.mu.Unlock()
	peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("old connection still open: %v", err)
	}
	// the new connection is untouched
	fx.mu.Lock()
	peer2 := fx.peers[1]
	fx.mu.Unlock()
	peer2.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := peer2.Read(make([]byte, 1)); !errors.Is(err, context.DeadlineExceeded) && !isTimeout(err) {
		t.Fatalf("new connection closed: %v", err)
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func TestFollowDialerSameUplinkKeepsConns(t *testing.T) {
	fx := newFollowFixture()
	c, _ := fx.dial()
	defer c.Close()
	fx.clk.Advance(UplinkTTL + time.Millisecond)
	c2, _ := fx.dial()
	defer c2.Close()
	if len(fx.built) != 1 {
		t.Fatalf("built %v", fx.built)
	}
	fx.mu.Lock()
	peer := fx.peers[0]
	fx.mu.Unlock()
	peer.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := peer.Read(make([]byte, 1)); !isTimeout(err) {
		t.Fatalf("connection closed: %v", err)
	}
}

func TestFollowDialerUplinkError(t *testing.T) {
	fx := newFollowFixture()
	boom := errors.New("no uplink")
	fx.f.uplink = func(context.Context) (netif.Interface, error) { return netif.Interface{}, boom }
	if _, err := fx.dial(); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}
