// Package checker runs the network-aware probe → decide → store cycle and publishes the current RouteTable.
package checker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/checker/decide"
	"github.com/nikoejento/obhod/internal/lib/checker/lists"
	"github.com/nikoejento/obhod/internal/lib/checker/netwatch"
	"github.com/nikoejento/obhod/internal/lib/checker/schedule"
	"github.com/nikoejento/obhod/internal/lib/checker/store"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/route"
)

// Child config types, re-exported so apps don't import the children.
type (
	// ListsConfig names the list files.
	ListsConfig = lists.Config
	// ScheduleConfig tunes when checks run.
	ScheduleConfig = schedule.Config
	// DecidePolicy tunes how results become decisions.
	DecidePolicy = decide.Policy
	// StoreConfig configures decision persistence.
	StoreConfig = store.Config
	// NetwatchConfig tunes network identification.
	NetwatchConfig = netwatch.Config
)

// Config configures a Checker.
type Config struct {
	Lists          ListsConfig
	Schedule       ScheduleConfig
	Decide         DecidePolicy
	Store          StoreConfig
	Netwatch       NetwatchConfig
	UnknownDefault route.Route   // route for candidates without a decision; default tunnel
	Retention      time.Duration // networks unseen for this long are pruned at start; default 90 days
}

// DefaultRetention is the Config.Retention default.
const DefaultRetention = 90 * 24 * time.Hour

// Deps are the checker's collaborators.
type Deps struct {
	Runner   analyzer.Runner
	Dialers  []analyzer.Dialer // routes the checker may probe (currently: direct)
	Clock    clock.Clock       // nil → real
	DataDir  string            // where store keeps decisions
	Resolver analyzer.Resolver // DoH resolver for network identification; nil → gateway IDs only
	// Optional overrides for tests; nil → real implementations from children.
	Network NetworkSource
}

// NetworkSource is what the checker needs from network detection (netwatch implements it).
type NetworkSource interface {
	Current() Network
	Changes() <-chan Network
	Run(ctx context.Context) error
}

// Network is the checker's view of the network it is on.
type Network struct {
	ID     string // opaque networkId, e.g. "asn:12389"
	ASN    uint32
	ASName string
	Known  bool // false until identified
}

// Failure is a live connection failure reported by a monitor.
type Failure struct {
	Domain  string
	Route   route.Route
	Verdict analyzer.Verdict
	At      time.Time
}

// Status is a snapshot for status pages.
type Status struct {
	Network    Network
	LastCheck  time.Time
	NextCheck  time.Time
	Running    bool
	Counts     map[route.Route]int
	Expired    int
	LastReport *analyzer.Report // most recent, for status pages
	Warnings   []string
}

const maxWarnings = 50

// Checker owns decisions: it probes, decides, stores and publishes route tables.
type Checker struct {
	cfg   Config
	deps  Deps
	clk   clock.Clock
	store store.Store
	sched *schedule.Scheduler
	net   NetworkSource

	candidates []string // sorted
	candSet    map[string]bool
	static     []route.Entry

	table atomic.Pointer[route.Table]

	subMu   sync.Mutex
	subs    map[int]chan route.Table
	nextSub int

	mu         sync.Mutex
	cur        Network
	lastCheck  time.Time
	running    bool
	lastReport *analyzer.Report
	fixed      []string // warnings from New (lists, store)
	dynamic    []string // warnings from cycles, newest last
	started    bool
}

// New validates cfg, loads the lists, opens the store and wires the children.
func New(cfg Config, deps Deps) (*Checker, error) {
	cfg, err := cfg.normalize()
	if err != nil {
		return nil, err
	}
	if deps.Clock == nil {
		deps.Clock = clock.Real()
	}
	if cfg.Store.File == "" {
		if deps.DataDir == "" {
			return nil, errors.New("checker: neither Store.File nor Deps.DataDir is set")
		}
		cfg.Store.File = filepath.Join(deps.DataDir, "decisions.json")
	}
	st, err := store.Open(cfg.Store, deps.Clock)
	if err != nil {
		return nil, fmt.Errorf("checker: %w", err)
	}
	c, err := newWithStore(cfg, deps, st)
	if err != nil {
		st.Close()
		return nil, err
	}
	return c, nil
}

// Validate reports the config errors New would report, without loading the
// lists or opening the store.
func (cfg Config) Validate() error {
	_, err := cfg.normalize()
	return err
}

func (cfg Config) normalize() (Config, error) {
	if cfg.UnknownDefault == "" {
		cfg.UnknownDefault = route.Tunnel
	}
	if !cfg.UnknownDefault.Checkable() {
		return cfg, fmt.Errorf("checker: UnknownDefault %q must be direct, desync or tunnel", cfg.UnknownDefault)
	}
	for _, r := range cfg.Decide.Order {
		if !r.Checkable() {
			return cfg, fmt.Errorf("checker: Decide.Order contains %q, which is not decidable", r)
		}
	}
	if cfg.Retention == 0 {
		cfg.Retention = DefaultRetention
	}
	if cfg.Retention < 0 {
		return cfg, errors.New("checker: negative Retention")
	}
	return cfg, nil
}

// newWithStore is New with an injected store (a test seam); cfg must already be normalized.
func newWithStore(cfg Config, deps Deps, st store.Store) (*Checker, error) {
	if deps.Runner == nil {
		return nil, errors.New("checker: Deps.Runner is nil")
	}
	if deps.Clock == nil {
		deps.Clock = clock.Real()
	}
	l, err := lists.Load(cfg.Lists)
	if err != nil {
		return nil, fmt.Errorf("checker: %w", err)
	}
	c := &Checker{
		cfg: cfg, deps: deps, clk: deps.Clock, store: st,
		sched:      schedule.New(cfg.Schedule, deps.Clock),
		candidates: l.Candidates, static: l.Static,
		candSet: make(map[string]bool, len(l.Candidates)),
		subs:    map[int]chan route.Table{},
		fixed:   append(append([]string(nil), l.Warnings...), st.Warnings()...),
	}
	for _, d := range l.Candidates {
		c.candSet[d] = true
	}
	if deps.Network != nil {
		c.net = deps.Network
	} else {
		c.net = newNetwatchSource(netwatch.New(cfg.Netwatch, netwatch.Deps{Clock: deps.Clock, Resolver: deps.Resolver}))
	}
	c.cur = c.net.Current()
	c.rebuild()
	return c, nil
}

// Table returns the current table for the current network (never blocks).
func (c *Checker) Table() route.Table {
	if t := c.table.Load(); t != nil {
		return *t
	}
	return route.Table{}
}

// Subscribe delivers a new Table whenever it changes (latest-wins, buffered 1). The cancel func unsubscribes
// and closes the channel.
func (c *Checker) Subscribe() (<-chan route.Table, func()) {
	ch := make(chan route.Table, 1)
	c.subMu.Lock()
	id := c.nextSub
	c.nextSub++
	c.subs[id] = ch
	c.subMu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			c.subMu.Lock()
			delete(c.subs, id)
			close(ch)
			c.subMu.Unlock()
		})
	}
}

// publish makes t the current table and notifies subscribers, unless it equals the previous one.
func (c *Checker) publish(t route.Table) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	if prev := c.table.Load(); prev != nil && prev.Equal(t) {
		return
	}
	c.table.Store(&t)
	for _, ch := range c.subs {
		select { // latest wins
		case <-ch:
		default:
		}
		ch <- t // cap 1 and just drained under subMu: never blocks
	}
}

// ReportFailure feeds a live connection failure into the failure threshold. Failures of static entries, of
// unknown domains, or on a route other than the current one are ignored.
func (c *Checker) ReportFailure(f Failure) {
	c.mu.Lock()
	net := c.cur
	c.mu.Unlock()
	if !net.Known {
		return
	}
	e, ok := c.Table().Lookup(f.Domain)
	if !ok || e.Source == route.SourceStatic || e.Route != f.Route {
		return
	}
	c.sched.Failure(modelID(net.ID), e.Domain, f.Route, f.At)
}

// CheckNow schedules an immediate check of the given domains (all candidates if none).
func (c *Checker) CheckNow(domains ...string) {
	var ds []string
	for _, d := range domains {
		if n, err := route.Normalize(d); err == nil {
			ds = append(ds, n)
		}
	}
	if len(domains) > 0 && len(ds) == 0 {
		return
	}
	c.sched.Manual(ds)
}

// Status returns a snapshot of the checker's state.
func (c *Checker) Status() Status {
	c.mu.Lock()
	s := Status{
		Network: c.cur, LastCheck: c.lastCheck, Running: c.running, LastReport: c.lastReport,
		Warnings: append(append([]string(nil), c.fixed...), c.dynamic...),
	}
	c.mu.Unlock()
	s.NextCheck = c.sched.Next()
	s.Counts = c.Table().Counts()
	if s.Network.Known {
		now := c.clk.Now()
		for _, d := range c.store.All(modelID(s.Network.ID)) {
			if c.candSet[d.Domain] && d.Expired(now) {
				s.Expired++
			}
		}
	}
	return s
}

// warn records a runtime problem for Status.Warnings (bounded, newest kept).
func (c *Checker) warn(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dynamic = append(c.dynamic, fmt.Sprintf(format, args...))
	if n := len(c.dynamic); n > maxWarnings {
		c.dynamic = append([]string(nil), c.dynamic[n-maxWarnings:]...)
	}
}
