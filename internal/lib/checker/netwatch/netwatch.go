// Package netwatch identifies the current network (networkId) and emits change events.
package netwatch

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/checker/lib/model"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/netif"
)

// Network is what the watcher knows about the network it is on.
type Network struct {
	ID       model.NetworkID
	ASN      uint32
	ASName   string
	PublicIP netip.Addr // kept in memory only, never persisted or logged
	Uplink   string     // interface name
	Known    bool
}

// Config tunes the watcher. Zero durations take their defaults.
type Config struct {
	Debounce       time.Duration // default 3s
	Refresh        time.Duration // default 30m
	IncludeGateway bool
	EchoEndpoints  []string // public-IP echo services; none → no public-IP lookup, gateway IDs only
	Salt           string   // generated once by the app, stored in the data dir
}

func (c Config) withDefaults() Config {
	if c.Debounce <= 0 {
		c.Debounce = 3 * time.Second
	}
	if c.Refresh <= 0 {
		c.Refresh = 30 * time.Minute
	}
	return c
}

// Deps are the watcher's collaborators; nil fields take the real implementations.
type Deps struct {
	Clock    clock.Clock
	Resolver analyzer.Resolver                                     // DoH, direct path
	PublicIP func(ctx context.Context) (netip.Addr, error)         // nil → default echo endpoints
	Uplink   func(ctx context.Context) (netif.Interface, error)    // nil → netif.Uplink
	Events   func(ctx context.Context) (<-chan netif.Event, error) // nil → netif.Watch
}

// Watcher identifies the network and reports changes.
type Watcher struct {
	cfg  Config
	deps Deps

	mu      sync.Mutex
	cur     Network
	curKey  string // uplinkKey of the interface cur was identified on
	changes chan Network
}

// New returns a watcher; call Run to start it.
func New(cfg Config, deps Deps) *Watcher {
	cfg = cfg.withDefaults()
	if deps.Clock == nil {
		deps.Clock = clock.Real()
	}
	if deps.Uplink == nil {
		deps.Uplink = netif.Uplink
	}
	if deps.Events == nil {
		deps.Events = netif.Watch
	}
	if deps.PublicIP == nil {
		deps.PublicIP = echoLookup(cfg.EchoEndpoints, deps.Uplink)
	}
	return &Watcher{cfg: cfg, deps: deps, changes: make(chan Network, 1)}
}

// Current returns the last identified network (Known=false before the first identification).
func (w *Watcher) Current() Network {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cur
}

// Changes delivers the network whenever its ID or Known flag changes (latest-wins, buffered 1).
func (w *Watcher) Changes() <-chan Network { return w.changes }

// Run identifies the network at start, on every (debounced) netif event and every Refresh, until ctx is done.
// It returns nil then.
func (w *Watcher) Run(ctx context.Context) error {
	events, err := w.deps.Events(ctx)
	if err != nil {
		events = nil // periodic refresh still works
	}
	refresh := w.deps.Clock.NewTimer(w.cfg.Refresh)
	defer refresh.Stop()
	var debounce clock.Timer
	var debounceC <-chan time.Time
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()

	identifyAndRearm := func() {
		w.identify(ctx)
		refresh.Reset(w.cfg.Refresh) // Go 1.23+ timers: no drain needed
	}
	identifyAndRearm()
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if debounce == nil {
				debounce = w.deps.Clock.NewTimer(w.cfg.Debounce)
				debounceC = debounce.C()
			}
		case <-debounceC:
			debounce, debounceC = nil, nil
			identifyAndRearm()
		case <-refresh.C():
			w.identify(ctx)
			refresh.Reset(w.cfg.Refresh)
		}
	}
}

// publish stores n as current and notifies subscribers if the identity changed.
func (w *Watcher) publish(n Network, key string) {
	w.mu.Lock()
	changed := n.ID != w.cur.ID || n.Known != w.cur.Known
	w.cur, w.curKey = n, key
	w.mu.Unlock()
	if !changed {
		return
	}
	select { // latest wins
	case <-w.changes:
	default:
	}
	select {
	case w.changes <- n:
	default:
	}
}
