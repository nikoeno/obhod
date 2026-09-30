package netif

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"github.com/nikoejento/obhod/internal/lib/clock"
)

// EventKind classifies a network change.
type EventKind int

// Kinds of network change reported by Watch.
const (
	// UplinkChanged means the uplink interface or its gateway changed (including an uplink appearing).
	UplinkChanged EventKind = iota + 1
	// AddressChanged means the uplink kept its identity but its addresses changed.
	AddressChanged
	// LinkDown means the uplink lost its link or disappeared.
	LinkDown
)

// String returns the kind name.
func (k EventKind) String() string {
	switch k {
	case UplinkChanged:
		return "UplinkChanged"
	case AddressChanged:
		return "AddressChanged"
	case LinkDown:
		return "LinkDown"
	}
	return "EventKind(" + strconv.Itoa(int(k)) + ")"
}

// Event is one network change notification.
type Event struct {
	At   time.Time
	Kind EventKind // UplinkChanged, AddressChanged, LinkDown
}

const pollInterval = 2 * time.Second

// snapshot is the comparable state of the uplink at one instant.
type snapshot struct {
	Name    string
	Index   int
	Addrs   []string // sorted
	Gateway netip.Addr
	LinkUp  bool
}

// snapshotter captures the current uplink state.
type snapshotter interface {
	Snapshot(ctx context.Context) (snapshot, error)
}

// classify returns the event kind for a transition, or 0 when nothing relevant changed.
func classify(prev, cur snapshot) EventKind {
	switch {
	case prev.LinkUp && !cur.LinkUp:
		return LinkDown
	case !cur.LinkUp:
		return 0
	case prev.Name != cur.Name || prev.Index != cur.Index || prev.Gateway != cur.Gateway || !prev.LinkUp:
		return UplinkChanged
	case !slices.Equal(prev.Addrs, cur.Addrs):
		return AddressChanged
	}
	return 0
}

// osSnapshotter reads the real system state.
type osSnapshotter struct{}

func (osSnapshotter) Snapshot(ctx context.Context) (snapshot, error) {
	ifc, err := Uplink(ctx)
	if errors.Is(err, ErrNoUplink) {
		return snapshot{}, nil
	}
	if err != nil {
		return snapshot{}, err
	}
	s := snapshot{Name: ifc.Name, Index: ifc.Index, Gateway: ifc.Gateway}
	for _, p := range ifc.Addrs {
		s.Addrs = append(s.Addrs, p.String())
	}
	slices.Sort(s.Addrs)
	if ni, err := net.InterfaceByIndex(ifc.Index); err == nil {
		s.LinkUp = linkUp(*ni)
	}
	return s, nil
}

// Watch reports uplink changes by polling every 2 s. Events may be coalesced: several changes between two
// polls yield one event. The channel is closed when ctx ends.
func Watch(ctx context.Context) (<-chan Event, error) {
	return watch(ctx, clock.Real(), osSnapshotter{}, pollInterval)
}

func watch(ctx context.Context, clk clock.Clock, src snapshotter, every time.Duration) (<-chan Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prev, err := src.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	tick := clk.NewTicker(every)
	out := make(chan Event, 4)
	go func() {
		defer close(out)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C():
			}
			cur, err := src.Snapshot(ctx)
			if err != nil {
				continue // transient failure: keep the previous state
			}
			kind := classify(prev, cur)
			prev = cur
			if kind == 0 {
				continue
			}
			select {
			case out <- Event{At: clk.Now(), Kind: kind}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}
