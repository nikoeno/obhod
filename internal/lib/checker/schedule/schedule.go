// Package schedule decides when to run checks: timer with jitter, network change, expired TTL, failure threshold.
package schedule

import (
	"context"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/nikoejento/obhod/internal/lib/checker/lib/model"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/route"
)

// Kind says why a check was triggered.
type Kind string

// The trigger kinds.
const (
	Startup       Kind = "startup"
	Timer         Kind = "timer"
	NetworkChange Kind = "network_change"
	Expired       Kind = "expired"
	Failures      Kind = "failures"
	Manual        Kind = "manual"
)

// rank orders kinds for the reported Kind of a merged trigger (higher wins).
func (k Kind) rank() int {
	switch k {
	case NetworkChange:
		return 6
	case Failures:
		return 5
	case Manual:
		return 4
	case Expired:
		return 3
	case Timer:
		return 2
	case Startup:
		return 1
	}
	return 0
}

// Trigger asks the checker to run one cycle.
type Trigger struct {
	Kind    Kind
	Network model.NetworkID
	Domains []string // nil = all candidates
	At      time.Time
}

// Config tunes the scheduler. Zero fields take their defaults.
type Config struct {
	Interval      time.Duration // default 1h
	Jitter        time.Duration // default ±10m
	MinGap        time.Duration // min time between full checks; default 5m
	FailThreshold int           // default 3
	FailWindow    time.Duration // default 5m
}

func (c Config) withDefaults() Config {
	if c.Interval <= 0 {
		c.Interval = time.Hour
	}
	if c.Jitter <= 0 {
		c.Jitter = 10 * time.Minute
	}
	if c.MinGap <= 0 {
		c.MinGap = 5 * time.Minute
	}
	if c.FailThreshold <= 0 {
		c.FailThreshold = 3
	}
	if c.FailWindow <= 0 {
		c.FailWindow = 5 * time.Minute
	}
	// Keep Interval - Jitter >= MinGap so the timer never fires inside the gap.
	if c.Interval < c.MinGap {
		c.Interval = c.MinGap
	}
	if c.Jitter > c.Interval-c.MinGap {
		c.Jitter = c.Interval - c.MinGap
	}
	return c
}

type failKey struct {
	net    model.NetworkID
	domain string
	route  route.Route
}

// Scheduler turns events into triggers. Its methods never block and are safe for concurrent use.
type Scheduler struct {
	cfg Config
	clk clock.Clock
	rng *rand.Rand // used by Run only
	out chan Trigger

	mu       sync.Mutex
	wake     chan struct{}
	net      model.NetworkID
	pending  *Trigger
	sent     uint64 // triggers handed to the checker
	done     uint64 // Done calls; inFlight while sent > done (Done may race ahead of the send bookkeeping)
	lastFull time.Time
	next     time.Time // when the periodic timer is due (for Next)
	fails    map[failKey][]time.Time
}

// New returns a scheduler whose jitter is seeded from the clock.
func New(cfg Config, c clock.Clock) *Scheduler {
	return newWithRand(cfg, c, rand.New(rand.NewSource(c.Now().UnixNano())))
}

func newWithRand(cfg Config, c clock.Clock, rng *rand.Rand) *Scheduler {
	return &Scheduler{
		cfg: cfg.withDefaults(), clk: c, rng: rng,
		out:   make(chan Trigger),
		wake:  make(chan struct{}, 1),
		fails: map[failKey][]time.Time{},
	}
}

// Triggers delivers at most one trigger at a time; the next one is offered after Done.
func (s *Scheduler) Triggers() <-chan Trigger { return s.out }

// Next returns when the periodic check is next due (zero before Run starts).
func (s *Scheduler) Next() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next
}

// interval returns Interval ± rand(Jitter).
func (s *Scheduler) interval() time.Duration {
	j := time.Duration(0)
	if s.cfg.Jitter > 0 {
		j = time.Duration(s.rng.Int63n(int64(2*s.cfg.Jitter)+1)) - s.cfg.Jitter
	}
	return s.cfg.Interval + j
}

// Run emits a Startup trigger and then serves events until ctx is done.
//
// A trigger counts as in flight from the moment it is offered on Triggers(): the offer is withdrawn (and merged
// back into the pending trigger) if another event arrives before the checker receives it. That way Done can
// never race ahead of the bookkeeping.
func (s *Scheduler) Run(ctx context.Context) error {
	s.add(Trigger{Kind: Startup}, false)
	periodic := s.clk.NewTimer(s.armPeriodic())
	defer periodic.Stop()
	resetPeriodic := func() {
		periodic.Reset(s.armPeriodic()) // Go 1.23+ timers: no drain needed
	}
	for {
		var (
			send     chan<- Trigger
			snap     Trigger
			wait     time.Duration
			prevFull time.Time
		)
		s.mu.Lock()
		if s.pending != nil && s.sent <= s.done {
			if wait = s.waitLocked(); wait <= 0 {
				send, snap = s.out, *s.pending
				s.pending = nil
				s.sent++
				prevFull = s.lastFull
				if snap.Domains == nil {
					s.lastFull = s.clk.Now()
				}
			}
		}
		s.mu.Unlock()
		if send != nil && snap.Domains == nil {
			resetPeriodic() // a full check is about to run: restart the periodic countdown
		}

		var gap clock.Timer
		var gapC <-chan time.Time
		if wait > 0 {
			gap = s.clk.NewTimer(wait)
			gapC = gap.C()
		}
		sentOK := false
		select {
		case <-ctx.Done():
			if gap != nil {
				gap.Stop()
			}
			return nil
		case <-s.wake:
		case <-gapC:
		case <-periodic.C():
			s.add(Trigger{Kind: Timer}, false)
			periodic.Reset(s.armPeriodic())
		case send <- snap:
			sentOK = true
		}
		if gap != nil {
			gap.Stop()
		}
		if send != nil && !sentOK {
			s.withdraw(snap, prevFull)
		}
	}
}

// withdraw takes back an offered trigger: it is merged with whatever arrived since.
func (s *Scheduler) withdraw(offered Trigger, prevFull time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent--
	s.lastFull = prevFull
	if s.pending != nil {
		merged := offered
		mergeInto(&merged, *s.pending)
		s.pending = &merged
	} else {
		s.pending = &offered
	}
}

// armPeriodic picks the next interval and records when it is due.
func (s *Scheduler) armPeriodic() time.Duration {
	d := s.interval()
	s.mu.Lock()
	s.next = s.clk.Now().Add(d)
	s.mu.Unlock()
	return d
}

// waitLocked returns how long the pending trigger must wait: only full timer/expiry/startup checks respect
// MinGap. Network changes, manual requests and targeted rechecks go out at once.
func (s *Scheduler) waitLocked() time.Duration {
	p := s.pending
	if p.Domains != nil || p.Kind == NetworkChange || p.Kind == Manual || s.lastFull.IsZero() {
		return 0
	}
	return s.lastFull.Add(s.cfg.MinGap).Sub(s.clk.Now())
}

func (t Trigger) clone() Trigger {
	if t.Domains != nil {
		t.Domains = append([]string(nil), t.Domains...)
	}
	return t
}

// add merges t into the pending trigger. useNet=false stamps the scheduler's current network.
func (s *Scheduler) add(t Trigger, keepNet bool) {
	s.mu.Lock()
	s.addLocked(t, keepNet)
	s.mu.Unlock()
	s.signal()
}

func (s *Scheduler) addLocked(t Trigger, keepNet bool) {
	if !keepNet {
		t.Network = s.net
	}
	if t.At.IsZero() {
		t.At = s.clk.Now()
	}
	t.Domains = normalizeDomains(t.Domains)
	if s.pending == nil {
		c := t.clone()
		s.pending = &c
		return
	}
	mergeInto(s.pending, t)
}

// mergeInto folds the newer trigger t into p: the higher-priority kind wins, domains are a union (nil absorbs
// everything), a network change replaces the network, and At is the latest.
func mergeInto(p *Trigger, t Trigger) {
	if t.Kind.rank() > p.Kind.rank() {
		p.Kind = t.Kind
	}
	if p.Domains == nil || t.Domains == nil {
		p.Domains = nil
	} else {
		p.Domains = normalizeDomains(append(p.Domains, t.Domains...))
	}
	if t.Kind == NetworkChange || p.Network == "" {
		p.Network = t.Network
	}
	if t.At.After(p.At) {
		p.At = t.At
	}
}

// normalizeDomains sorts and dedups; nil stays nil, and an empty non-nil list becomes nil (= all).
func normalizeDomains(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	j := 0
	for i, d := range out {
		if i == 0 || d != out[j-1] {
			out[j] = d
			j++
		}
	}
	return out[:j]
}

func (s *Scheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// NetworkChanged requests an immediate full check for the new network. It bypasses MinGap and supersedes
// the network of any pending trigger; failure counters of the old network are dropped.
func (s *Scheduler) NetworkChanged(id model.NetworkID) {
	s.mu.Lock()
	s.net = id
	s.fails = map[failKey][]time.Time{}
	s.addLocked(Trigger{Kind: NetworkChange, Network: id}, true)
	s.mu.Unlock()
	s.signal()
}

// Expired asks to recheck domains whose decisions ran out (nil = everything).
func (s *Scheduler) Expired(id model.NetworkID, domains []string) {
	s.mu.Lock()
	if s.net != "" && id != s.net {
		s.mu.Unlock()
		return
	}
	s.addLocked(Trigger{Kind: Expired, Network: id, Domains: domains}, true)
	s.mu.Unlock()
	s.signal()
}

// Failure records a live failure of domain over r. FailThreshold failures of the same (network, domain,
// route) within FailWindow trigger a recheck of that domain, and reset the counter.
func (s *Scheduler) Failure(id model.NetworkID, domain string, r route.Route, at time.Time) {
	s.mu.Lock()
	if s.net != "" && id != s.net {
		s.mu.Unlock()
		return
	}
	k := failKey{id, domain, r}
	cut := at.Add(-s.cfg.FailWindow)
	times := s.fails[k]
	kept := times[:0]
	for _, ft := range times {
		if ft.After(cut) {
			kept = append(kept, ft)
		}
	}
	kept = append(kept, at)
	if len(kept) < s.cfg.FailThreshold {
		s.fails[k] = kept
		s.mu.Unlock()
		return
	}
	delete(s.fails, k)
	s.addLocked(Trigger{Kind: Failures, Network: id, Domains: []string{domain}, At: at}, true)
	s.mu.Unlock()
	s.signal()
}

// Manual asks for an immediate check of domains (nil = all candidates).
func (s *Scheduler) Manual(domains []string) { s.add(Trigger{Kind: Manual, Domains: domains}, false) }

// Done tells the scheduler that the checker finished handling t (call it exactly once per trigger); whatever merged meanwhile goes out next.
func (s *Scheduler) Done(Trigger) {
	s.mu.Lock()
	s.done++
	s.mu.Unlock()
	s.signal()
}
