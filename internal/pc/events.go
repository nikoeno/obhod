package pc

import (
	"context"
	"sync"
	"time"

	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/status"
)

// EventKind names an Event.
type EventKind string

// The event kinds. A state event carries no data of its own: the receiver
// reads State, LastError, Checking, Snapshot and Networks.
const (
	EventState   EventKind = "state"   // a state transition, an error set or cleared, the end of a check
	EventRoutes  EventKind = "routes"  // the route table changed
	EventCheck   EventKind = "check"   // a check started or ended
	EventTraffic EventKind = "traffic" // the traffic tick while connected
)

// Event is one notification for Subscribe. Only the field of its Kind is set.
type Event struct {
	Kind    EventKind
	Changes []status.Change // routes
	Check   CheckEvent      // check
	Traffic TrafficTick     // traffic
}

// CheckEvent is the payload of a check event.
type CheckEvent struct {
	Running bool
	Domains []string // what CheckNow was asked for (normalized); nil for all candidates or a scheduled check
	Changed int      // route changes published while the check ran; set when Running is false
}

// TrafficTick is the payload of a traffic event: aggregated per route, never
// per host or address.
type TrafficTick struct {
	Upload      int64 // sing-box totals since it started
	Download    int64
	Connections int
	Delta       map[route.Route]int64 // bytes per route since the previous tick
	Total       map[route.Route]int64 // bytes per route since the connect (Snapshot.Traffic)
	Savings     float64               // as Snapshot.Savings
	At          time.Time
}

const (
	subscriberBuffer = 64
	pollInterval     = 500 * time.Millisecond // checker and network watcher
	trafficInterval  = 2 * time.Second
	checkGraceTicks  = 8 // polls to wait for a check that CheckNow asked for to show as running
	maxNetworks      = 10
	maxHistory       = 10
	trafficProbe     = time.Second // bound of the Clash API call of one tick
)

// hub fans events out to subscribers without ever blocking the publisher.
type hub struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}
}

type subscriber struct{ ch chan Event }

func (h *hub) subscribe() (<-chan Event, func()) {
	s := &subscriber{ch: make(chan Event, subscriberBuffer)}
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[*subscriber]struct{}{}
	}
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s.ch, func() { h.drop(s) }
}

// drop removes s and closes its channel, once.
func (h *hub) drop(s *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[s]; ok {
		delete(h.subs, s)
		close(s.ch)
	}
}

// publish delivers ev to every subscriber. A subscriber whose buffer is full
// is dropped and its channel closed (it must resubscribe and resync).
func (h *hub) publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s.ch <- ev:
		default:
			delete(h.subs, s)
			close(s.ch)
		}
	}
}

// Subscribe delivers events to the gateway; the returned func unsubscribes and
// closes the channel. It never blocks the app: a subscriber that falls 64
// events behind is dropped, and its channel is closed.
func (a *App) Subscribe() (<-chan Event, func()) { return a.events.subscribe() }

func (a *App) publish(ev Event) { a.events.publish(ev) }

// eventClock is the clock of the check watcher and the traffic ticker.
func (a *App) eventClock() clock.Clock {
	if a.d.evClk != nil {
		return a.d.evClk
	}
	return a.d.clk
}

// watch runs until ctx ends. It polls the checker (a check starting and
// ending, the network label) every pollInterval and, while connected, sends a
// traffic tick every trafficInterval.
func (a *App) watch(ctx context.Context) {
	clk := a.eventClock()
	poll := clk.NewTicker(pollInterval)
	defer poll.Stop()
	traffic := clk.NewTicker(trafficInterval)
	defer traffic.Stop()
	var prev map[route.Route]int64 // Snapshot.Traffic at the previous tick; nil while disconnected
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C():
			a.pollChecker()
		case <-traffic.C():
			prev = a.trafficTick(ctx, prev)
		}
	}
}

// pollChecker turns the checker's Running flag into check events and records
// the network it identified.
func (a *App) pollChecker() {
	st := a.d.checker.Status()
	a.noteNetwork(st.Network.Known, st.Network.ID, networkLabel(st.Network))

	a.chkMu.Lock()
	var end *CheckEvent
	var start *CheckEvent
	switch {
	case st.Running && !a.chkActive:
		a.chkActive, a.chkSeen, a.chkGrace, a.chkDomains = true, true, 0, nil
		a.mu.Lock()
		a.chkStart = a.changeCount
		a.mu.Unlock()
		start = &CheckEvent{Running: true}
	case st.Running:
		a.chkSeen = true
	case a.chkActive && (a.chkSeen || a.chkGrace+1 >= checkGraceTicks):
		end = a.finishCheckLocked()
	case a.chkActive:
		a.chkGrace++
	}
	a.chkMu.Unlock()
	if start != nil {
		a.publish(Event{Kind: EventCheck, Check: *start})
	}
	if end != nil {
		a.publish(Event{Kind: EventCheck, Check: *end})
		a.publish(Event{Kind: EventState})
	}
}

// finishCheckLocked ends the active check. a.chkMu must be held.
func (a *App) finishCheckLocked() *CheckEvent {
	a.mu.Lock()
	changed := a.changeCount - a.chkStart
	a.mu.Unlock()
	ev := &CheckEvent{Running: false, Domains: a.chkDomains, Changed: changed}
	a.chkActive, a.chkSeen, a.chkGrace, a.chkDomains = false, false, 0, nil
	return ev
}

// noteCheckRequested records a manual check so its start is announced at once.
func (a *App) noteCheckRequested(domains []string) {
	a.chkMu.Lock()
	if a.chkActive {
		a.chkMu.Unlock()
		return
	}
	a.chkActive, a.chkSeen, a.chkGrace = true, false, 0
	a.chkDomains = append([]string(nil), domains...)
	if len(domains) == 0 {
		a.chkDomains = nil
	}
	a.mu.Lock()
	a.chkStart = a.changeCount
	a.mu.Unlock()
	echo := a.chkDomains
	a.chkMu.Unlock()
	a.publish(Event{Kind: EventCheck, Check: CheckEvent{Running: true, Domains: echo}})
}

// trafficTick sends one traffic event while connected and returns the byte
// counters to diff against at the next tick (nil when not connected).
func (a *App) trafficTick(ctx context.Context, prev map[route.Route]int64) map[route.Route]int64 {
	if a.State() != StateConnected {
		return nil
	}
	snap := a.Snapshot()
	tick := TrafficTick{Delta: map[route.Route]int64{}, Total: snap.Traffic, Savings: snap.Savings, At: a.eventClock().Now()}
	for r, b := range snap.Traffic {
		if d := b - prev[r]; d > 0 {
			tick.Delta[r] = d
		}
	}
	cctx, cancel := context.WithTimeout(ctx, trafficProbe)
	cs, err := a.liveClient().Connections(cctx)
	cancel()
	if err == nil {
		tick.Upload, tick.Download, tick.Connections = cs.UploadTotal, cs.DownloadTotal, len(cs.Connections)
	} else {
		a.log.Debug("traffic tick: clash api did not answer")
	}
	a.publish(Event{Kind: EventTraffic, Traffic: tick})
	next := make(map[route.Route]int64, len(snap.Traffic))
	for r, b := range snap.Traffic {
		next[r] = b
	}
	return next
}
