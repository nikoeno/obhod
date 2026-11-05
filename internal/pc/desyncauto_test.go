package pc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/autodesync"
	"github.com/nikoejento/obhod/internal/pc/desync"
	"github.com/nikoejento/obhod/internal/pc/monitor"
	"github.com/nikoejento/obhod/internal/pc/singbox"
)

// What these tests cover: the app's side of the automatic desync search (selection, the trial state, the
// effective table, persistence and reuse, cancellation, events, live failures) against a fake engine
// (fakeProcs), a scripted probe and a fake clock. What they do NOT cover: a real winws2/nfqws2 (the project
// rules forbid running them), a real direct probe through a mangled path, and real timing.

// sleepClock is the app clock of the search tests: Sleep advances the fake time at once and records the pause.
type sleepClock struct {
	*clock.Fake
	mu     sync.Mutex
	sleeps []time.Duration
}

func (c *sleepClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
	c.Advance(d)
	return ctx.Err()
}

func (c *sleepClock) pauses() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// autoCfg makes the app config desync.preset auto (and lets a test tune the search).
func autoCfg(mut func(*Config)) Option {
	return func(a *App) {
		a.cfg.Desync.Preset, a.cfg.Desync.Strategy = desync.PresetAuto, nil
		if mut != nil {
			mut(&a.cfg)
		}
	}
}

// presetOpt makes the app config a named preset.
func presetOpt(id string) Option {
	return func(a *App) { a.cfg.Desync.Preset, a.cfg.Desync.Strategy = id, nil }
}

// autoHarness is a harness whose probe is scripted per active preset.
type autoHarness struct {
	*harness
	clk *sleepClock

	mu       sync.Mutex
	fixes    map[string][]string // preset id -> sites reached with it
	probes   [][]string          // sites of every probe call
	seen     []string            // the preset the engine ran with at every probe call
	hostlist []string            // the hostlist file at every probe call
	hook     func(n int, preset string) error
	prints   map[string]string // engine arguments -> preset id
}

func newAutoHarness(t *testing.T, initial route.Table, opts ...Option) *autoHarness {
	t.Helper()
	h := newHarness(t, initial, opts...)
	ah := &autoHarness{harness: h, fixes: map[string][]string{}, prints: map[string]string{}}
	ah.clk = &sleepClock{Fake: h.fc}
	h.d.clk = ah.clk
	h.d.trialProbe = ah.probe
	// Fingerprint every preset's engine arguments (they do not depend on the hostlist content).
	cfg := h.app.cfg.Desync
	cfg.Engine, cfg.Binary = desync.WinWS, "/bin/winws2"
	cfg.ExcludeIPs = excludeIPs(nil, h.app.outbounds.Tunnel)
	if len(cfg.ExcludeIPs) == 0 {
		cfg.ExcludeIPs = []netip.Prefix{noTunnelExclude}
	}
	one := tbl(t, ent("fp.example", route.Desync))
	for _, id := range desync.PresetIDs() {
		c := cfg
		c.Preset, c.Strategy, c.Ports = id, nil, nil
		plan, err := desync.Build(c, h.uplink, one)
		if err != nil {
			t.Fatal(err)
		}
		if prev, dup := ah.prints[strings.Join(plan.Args, "\x00")]; dup && prev != "alt11-fake-multisplit" {
			t.Logf("presets %s and %s render the same arguments", prev, id)
		}
		ah.prints[strings.Join(plan.Args, "\x00")] = id
	}
	return ah
}

// enginePreset is the preset the fake engine currently runs with ("" when it is not running).
func (ah *autoHarness) enginePreset() string {
	spec, ok := ah.procs.spec("winws2")
	if !ok {
		return ""
	}
	return ah.prints[strings.Join(spec.Args, "\x00")]
}

func (ah *autoHarness) probe(ctx context.Context, domains []string) (map[string]bool, error) {
	preset := ah.enginePreset()
	hl, _ := os.ReadFile(filepath.Join(ah.p.Run, "hostlist.txt"))
	ah.mu.Lock()
	ah.probes = append(ah.probes, append([]string(nil), domains...))
	ah.seen = append(ah.seen, preset)
	ah.hostlist = append(ah.hostlist, string(hl))
	n := len(ah.probes)
	hook := ah.hook
	out := map[string]bool{}
	for _, d := range ah.fixes[preset] {
		out[d] = true
	}
	ah.mu.Unlock()
	if hook != nil {
		if err := hook(n, preset); err != nil {
			return nil, err
		}
	}
	return out, ctx.Err()
}

func (ah *autoHarness) probeCount() int {
	ah.mu.Lock()
	defer ah.mu.Unlock()
	return len(ah.probes)
}

func (ah *autoHarness) presetsSeen() []string {
	ah.mu.Lock()
	defer ah.mu.Unlock()
	return append([]string(nil), ah.seen...)
}

func (ah *autoHarness) waitSearchDone(n int) {
	ah.t.Helper()
	waitUntil(ah.t, "search ended", func() bool {
		d := ah.app.Desync()
		return !d.Searching && ah.probeCount() >= n
	})
}

// waitEngine waits for the fake engine to run with the preset (the final state after a search).
func (ah *autoHarness) waitEngine(preset string) {
	ah.t.Helper()
	waitUntil(ah.t, "engine preset "+preset, func() bool { return ah.enginePreset() == preset })
}

func discordTable(t *testing.T) route.Table {
	return tbl(t, ent("discord.com", route.Desync), ent("plain.example", route.Direct), ent("blocked.example", route.Tunnel))
}

func routeOf(a *App, domain string) RouteInfo {
	for _, r := range a.Routes() {
		if r.Domain == domain {
			return r
		}
	}
	return RouteInfo{}
}

func TestAutoSearchFindsSecondPresetPersistsAndReusesIt(t *testing.T) {
	ah := newAutoHarness(t, discordTable(t), autoCfg(nil))
	ah.fixes["alt3-hostfakesplit"] = []string{"discord.com"}
	ev, unsub := ah.app.Subscribe()
	defer unsub()
	ah.start()
	ah.waitRunning()

	// Connect did not wait for the search: the engine first ran with the default preset.
	if first := ah.log.index("start winws2"); first < 0 {
		t.Fatalf("the engine never started: %s", ah.ops())
	}
	ah.waitSearchDone(2)
	ah.waitEngine("alt3-hostfakesplit")

	if got := ah.presetsSeen(); len(got) != 2 || got[0] != "alt2-multisplit" || got[1] != "alt3-hostfakesplit" {
		t.Fatalf("presets tried: %v, want the priority order and an early stop at the first that fixes all", got)
	}
	for _, hl := range ah.hostlist {
		if hl != "discord.com\n" {
			t.Fatalf("trial hostlist %q, want exactly the failing domain", hl)
		}
	}
	d := ah.app.Desync()
	if d.Mode != DesyncModeAuto || d.Preset != "alt3-hostfakesplit" || d.LastSearch == nil || d.LastSearch.Chosen != "alt3-hostfakesplit" ||
		d.LastSearch.Outcome != "chosen" || len(d.LastSearch.Tried) != 2 || d.LastSearch.Tried[0] != (DesyncTry{"alt2-multisplit", 0, 1}) ||
		d.LastSearch.Tried[1] != (DesyncTry{"alt3-hostfakesplit", 1, 1}) {
		t.Fatalf("status %+v / %+v", d, d.LastSearch)
	}
	if r := routeOf(ah.app, "discord.com"); r.Route != route.Desync || r.Desync != DesyncVerified {
		t.Fatalf("route %+v", r)
	}
	// Persisted, private, and holding the network id and the site, never an address.
	b, err := os.ReadFile(filepath.Join(ah.p.Data, autodesync.FileName))
	if err != nil || !strings.Contains(string(b), `"alt3-hostfakesplit"`) || !strings.Contains(string(b), `"net"`) || !strings.Contains(string(b), "discord.com") {
		t.Fatalf("%s, %v", b, err)
	}
	// Events: searching, trying 1 of N, trying 2 of N, done; no site name anywhere.
	var phases []string
	var raw []string
collect:
	for {
		select {
		case e := <-ev:
			if e.Kind == EventDesync {
				phases = append(phases, e.Desync.Phase)
				raw = append(raw, fmt.Sprintf("%+v", e.Desync))
			}
		case <-time.After(50 * time.Millisecond):
			break collect
		}
	}
	if strings.Join(phases, ",") != "searching,trying,trying,done" || strings.Contains(strings.Join(raw, " "), "discord") {
		t.Fatalf("events %v", raw)
	}

	// A reconnect on the same network reuses the winner: no search, the plan already has it.
	before := ah.probeCount()
	if err := ah.app.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	ah.log.clear()
	if err := ah.app.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ah.enginePreset() != "alt3-hostfakesplit" {
		t.Fatalf("the reconnect started the engine with %q", ah.enginePreset())
	}
	time.Sleep(50 * time.Millisecond)
	if ah.probeCount() != before || ah.app.Desync().Searching {
		t.Fatalf("a search ran on reconnect (%d probes, was %d)", ah.probeCount(), before)
	}
}

func TestAutoSearchNothingFixedKeepsDefaultAndRoutesToTunnel(t *testing.T) {
	ah := newAutoHarness(t, discordTable(t), withTunnelOutbounds(singbox.Outbounds{}), autoCfg(func(c *Config) { c.DesyncAuto.MaxPresets = 3 }))
	ah.start()
	ah.waitRunning()
	ah.waitSearchDone(3)
	waitUntil(t, "failed route", func() bool { return routeOf(ah.app, "discord.com").Desync == DesyncFailed })
	waitUntil(t, "engine stopped", func() bool { return ah.enginePreset() == "" })

	r := routeOf(ah.app, "discord.com")
	if r.Route != route.Tunnel || !r.Degraded || r.Source != route.SourceChecked {
		t.Fatalf("route %+v: want tunnel, degraded (no detour server), source checked", r)
	}
	d := ah.app.Desync()
	if d.LastSearch == nil || d.LastSearch.Outcome != "none" || d.LastSearch.Chosen != "" || len(d.LastSearch.Tried) != 3 {
		t.Fatalf("%+v", d.LastSearch)
	}
	if d.Preset != desync.DefaultPreset {
		t.Fatalf("the default preset must stay: %q", d.Preset)
	}
	if got := ah.presetsSeen(); got[0] != "alt2-multisplit" || got[1] != "alt3-hostfakesplit" || got[2] != "alt-fakedsplit" {
		t.Fatalf("order %v", got)
	}
	// No second search by itself: the failed site stays failed until its TTL ends.
	ah.app.kickReload()
	time.Sleep(100 * time.Millisecond)
	if ah.probeCount() != 3 {
		t.Fatalf("%d probes: a failed site was searched again at once", ah.probeCount())
	}
}

func TestFailedSiteIsSearchedAgainAfterItsTTL(t *testing.T) {
	ah := newAutoHarness(t, discordTable(t), autoCfg(func(c *Config) {
		c.DesyncAuto.MaxPresets = 1
		c.DesyncAuto.FailedTTL = time.Hour
		c.DesyncAuto.Backoff = time.Minute
	}))
	ah.start()
	ah.waitRunning()
	ah.waitSearchDone(1)
	waitUntil(t, "failed", func() bool { return routeOf(ah.app, "discord.com").Desync == DesyncFailed })
	ah.fixes["alt2-multisplit"] = []string{"discord.com"}
	ah.fc.Advance(2 * time.Hour)
	ah.app.setTable(ah.chk.Table())
	if routeOf(ah.app, "discord.com").Desync != DesyncPending {
		t.Fatalf("after the TTL the site must be pending, got %+v", routeOf(ah.app, "discord.com"))
	}
	ah.app.kickReload()
	ah.waitSearchDone(2)
	waitUntil(t, "verified", func() bool { return routeOf(ah.app, "discord.com").Desync == DesyncVerified })
}

func TestManualPresetNeverSearches(t *testing.T) {
	ah := newAutoHarness(t, discordTable(t), presetOpt("alt3-hostfakesplit"))
	ah.start()
	ah.waitRunning()
	time.Sleep(100 * time.Millisecond)
	if ah.probeCount() != 0 || ah.app.Desync().Searching {
		t.Fatal("a named preset triggered a search")
	}
	if ah.enginePreset() != "alt3-hostfakesplit" {
		t.Fatalf("engine runs %q", ah.enginePreset())
	}
	d := ah.app.Desync()
	if d.Mode != DesyncModePreset || d.Preset != "alt3-hostfakesplit" {
		t.Fatalf("%+v", d)
	}
	if r := routeOf(ah.app, "discord.com"); r.Route != route.Desync || r.Desync != "" {
		t.Fatalf("route %+v: manual mode leaves the table alone", r)
	}
	if err := ah.app.StartDesyncSearch(); !errors.Is(err, ErrDesyncNotAuto) {
		t.Fatalf("explicit search with a named preset: %v", err)
	}
}

func TestSearchIsCancelledByDisconnectAndStoresNothing(t *testing.T) {
	ah := newAutoHarness(t, discordTable(t), autoCfg(nil))
	entered := make(chan struct{})
	var once sync.Once
	// The probe is in flight until its context ends.
	ah.d.trialProbe = func(ctx context.Context, d []string) (map[string]bool, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ah.start()
	ah.waitRunning()
	<-entered
	if !ah.app.Desync().Searching {
		t.Fatal("not searching")
	}
	if err := ah.app.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ah.app.Desync().Searching {
		t.Fatal("the search survived Disconnect")
	}
	if ah.app.State() != StateDisconnected || ah.enginePreset() != "" {
		t.Fatalf("state %s, engine %q", ah.app.State(), ah.enginePreset())
	}
	if _, err := os.Stat(filepath.Join(ah.p.Data, autodesync.FileName)); err == nil {
		t.Fatal("a cancelled search stored a result")
	}
	if d := ah.app.Desync(); d.LastSearch != nil {
		t.Fatalf("a cancelled search is no result: %+v", d.LastSearch)
	}
	ah.app.dsMu.Lock()
	trial := ah.app.dsTrial
	ah.app.dsMu.Unlock()
	if trial != nil {
		t.Fatal("the trial state was left behind")
	}
}

func TestShutdownEndsARunningSearch(t *testing.T) {
	ah := newAutoHarness(t, discordTable(t), autoCfg(nil))
	entered := make(chan struct{})
	var once sync.Once
	ah.d.trialProbe = func(ctx context.Context, d []string) (map[string]bool, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	stop := ah.start()
	ah.waitRunning()
	<-entered
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ah.app.Desync().Searching {
		t.Fatal("search still running after shutdown")
	}
}

func TestSearchCapsAndPolitenessWithFakeClock(t *testing.T) {
	ah := newAutoHarness(t, discordTable(t), autoCfg(func(c *Config) {
		c.DesyncAuto.MaxPresets = 4
		c.DesyncAuto.Settle = 7 * time.Second
		c.DesyncAuto.Pause = [2]time.Duration{2 * time.Second, 5 * time.Second}
	}))
	ah.start()
	ah.waitRunning()
	ah.waitSearchDone(4)
	if n := ah.probeCount(); n != 4 {
		t.Fatalf("%d presets tried, want the cap of 4", n)
	}
	settles, pauses := 0, 0
	for _, d := range ah.clk.pauses() {
		switch {
		case d == 7*time.Second:
			settles++
		case d >= 2*time.Second && d <= 5*time.Second:
			pauses++
		default:
			t.Errorf("unexpected sleep %v", d)
		}
	}
	if settles != 4 || pauses != 3 {
		t.Fatalf("%d settles, %d pauses between presets: %v", settles, pauses, ah.clk.pauses())
	}
	// The probe is the polite one: <= 2 at once, one per address, jitter, a bounded timeout.
	c := trialAnalyzerConfig(analyzer.DefaultConfig(), ah.app.cfg.DesyncAuto)
	if c.Concurrency > 2 || c.PerIPConcurrency != 1 || c.Jitter[1] < 900*time.Millisecond || len(c.CanaryDomains) != 0 || c.Options.SNISwap ||
		c.Options.Repeats != 1 || c.Options.Timeouts.TLS > 8*time.Second || c.Options.Timeouts.Bulk > 8*time.Second {
		t.Fatalf("%+v", c)
	}
}

func TestTrialAnalyzerConfigClamps(t *testing.T) {
	base := analyzer.DefaultConfig()
	base.Concurrency = 64
	base.Jitter = [2]time.Duration{}
	got := trialAnalyzerConfig(base, autodesync.Config{Concurrency: 2, ProbeTimeout: 3 * time.Second})
	if got.Concurrency != 2 || got.Jitter[0] < 300*time.Millisecond || got.Options.Timeouts.TCP != 3*time.Second {
		t.Fatalf("%+v", got)
	}
	// Even an invalid value cannot lift the cap.
	if got := trialAnalyzerConfig(base, autodesync.Config{Concurrency: 9}); got.Concurrency != 2 {
		t.Fatalf("concurrency %d", got.Concurrency)
	}
	low := analyzer.DefaultConfig()
	low.Concurrency = 1
	if got := trialAnalyzerConfig(low, autodesync.Config{}); got.Concurrency != 1 {
		t.Fatalf("concurrency %d", got.Concurrency)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("the derived analyzer config must validate: %v", err)
	}
}

func TestReloadDuringASearchKeepsTheTrial(t *testing.T) {
	ah := newAutoHarness(t, discordTable(t), autoCfg(func(c *Config) { c.DesyncAuto.MaxPresets = 1 }))
	more := tbl(t, ent("discord.com", route.Desync), ent("other.example", route.Desync))
	ah.hook = func(n int, preset string) error {
		if n > 1 {
			return nil
		}
		ah.chk.mu.Lock()
		ah.chk.table = more
		ah.chk.mu.Unlock()
		before, _ := ah.procs.spec("winws2")
		ah.app.reload(context.Background()) // a reload in the middle of a measurement
		after, _ := ah.procs.spec("winws2")
		hl, _ := os.ReadFile(filepath.Join(ah.p.Run, "hostlist.txt"))
		if strings.Join(before.Args, " ") != strings.Join(after.Args, " ") || string(hl) != "discord.com\n" {
			t.Errorf("a reload disturbed the trial: hostlist %q", hl)
		}
		return nil
	}
	ah.start()
	ah.waitRunning()
	ah.waitSearchDone(1)
}

func TestMissingPayloadSkipsAPresetInTheSearch(t *testing.T) {
	ah := newAutoHarness(t, discordTable(t), autoCfg(func(c *Config) { c.DesyncAuto.MaxPresets = 2 }))
	// The default preset needs only the Google payloads; alt3 and alt-fakedsplit also need max_ru.
	ah.d.fileExists = func(p string) bool { return !strings.Contains(p, "max_ru") }
	ah.fixes["general-multisplit"] = []string{"discord.com"}
	ah.start()
	ah.waitRunning()
	ah.waitSearchDone(2)
	if got := ah.presetsSeen(); len(got) != 2 || got[0] != "alt2-multisplit" || got[1] != "general-multisplit" {
		t.Fatalf("tried %v: presets whose payload is missing are skipped and do not count against the cap", got)
	}
	if d := ah.app.Desync(); d.LastSearch == nil || d.LastSearch.Chosen != "general-multisplit" || len(d.LastSearch.Tried) != 2 {
		t.Fatalf("%+v", d.LastSearch)
	}
}

func TestLiveFailuresSendAVerifiedSiteBackToASearch(t *testing.T) {
	ah := newAutoHarness(t, discordTable(t), autoCfg(func(c *Config) { c.DesyncAuto.Backoff = time.Hour }))
	ah.fixes["alt3-hostfakesplit"] = []string{"discord.com"}
	ah.start()
	ah.waitRunning()
	ah.waitSearchDone(2)
	waitUntil(t, "verified", func() bool { return routeOf(ah.app, "discord.com").Desync == DesyncVerified })
	ah.waitEngine("alt3-hostfakesplit")
	ah.mu.Lock()
	ah.seen, ah.probes = nil, nil
	ah.mu.Unlock()

	ah.harness.mu.Lock()
	sink := ah.sink
	ah.harness.mu.Unlock()
	if sink == nil {
		t.Fatal("no monitor sink")
	}
	fail := func() {
		sink(monitor.Failure{Domain: "cdn.discord.com", Route: route.Desync, Verdict: "STALL_16_20", At: ah.fc.Now()})
	}
	fail()
	fail()
	time.Sleep(50 * time.Millisecond)
	if ah.probeCount() != 0 {
		t.Fatal("two failures are below the checker's threshold of three")
	}
	fail() // the third fires
	ah.waitSearchDone(1)
	if got := ah.presetsSeen(); got[0] != "alt3-hostfakesplit" {
		t.Fatalf("a re-search starts with the stored preset: %v", got)
	}
	if routeOf(ah.app, "discord.com").Desync != DesyncVerified {
		t.Fatalf("%+v", routeOf(ah.app, "discord.com"))
	}
	// The failures were still reported to the checker.
	if len(ah.chk.reports) != 3 {
		t.Fatalf("%d reports", len(ah.chk.reports))
	}
}

func TestUserSearchPreconditions(t *testing.T) {
	ah := newAutoHarness(t, tbl(t, ent("plain.example", route.Direct)), autoCfg(nil), WithUI(true))
	if err := ah.app.StartDesyncSearch(); !errors.Is(err, ErrDesyncBusy) {
		t.Fatalf("not connected: %v", err)
	}
	ah.start()
	ah.waitRunning()
	if err := ah.app.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ah.app.StartDesyncSearch(); !errors.Is(err, ErrDesyncNoDomain) {
		t.Fatalf("no desync site: %v", err)
	}
	ah.chk.publish(discordTable(t))
	waitUntil(t, "table", func() bool { return len(ah.app.Routes()) == 3 })
	block := make(chan struct{})
	ah.d.trialProbe = func(ctx context.Context, d []string) (map[string]bool, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return nil, errors.New("stop")
	}
	ah.app.setTable(ah.chk.Table())
	if err := ah.app.StartDesyncSearch(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "searching", func() bool { return ah.app.Desync().Searching })
	if err := ah.app.StartDesyncSearch(); !errors.Is(err, ErrDesyncBusy) {
		t.Fatalf("second search: %v", err)
	}
	close(block)
	waitUntil(t, "ended", func() bool { return !ah.app.Desync().Searching })
}

func TestSetDesyncValidatesStoresAndSwitches(t *testing.T) {
	ah := newAutoHarness(t, discordTable(t), presetOpt("alt2-multisplit"))
	ctx := context.Background()
	if err := ah.app.SetDesync(ctx, "nope"); !errors.Is(err, ErrDesyncPreset) {
		t.Fatalf("unknown: %v", err)
	}
	if err := ah.app.SetDesync(ctx, "custom"); !errors.Is(err, ErrDesyncPreset) {
		t.Fatalf("custom without a strategy: %v", err)
	}
	ah.start()
	ah.waitRunning()
	ah.waitEngine("alt2-multisplit")

	// Manual id: applied through the reload path (the engine restarts with the new preset).
	if err := ah.app.SetDesync(ctx, "alt-fakedsplit"); err != nil {
		t.Fatal(err)
	}
	ah.waitEngine("alt-fakedsplit")
	if d := ah.app.Desync(); d.Mode != DesyncModePreset || d.Preset != "alt-fakedsplit" {
		t.Fatalf("%+v", d)
	}
	if ah.probeCount() != 0 {
		t.Fatal("a manual choice searched")
	}
	// The choice is stored and wins over app.yaml at the next start.
	b, err := os.ReadFile(filepath.Join(ah.p.Data, autodesync.FileName))
	if err != nil || !strings.Contains(string(b), `"selected": "alt-fakedsplit"`) {
		t.Fatalf("%s %v", b, err)
	}
	app2, err := New(ah.app.cfg, ah.p, withDeps(ah.d), withTunnelOutbounds(singbox.Outbounds{Tunnel: json.RawMessage(tunnelJSON)}))
	if err != nil {
		t.Fatal(err)
	}
	if d := app2.Desync(); d.Mode != DesyncModePreset || d.Preset != "alt-fakedsplit" {
		t.Fatalf("restart: %+v", d)
	}

	// Switching to auto while connected starts the search for the pending site.
	ah.fixes["alt3-hostfakesplit"] = []string{"discord.com"}
	if err := ah.app.SetDesync(ctx, "auto"); err != nil {
		t.Fatal(err)
	}
	ah.waitSearchDone(2)
	ah.waitEngine("alt3-hostfakesplit")
	if d := ah.app.Desync(); d.Mode != DesyncModeAuto || d.Preset != "alt3-hostfakesplit" {
		t.Fatalf("%+v", d)
	}
	// Back to a named preset: the failed/verified bookkeeping stops applying, no search.
	n := ah.probeCount()
	if err := ah.app.SetDesync(ctx, "alt2-multisplit"); err != nil {
		t.Fatal(err)
	}
	ah.waitEngine("alt2-multisplit")
	if ah.probeCount() != n {
		t.Fatal("searched after a manual choice")
	}
	if r := routeOf(ah.app, "discord.com"); r.Desync != "" {
		t.Fatalf("%+v", r)
	}
}

func TestSetDesyncManualPresetNeedsItsFiles(t *testing.T) {
	ah := newAutoHarness(t, discordTable(t), presetOpt("alt2-multisplit"))
	ah.d.fileExists = func(string) bool { return false }
	err := ah.app.SetDesync(context.Background(), "alt3-hostfakesplit")
	if oe := Classify(err); err == nil || oe.Code != CodeProgramMissing {
		t.Fatalf("err %v → %+v", err, oe)
	}
	if ah.app.Desync().Preset != "alt2-multisplit" {
		t.Fatal("a refused choice was stored")
	}
	// auto does not need the files at choice time (the search skips what is missing).
	if err := ah.app.SetDesync(context.Background(), "auto"); err != nil {
		t.Fatal(err)
	}
}

func TestEffectiveTableLeavesUserAndStaticEntriesAlone(t *testing.T) {
	ah := newAutoHarness(t, tbl(t), autoCfg(nil))
	st, err := autodesync.OpenStore(ah.p.Data, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := ah.fc.Now()
	if err := st.Put("net", autodesync.Record{Preset: "alt3-hostfakesplit", At: now, Failed: map[string]time.Time{"a.example": now, "b.example": now, "c.example": now}}); err != nil {
		t.Fatal(err)
	}
	raw := tbl(t,
		route.Entry{Domain: "a.example", Route: route.Desync, Source: route.SourceChecked},
		route.Entry{Domain: "b.example", Route: route.Desync, Source: route.SourceUser},
		route.Entry{Domain: "c.example", Route: route.Desync, Source: route.SourceStatic})
	eff, states := ah.app.effectiveTable(raw)
	get := func(d string) route.Route { e, _ := eff.Lookup(d); return e.Route }
	if get("a.example") != route.Tunnel || get("b.example") != route.Desync || get("c.example") != route.Desync {
		t.Fatalf("%+v", eff.Entries)
	}
	if states["a.example"] != DesyncFailed || states["b.example"] != "" {
		t.Fatalf("%v", states)
	}
	// An unknown network is left alone.
	raw.NetworkID = ""
	if eff, _ := ah.app.effectiveTable(raw); !eff.Equal(raw) {
		t.Fatal("a table without a network id was rewritten")
	}
}

func TestSearchOrder(t *testing.T) {
	want := map[string]bool{}
	for _, id := range desync.PresetIDs() {
		want[id] = true
	}
	seen := map[string]bool{}
	for _, id := range presetPriority {
		if !want[id] || seen[id] {
			t.Errorf("%q is not a catalogue id or is listed twice", id)
		}
		seen[id] = true
	}
	for id := range want {
		if !seen[id] && id != "alt12-fake-multisplit-google" {
			t.Errorf("catalogue preset %q is missing from presetPriority", id)
		}
	}
	if presetPriority[0] != desync.DefaultPreset || presetPriority[1] != "alt3-hostfakesplit" || presetPriority[2] != "alt-fakedsplit" {
		t.Errorf("order starts %v", presetPriority[:3])
	}
	o := searchOrder("alt7-multisplit-sniext")
	if o[0] != "alt7-multisplit-sniext" || len(o) != len(presetPriority) {
		t.Errorf("stored winner first and not twice: %v", o[:4])
	}
	if o := searchOrder("not-a-preset"); len(o) != len(presetPriority) || o[0] != presetPriority[0] {
		t.Errorf("unknown stored id: %v", o[:3])
	}
}

// fakeRunner is a scripted analyzer.Runner.
type fakeRunner struct {
	rep analyzer.Report
	err error
}

func (f fakeRunner) Run(context.Context, analyzer.Batch) (analyzer.Report, error) {
	return f.rep, f.err
}

func TestDirectProbeMapsVerdicts(t *testing.T) {
	res := func(d, v string) analyzer.ProbeResult {
		return analyzer.ProbeResult{Target: analyzer.Target{Domain: d}, Verdict: analyzer.Verdict(v)}
	}
	rep := analyzer.Report{Calibration: analyzer.Calibration{Internet: analyzer.CheckOK},
		Results: []analyzer.ProbeResult{res("a.example", "OK"), res("b.example", "OK_REDIRECT"), res("c.example", "TLS_RST"), res("d.example", "HTTP_RST")}}
	got, err := directProbe(fakeRunner{rep: rep}, nil)(context.Background(), []string{"a.example"})
	if err != nil || !got["a.example"] || !got["b.example"] || got["c.example"] || got["d.example"] {
		t.Fatalf("%v %v", got, err)
	}
	rep.Calibration.Internet = analyzer.CheckFailed
	if _, err := directProbe(fakeRunner{rep: rep}, nil)(context.Background(), nil); !errors.Is(err, autodesync.ErrNoInternet) {
		t.Fatalf("%v", err)
	}
	boom := errors.New("cancelled")
	if _, err := directProbe(fakeRunner{err: boom}, nil)(context.Background(), nil); !errors.Is(err, boom) {
		t.Fatalf("%v", err)
	}
}

func TestDesyncPresetsListsTheCatalogue(t *testing.T) {
	ah := newAutoHarness(t, discordTable(t))
	ps := ah.app.DesyncPresets()
	if len(ps) != len(desync.PresetIDs()) || ps[0].ID != desync.PresetIDs()[0] || ps[0].Name == "" || ps[0].Description == "" {
		t.Fatalf("%+v", ps[0])
	}
}
