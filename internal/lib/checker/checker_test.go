package checker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/checker/lib/model"
	"github.com/nikoejento/obhod/internal/lib/checker/store"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/route"
)

var t0 = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

const (
	netA = "asn:100"
	netB = "asn:200"
)

var (
	nA = Network{ID: netA, ASN: 100, ASName: "A", Known: true}
	nB = Network{ID: netB, ASN: 200, ASName: "B", Known: true}
)

// fakeRunner scripts probe reports: verdict(domain) per target, a calibration state, and an optional
// one-shot hold that keeps the next Run blocked until released.
type fakeRunner struct {
	mu       sync.Mutex
	verdict  func(domain string) string
	internet analyzer.CheckState
	hold     chan struct{}
	calls    chan analyzer.Batch
}

func newRunner() *fakeRunner {
	return &fakeRunner{
		verdict:  func(string) string { return "OK" },
		internet: analyzer.CheckOK,
		calls:    make(chan analyzer.Batch, 100),
	}
}

func (f *fakeRunner) set(fn func(domain string) string) {
	f.mu.Lock()
	f.verdict = fn
	f.mu.Unlock()
}

func (f *fakeRunner) Run(ctx context.Context, b analyzer.Batch) (analyzer.Report, error) {
	f.calls <- b
	f.mu.Lock()
	verdict, internet, hold := f.verdict, f.internet, f.hold
	f.hold = nil
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return analyzer.Report{}, ctx.Err()
		}
	}
	rep := analyzer.Report{Calibration: analyzer.Calibration{Internet: internet}}
	for _, t := range b.Targets {
		rep.Results = append(rep.Results, analyzer.ProbeResult{Target: t, Route: route.Direct, Verdict: analyzer.Verdict(verdict(t.Domain))})
	}
	return rep, nil
}

type tc struct {
	c      *Checker
	net    *FakeNetwork
	runner *fakeRunner
	clk    *clock.Fake
	store  store.Store
	done   chan error
	cancel context.CancelFunc
}

func writeLists(t *testing.T) ListsConfig {
	t.Helper()
	dir := t.TempDir()
	w := func(name, body string) []string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return []string{p}
	}
	return ListsConfig{
		Candidates: w("cand.txt", "a.com\nb.com\np.com\n"),
		Proxy:      w("proxy.txt", "p.com\n"),
		Block:      w("block.txt", "x.com\n"),
	}
}

// setup builds a checker over an in-memory store; run is not started.
func setup(t *testing.T, initial Network, cfg Config) *tc {
	t.Helper()
	cfg.Lists = writeLists(t)
	if cfg.Schedule.Interval == 0 {
		cfg.Schedule.Interval = 1000 * time.Hour // keep the periodic timer out of the way
	}
	cfg, err := cfg.normalize()
	if err != nil {
		t.Fatal(err)
	}
	x := &tc{
		net: NewFakeNetwork(initial), runner: newRunner(), clk: clock.NewFake(t0), store: store.NewMemory(),
		done: make(chan error, 1),
	}
	x.c, err = newWithStore(cfg, Deps{Runner: x.runner, Clock: x.clk, Network: x.net}, x.store)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func (x *tc) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	x.cancel = cancel
	go func() { x.done <- x.c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-x.done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return")
		}
	})
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (x *tc) routeOf(domain string) (route.Route, route.Source) {
	e, _ := x.c.Table().Lookup(domain)
	return e.Route, e.Source
}

func (x *tc) waitRoute(t *testing.T, domain string, want route.Route, src route.Source) {
	t.Helper()
	eventually(t, domain+" → "+string(want), func() bool {
		r, s := x.routeOf(domain)
		return r == want && s == src
	})
}

func (x *tc) call(t *testing.T) analyzer.Batch {
	t.Helper()
	select {
	case b := <-x.runner.calls:
		return b
	case <-time.After(5 * time.Second):
		t.Fatal("no probe run")
	}
	return analyzer.Batch{}
}

func (x *tc) noCall(t *testing.T) {
	t.Helper()
	select {
	case b := <-x.runner.calls:
		t.Fatalf("unexpected probe run: %+v", b.Targets)
	case <-time.After(50 * time.Millisecond):
	}
}

func domains(b analyzer.Batch) []string {
	var out []string
	for _, t := range b.Targets {
		out = append(out, t.Domain)
	}
	return out
}

func TestStartupOnUnknownNetworkPublishesDefaults(t *testing.T) {
	x := setup(t, Network{}, Config{})
	tab := x.c.Table()
	if tab.NetworkID != "" {
		t.Errorf("NetworkID = %q", tab.NetworkID)
	}
	for dom, want := range map[string]route.Entry{
		"a.com": {Route: route.Tunnel, Source: route.SourceDefault},
		"b.com": {Route: route.Tunnel, Source: route.SourceDefault},
		"p.com": {Route: route.Proxy, Source: route.SourceStatic},
		"x.com": {Route: route.Block, Source: route.SourceStatic},
	} {
		e, ok := tab.Lookup(dom)
		if !ok || e.Route != want.Route || e.Source != want.Source {
			t.Errorf("%s = %+v, want %+v", dom, e, want)
		}
	}
	x.start(t)
	x.noCall(t) // no probing before the network is identified
	st := x.c.Status()
	if st.Network.Known || st.Counts[route.Tunnel] != 2 {
		t.Errorf("status = %+v", st)
	}
}

func TestNetworkSwitchRestoresDecisions(t *testing.T) {
	x := setup(t, Network{}, Config{})
	x.runner.set(func(d string) string {
		if x.net.Current().ID == netB && d == "a.com" {
			return "TLS_RST"
		}
		if x.net.Current().ID == netB && d == "b.com" {
			return "OK"
		}
		if d == "b.com" {
			return "TLS_RST"
		}
		return "OK"
	})
	x.start(t)
	x.noCall(t)

	x.net.Set(nA)
	x.waitRoute(t, "a.com", route.Direct, route.SourceChecked)
	x.waitRoute(t, "b.com", route.Tunnel, route.SourceChecked)
	if got := x.c.Table().NetworkID; got != netA {
		t.Errorf("NetworkID = %q", got)
	}
	if ds := domains(x.call(t)); len(ds) != 2 || ds[0] != "a.com" || ds[1] != "b.com" {
		t.Errorf("first batch = %v (statics must not be probed)", ds)
	}

	x.net.Set(nB)
	x.waitRoute(t, "a.com", route.Tunnel, route.SourceChecked)
	x.waitRoute(t, "b.com", route.Direct, route.SourceChecked)

	// Back to A with the probe held: A's stored decisions are already in the table.
	x.runner.mu.Lock()
	x.runner.hold = make(chan struct{})
	hold := x.runner.hold
	x.runner.mu.Unlock()
	x.net.Set(nA)
	eventually(t, "table of A", func() bool { return x.c.Table().NetworkID == netA })
	if r, _ := x.routeOf("a.com"); r != route.Direct {
		t.Errorf("a.com = %s before any probe, want direct", r)
	}
	if r, _ := x.routeOf("b.com"); r != route.Tunnel {
		t.Errorf("b.com = %s before any probe, want tunnel", r)
	}
	close(hold)

	if nets := x.store.Networks(); len(nets) != 2 || nets[0].Label != "A" {
		t.Errorf("networks = %+v", nets)
	}
}

func TestHysteresisOverTwoRuns(t *testing.T) {
	x := setup(t, nA, Config{})
	x.start(t)
	x.waitRoute(t, "a.com", route.Direct, route.SourceChecked)
	x.call(t)

	x.runner.set(func(string) string { return "TLS_RST" })
	x.c.CheckNow("a.com")
	eventually(t, "first confirmation", func() bool {
		d, ok := x.store.Get(netA, "a.com")
		return ok && d.Pending != nil && d.Pending.Count == 1
	})
	if r, _ := x.routeOf("a.com"); r != route.Direct {
		t.Fatalf("switched after one run: %s", r)
	}
	x.c.CheckNow("a.com")
	x.waitRoute(t, "a.com", route.Tunnel, route.SourceChecked)
}

func TestFailureThresholdTriggersTargetedRecheck(t *testing.T) {
	x := setup(t, nA, Config{})
	x.start(t)
	x.waitRoute(t, "a.com", route.Direct, route.SourceChecked)
	x.waitRoute(t, "b.com", route.Direct, route.SourceChecked)
	x.call(t)
	x.noCall(t)

	// Static and foreign-route failures never count.
	for i := 0; i < 5; i++ {
		x.c.ReportFailure(Failure{Domain: "p.com", Route: route.Proxy, At: x.clk.Now()})
		x.c.ReportFailure(Failure{Domain: "a.com", Route: route.Tunnel, At: x.clk.Now()})
		x.c.ReportFailure(Failure{Domain: "unknown.org", Route: route.Direct, At: x.clk.Now()})
	}
	x.noCall(t)

	for i := 0; i < 3; i++ {
		x.c.ReportFailure(Failure{Domain: "www.a.com", Route: route.Direct, Verdict: "TLS_RST", At: x.clk.Now()})
	}
	if ds := domains(x.call(t)); len(ds) != 1 || ds[0] != "a.com" {
		t.Errorf("recheck batch = %v, want [a.com]", ds)
	}
}

func TestStaticEntriesAreNeverOverridden(t *testing.T) {
	x := setup(t, nA, Config{})
	x.runner.set(func(string) string { return "TLS_RST" })
	x.start(t)
	x.waitRoute(t, "a.com", route.Tunnel, route.SourceChecked)
	for _, b := range []analyzer.Batch{x.call(t)} {
		for _, d := range domains(b) {
			if d == "p.com" || d == "x.com" {
				t.Errorf("static domain %s was probed", d)
			}
		}
	}
	if r, s := x.routeOf("p.com"); r != route.Proxy || s != route.SourceStatic {
		t.Errorf("p.com = %s/%s", r, s)
	}
	if r, s := x.routeOf("x.com"); r != route.Block || s != route.SourceStatic {
		t.Errorf("x.com = %s/%s", r, s)
	}
}

func TestCalibrationFailureChangesNothing(t *testing.T) {
	x := setup(t, nA, Config{})
	x.start(t)
	x.waitRoute(t, "a.com", route.Direct, route.SourceChecked)
	x.call(t)
	before := x.c.Table()

	x.runner.mu.Lock()
	x.runner.internet = analyzer.CheckFailed
	x.runner.mu.Unlock()
	x.runner.set(func(string) string { return "TLS_RST" })
	x.c.CheckNow()
	x.call(t)
	eventually(t, "calibration warning", func() bool {
		for _, w := range x.c.Status().Warnings {
			if strings.Contains(w, "calibration") {
				return true
			}
		}
		return false
	})
	d, _ := x.store.Get(netA, "a.com")
	if d.Route != route.Direct || d.Pending != nil {
		t.Errorf("decision changed: %+v", d)
	}
	if !x.c.Table().Equal(before) {
		t.Error("table changed")
	}
	if x.c.Status().LastReport == nil {
		t.Error("LastReport not kept")
	}
}

func TestNetworkChangeDuringProbeDropsResults(t *testing.T) {
	x := setup(t, Network{}, Config{})
	x.start(t)
	x.noCall(t)

	hold := make(chan struct{})
	x.runner.mu.Lock()
	x.runner.hold = hold
	x.runner.mu.Unlock()
	x.net.Set(nA)
	x.call(t) // the probe for A is now in flight
	x.net.Set(nB)
	close(hold)

	x.call(t) // then B is probed
	x.waitRoute(t, "a.com", route.Direct, route.SourceChecked)
	if got := x.c.Table().NetworkID; got != netB {
		t.Errorf("NetworkID = %q", got)
	}
	if _, ok := x.store.Get(netA, "a.com"); ok {
		t.Error("results of the old network were stored")
	}
	if _, ok := x.store.Get(netB, "a.com"); !ok {
		t.Error("no decision for the new network")
	}
}

func TestExpiryTriggersRecheck(t *testing.T) {
	x := setup(t, nA, Config{Decide: DecidePolicy{TTL: time.Hour}})
	x.start(t)
	x.waitRoute(t, "a.com", route.Direct, route.SourceChecked)
	x.call(t)
	x.noCall(t)
	if x.c.Status().Expired != 0 {
		t.Error("nothing should be expired yet")
	}

	// Waiters: expiry timer (+ scheduler's periodic timer).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := x.clk.BlockUntil(ctx, 2); err != nil {
		t.Fatal(err)
	}
	x.clk.Advance(time.Hour + time.Minute)
	if ds := domains(x.call(t)); len(ds) != 2 {
		t.Errorf("expired recheck batch = %v", ds)
	}
	// Expired entries stay in use while rechecking.
	if r, _ := x.routeOf("a.com"); r != route.Direct {
		t.Errorf("a.com = %s", r)
	}
}

func TestExpiredDecisionsRecheckedAtStart(t *testing.T) {
	x := setup(t, nA, Config{})
	old := model.Decision{Domain: "a.com", Route: route.Desync, Source: route.SourceChecked, CheckedAt: t0.Add(-48 * time.Hour), ExpiresAt: t0.Add(-24 * time.Hour)}
	if err := x.store.Put(netA, old); err != nil {
		t.Fatal(err)
	}
	x.c.rebuild()
	if r, _ := x.routeOf("a.com"); r != route.Desync {
		t.Fatalf("expired decision not used: %s", r)
	}
	if x.c.Status().Expired != 1 {
		t.Errorf("Expired = %d", x.c.Status().Expired)
	}
	x.start(t)
	x.call(t)
}

func TestSubscribeLatestWinsAndCancel(t *testing.T) {
	x := setup(t, nA, Config{})
	ch, cancel := x.c.Subscribe()
	mk := func(id string, r route.Route) route.Table {
		tab, err := route.NewTable(id, t0, []route.Entry{{Domain: "a.com", Route: r, Source: route.SourceChecked}})
		if err != nil {
			t.Fatal(err)
		}
		return tab
	}
	x.c.publish(mk("asn:1", route.Direct))
	x.c.publish(mk("asn:1", route.Tunnel)) // slow reader: the first one is replaced
	x.c.publish(mk("asn:1", route.Tunnel)) // equal: not re-sent
	x.c.publish(mk("asn:1", route.Desync))
	got := <-ch
	if e, _ := got.Lookup("a.com"); e.Route != route.Desync {
		t.Errorf("got %+v, want the latest table", got)
	}
	select {
	case extra := <-ch:
		t.Errorf("extra table %+v", extra)
	default:
	}
	cancel()
	cancel() // idempotent
	if _, ok := <-ch; ok {
		t.Error("channel not closed by cancel")
	}
	x.c.publish(mk("asn:1", route.Direct)) // no subscribers left: must not panic
}

type failingPut struct{ store.Store }

func (failingPut) Put(model.NetworkID, ...model.Decision) error { return errors.New("disk full") }

func TestStorePutErrorIsFatal(t *testing.T) {
	x := setup(t, nA, Config{})
	x.c.store = failingPut{x.store}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { x.done <- x.c.Run(ctx) }()
	select {
	case err := <-x.done:
		if err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Errorf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not fail")
	}
}

func TestRunTwiceFails(t *testing.T) {
	x := setup(t, Network{}, Config{})
	x.start(t)
	eventually(t, "first Run to start", func() bool {
		x.c.mu.Lock()
		defer x.c.mu.Unlock()
		return x.c.started
	})
	if err := x.c.Run(context.Background()); err == nil {
		t.Error("second Run succeeded")
	}
}

func TestConfigValidation(t *testing.T) {
	if _, err := (Config{UnknownDefault: route.Proxy}).normalize(); err == nil {
		t.Error("proxy accepted as UnknownDefault")
	}
	if _, err := (Config{Decide: DecidePolicy{Order: []route.Route{route.Block}}}).normalize(); err == nil {
		t.Error("block accepted in Order")
	}
	if _, err := (Config{Retention: -1}).normalize(); err == nil {
		t.Error("negative retention accepted")
	}
	cfg, err := (Config{}).normalize()
	if err != nil || cfg.UnknownDefault != route.Tunnel || cfg.Retention != DefaultRetention {
		t.Errorf("defaults: %+v %v", cfg, err)
	}
	if _, err := New(Config{}, Deps{Runner: newRunner()}); err == nil {
		t.Error("New without a data dir succeeded")
	}
	if _, err := New(Config{}, Deps{DataDir: t.TempDir()}); err == nil {
		t.Error("New without a runner succeeded")
	}
	bad := Config{Lists: ListsConfig{Candidates: []string{filepath.Join(t.TempDir(), "missing.txt")}}}
	if _, err := New(bad, Deps{Runner: newRunner(), DataDir: t.TempDir(), Network: NewFakeNetwork(Network{})}); err == nil {
		t.Error("New with a missing list succeeded")
	}
}

func TestNewWithRealStorePersistsDecisions(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Lists: writeLists(t)}
	newC := func() (*Checker, *fakeRunner) {
		r := newRunner()
		c, err := New(cfg, Deps{Runner: r, DataDir: dir, Clock: clock.NewFake(t0), Network: NewFakeNetwork(nA)})
		if err != nil {
			t.Fatal(err)
		}
		return c, r
	}
	c, r := newC()
	r.set(func(string) string { return "TLS_RST" })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	eventually(t, "tunnel decision", func() bool { e, _ := c.Table().Lookup("a.com"); return e.Source == route.SourceChecked })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "decisions.json")); err != nil {
		t.Fatal(err)
	}

	c2, _ := newC() // a restart sees the saved decisions before probing
	if e, _ := c2.Table().Lookup("a.com"); e.Route != route.Tunnel || e.Source != route.SourceChecked {
		t.Errorf("after restart: %+v", e)
	}
	c2.store.Close()
}

func TestCheckNowIgnoresInvalidDomains(t *testing.T) {
	x := setup(t, nA, Config{})
	x.start(t)
	x.waitRoute(t, "a.com", route.Direct, route.SourceChecked)
	x.call(t)
	x.c.CheckNow("not a domain", "http://x")
	x.noCall(t)
}
