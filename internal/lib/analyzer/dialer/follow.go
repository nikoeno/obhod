package dialer

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/route"
)

// UplinkTTL is how long a resolved uplink is trusted by the DoH dialer. A switch of uplink is picked up by
// the next dial after at most this long.
const UplinkTTL = 2 * time.Second

// followDialer is a direct dialer that follows the uplink. Sockets stay pinned to the physical interface (probes
// never enter a TUN), but the interface is re-resolved when the cached one is older than UplinkTTL and after
// any failed dial. When the binding changes, connections opened through the old one are closed, so a pooled
// DoH connection on a dead interface is never reused.
type followDialer struct {
	uplink func(ctx context.Context) (netif.Interface, error)
	build  func(netif.Interface) (Dialer, error)
	now    func() time.Time

	mu    sync.Mutex
	d     Dialer
	ifc   netif.Interface
	at    time.Time
	valid bool
	conns map[*followConn]struct{} // open connections made through the current binding
}

func newFollowDialer(uplink func(context.Context) (netif.Interface, error), build func(netif.Interface) (Dialer, error), now func() time.Time) *followDialer {
	return &followDialer{uplink: uplink, build: build, now: now, conns: map[*followConn]struct{}{}}
}

// NewFollowing returns a direct dialer pinned to the physical uplink that follows uplink changes: the uplink is
// re-resolved (netif.Uplink) when the cached one is older than UplinkTTL (measured by now) and after any failed
// dial; sockets stay bound via netif.BindControl, so probes never enter a TUN. When the binding changes,
// connections opened through the old one are closed. Nothing is resolved until the first dial.
func NewFollowing(now func() time.Time) Dialer {
	return newFollowDialer(netif.Uplink, func(ifc netif.Interface) (Dialer, error) {
		return NewDirect(DirectOptions{BindIface: true, Uplink: ifc})
	}, now)
}

type followConn struct {
	net.Conn
	f *followDialer
}

func (c *followConn) Close() error {
	c.f.mu.Lock()
	delete(c.f.conns, c)
	c.f.mu.Unlock()
	return c.Conn.Close()
}

func sameIface(a, b netif.Interface) bool { return a.Name == b.Name && a.Index == b.Index }

// get returns a dialer bound to the current uplink and that uplink.
func (f *followDialer) get(ctx context.Context) (Dialer, netif.Interface, error) {
	f.mu.Lock()
	if f.valid && f.now().Sub(f.at) < UplinkTTL {
		d, ifc := f.d, f.ifc
		f.mu.Unlock()
		return d, ifc, nil
	}
	f.mu.Unlock()

	ifc, err := f.uplink(ctx)
	if err != nil {
		return nil, netif.Interface{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.d == nil || !sameIface(f.ifc, ifc) {
		d, err := f.build(ifc)
		if err != nil {
			return nil, netif.Interface{}, err
		}
		stale := f.conns
		f.conns = map[*followConn]struct{}{}
		f.d, f.ifc = d, ifc
		for c := range stale { // idle pooled connections of the old interface must not be reused
			go c.Conn.Close()
		}
	}
	f.at, f.valid = f.now(), true
	return f.d, f.ifc, nil
}

func (f *followDialer) invalidate() {
	f.mu.Lock()
	f.valid = false
	f.mu.Unlock()
}

func (f *followDialer) dialOnce(ctx context.Context, d Dialer, network, addr string) (net.Conn, error) {
	c, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	fc := &followConn{Conn: c, f: f}
	f.mu.Lock()
	f.conns[fc] = struct{}{}
	f.mu.Unlock()
	return fc, nil
}

// DialContext dials from the current uplink. A failed dial drops the cache; if the uplink turns out to be a
// different interface, the dial is tried once more.
func (f *followDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d, ifc, err := f.get(ctx)
	if err != nil {
		return nil, err
	}
	c, err := f.dialOnce(ctx, d, network, addr)
	if err == nil || ctx.Err() != nil {
		return c, err
	}
	f.invalidate()
	d2, ifc2, rerr := f.get(ctx)
	if rerr != nil || sameIface(ifc, ifc2) {
		return nil, err
	}
	c, err2 := f.dialOnce(ctx, d2, network, addr)
	if err2 != nil {
		return nil, errors.Join(err, err2)
	}
	return c, nil
}

func (f *followDialer) Route() route.Route { return route.Direct }
