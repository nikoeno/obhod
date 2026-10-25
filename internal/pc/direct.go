package pc

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/route"
)

// directUplinkTTL is how long a resolved uplink is trusted. A switch of uplink
// is therefore picked up by the next dial after at most this long, with no
// event needed.
const directUplinkTTL = 2 * time.Second

// lazyDirect is the checker's direct dialer. The real dialer is pinned to one
// interface (G12: probes never enter the TUN), so the uplink is re-resolved
// when the cached binding is older than directUplinkTTL, after reset, and after
// any failed dial. The dialer is rebuilt only when the interface changed.
type lazyDirect struct {
	uplink func(ctx context.Context) (netif.Interface, error)
	build  func(netif.Interface) (analyzer.Dialer, error)
	clk    clock.Clock

	mu    sync.Mutex
	d     analyzer.Dialer
	ifc   netif.Interface
	at    time.Time
	gen   uint64 // bumped by reset: a resolve that started before it must not be cached
	valid bool
}

func newLazyDirect(uplink func(context.Context) (netif.Interface, error), build func(netif.Interface) (analyzer.Dialer, error), clk clock.Clock) *lazyDirect {
	return &lazyDirect{uplink: uplink, build: build, clk: clk}
}

// get returns a dialer bound to the current uplink and that uplink.
func (l *lazyDirect) get(ctx context.Context) (analyzer.Dialer, netif.Interface, error) {
	l.mu.Lock()
	if l.valid && l.clk.Since(l.at) < directUplinkTTL {
		d, ifc := l.d, l.ifc
		l.mu.Unlock()
		return d, ifc, nil
	}
	gen := l.gen
	old, oldIfc := l.d, l.ifc
	l.mu.Unlock()

	ifc, err := l.uplink(ctx)
	if err != nil {
		return nil, netif.Interface{}, err
	}
	d := old
	if d == nil || !sameBinding(oldIfc, ifc) {
		if d, err = l.build(ifc); err != nil {
			return nil, netif.Interface{}, err
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.gen == gen {
		l.d, l.ifc, l.at, l.valid = d, ifc, l.clk.Now(), true
	}
	return d, ifc, nil
}

// sameBinding reports whether a dialer built for a also binds to b.
func sameBinding(a, b netif.Interface) bool { return a.Name == b.Name && a.Index == b.Index }

// reset drops the cached uplink; the next dial resolves it again.
func (l *lazyDirect) reset() {
	l.mu.Lock()
	l.gen++
	l.valid = false
	l.mu.Unlock()
}

func (l *lazyDirect) Route() route.Route { return route.Direct }

// DialContext dials from the current uplink. A failed dial drops the cache; if
// the uplink turns out to be a different interface, the dial is tried once more.
func (l *lazyDirect) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d, ifc, err := l.get(ctx)
	if err != nil {
		return nil, err
	}
	c, err := d.DialContext(ctx, network, addr)
	if err == nil || ctx.Err() != nil {
		return c, err
	}
	l.reset()
	d2, ifc2, rerr := l.get(ctx)
	if rerr != nil || sameBinding(ifc, ifc2) {
		return nil, err
	}
	c, err2 := d2.DialContext(ctx, network, addr)
	if err2 != nil {
		return nil, errors.Join(err, err2)
	}
	return c, nil
}
