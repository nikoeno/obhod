package pc

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/checker"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/paths"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/desync"
	"github.com/nikoejento/obhod/internal/pc/lib/clashapi"
	"github.com/nikoejento/obhod/internal/pc/monitor"
	"github.com/nikoejento/obhod/internal/pc/procman"
	"github.com/nikoejento/obhod/internal/pc/singbox"
	"github.com/nikoejento/obhod/internal/pc/status"
)

// opLog records what happened, in order, across all fakes.
type opLog struct {
	mu  sync.Mutex
	ops []string
}

func (l *opLog) add(s string) { l.mu.Lock(); l.ops = append(l.ops, s); l.mu.Unlock() }
func (l *opLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ops...)
}
func (l *opLog) index(op string) int {
	for i, o := range l.all() {
		if o == op {
			return i
		}
	}
	return -1
}
func (l *opLog) count(op string) int {
	n := 0
	for _, o := range l.all() {
		if o == op {
			n++
		}
	}
	return n
}
func (l *opLog) clear() { l.mu.Lock(); l.ops = nil; l.mu.Unlock() }

type fakeProcs struct {
	log      *opLog
	mu       sync.Mutex
	running  map[string]procman.Spec
	started  map[string]bool
	startErr map[string]error
	// restartErr fails the next Restart calls of a name, in order.
	restartErr map[string][]error
	logs       map[string][]string
	events     chan procman.Event
	onStart    func(name string) // called inside Start, with the fake locked
}

func newFakeProcs(l *opLog) *fakeProcs {
	return &fakeProcs{log: l, running: map[string]procman.Spec{}, started: map[string]bool{}, startErr: map[string]error{},
		restartErr: map[string][]error{}, logs: map[string][]string{}, events: make(chan procman.Event, 8)}
}

func (f *fakeProcs) Start(_ context.Context, s procman.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.startErr[s.Name]; err != nil {
		f.log.add("start-failed " + s.Name)
		return err
	}
	f.running[s.Name], f.started[s.Name] = s, true
	f.log.add("start " + s.Name)
	if f.onStart != nil {
		f.onStart(s.Name)
	}
	return nil
}
func (f *fakeProcs) Stop(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.started[name] {
		return procman.ErrNotFound
	}
	if _, ok := f.running[name]; ok {
		delete(f.running, name)
		f.log.add("stop " + name)
	}
	return nil
}
func (f *fakeProcs) Restart(_ context.Context, name string, s procman.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if errs := f.restartErr[name]; len(errs) > 0 {
		f.restartErr[name] = errs[1:]
		if errs[0] != nil {
			delete(f.running, name)
			f.log.add("restart-failed " + name)
			return errs[0]
		}
	}
	f.running[name], f.started[name] = s, true
	f.log.add("restart " + name)
	return nil
}
func (f *fakeProcs) StopAll(context.Context) error { return nil }
func (f *fakeProcs) Status() []procman.ProcStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []procman.ProcStatus
	for n := range f.running {
		out = append(out, procman.ProcStatus{Name: n, State: procman.StateRunning})
	}
	return out
}
func (f *fakeProcs) Events() <-chan procman.Event { return f.events }
func (f *fakeProcs) Logs(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logs[name]
}

// crash makes a running process disappear the way procman reports a give-up.
func (f *fakeProcs) crash(name string) {
	f.mu.Lock()
	delete(f.running, name)
	f.mu.Unlock()
	f.events <- procman.Event{Name: name, State: procman.StateFailed, Err: errors.New("gave up after 5 restarts")}
}
func (f *fakeProcs) CleanupStale(_ context.Context, names ...string) error {
	f.log.add("cleanup " + strings.Join(names, ","))
	return nil
}
func (f *fakeProcs) spec(name string) (procman.Spec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.running[name]
	return s, ok
}

type fakeChecker struct {
	log     *opLog
	mu      sync.Mutex
	table   route.Table
	sub     chan route.Table
	reports []checker.Failure
	runs    int
	// subscribed is set by Subscribe. Like the real checker, publish announces
	// a table only to a subscriber that exists at that moment.
	subscribed bool
}

func (f *fakeChecker) Run(ctx context.Context) error {
	f.mu.Lock()
	f.runs++
	f.mu.Unlock()
	f.log.add("checker.start")
	<-ctx.Done()
	f.log.add("checker.stop")
	return ctx.Err()
}
func (f *fakeChecker) Table() route.Table { f.mu.Lock(); defer f.mu.Unlock(); return f.table }
func (f *fakeChecker) Subscribe() (<-chan route.Table, func()) {
	f.mu.Lock()
	f.subscribed = true
	f.mu.Unlock()
	return f.sub, func() {}
}
func (f *fakeChecker) ReportFailure(x checker.Failure) {
	f.mu.Lock()
	f.reports = append(f.reports, x)
	f.mu.Unlock()
}
func (f *fakeChecker) Status() checker.Status {
	return checker.Status{Network: checker.Network{ASName: "AS64500 Example"}, Counts: map[route.Route]int{route.Tunnel: 2}, Warnings: []string{"warn"}}
}

// publish makes t the current table and announces it, as the real checker does.
func (f *fakeChecker) publish(t route.Table) {
	f.mu.Lock()
	f.table = t
	subscribed := f.subscribed
	f.mu.Unlock()
	if !subscribed {
		return
	}
	select {
	case f.sub <- t:
	default:
		select {
		case <-f.sub:
		default:
		}
		f.sub <- t
	}
}

type fakeLoop struct {
	log       *opLog
	name      string
	stat      monitor.Stats
	listenErr error
}

func (f *fakeLoop) Run(ctx context.Context) error {
	f.log.add(f.name + ".start")
	<-ctx.Done()
	f.log.add(f.name + ".stop")
	return ctx.Err()
}
func (f *fakeLoop) Stats() monitor.Stats { return f.stat }
func (f *fakeLoop) Listen() error {
	f.log.add(f.name + ".listen")
	return f.listenErr
}

// harness wires an App to fakes and a temp directory.
type harness struct {
	t       *testing.T
	log     *opLog
	fc      *clock.Fake
	procs   *fakeProcs
	chk     *fakeChecker
	mon     *fakeLoop
	app     *App
	p       paths.Paths
	d       *deps
	sink    func(monitor.Failure)
	uplink  netif.Interface
	netEv   chan netif.Event
	admin   bool
	nft     bool
	nftList string
	ipRules string // listing of `ip rule show`
	ipErr   error
	lookup  func(host string) ([]netip.Addr, error)
	status  *fakeLoop
	check   func(file string) error
	checks  int
	parent  func() bool
	resets  int
	mu      sync.Mutex
	cfg     Config
	started chan struct{}
}

func tbl(t *testing.T, es ...route.Entry) route.Table {
	t.Helper()
	tb, err := route.NewTable("net", time.Unix(0, 0), es)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func ent(domain string, r route.Route) route.Entry {
	return route.Entry{Domain: domain, Route: r, Source: route.SourceChecked}
}

const tunnelJSON = `{"type":"vless","server":"203.0.113.7","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000"}`

func newHarness(t *testing.T, initial route.Table, opts ...Option) *harness {
	t.Helper()
	h := &harness{t: t, log: &opLog{}, fc: clock.NewFake(time.Unix(1_000_000, 0)), admin: true, nft: true, ipRules: engineRuleListing,
		uplink: netif.Interface{Name: "eth0", Index: 3}, netEv: make(chan netif.Event, 4)}
	h.p = paths.FromRoot(t.TempDir())
	h.procs = newFakeProcs(h.log)
	h.chk = &fakeChecker{log: h.log, table: initial, sub: make(chan route.Table, 1)}
	h.mon = &fakeLoop{log: h.log, name: "monitor"}
	h.status = &fakeLoop{log: h.log, name: "status"}
	h.lookup = func(string) ([]netip.Addr, error) { return nil, errors.New("no such host") }
	h.cfg = DefaultConfig()
	h.cfg.Desync.Engine = desync.WinWS
	h.cfg.Desync.Strategy = []string{"--filter-l7=tls"}
	h.check = func(string) error { return nil }
	h.parent = func() bool { return true }
	h.d = &deps{
		clk:     h.fc,
		checker: h.chk,
		procs:   h.procs,
		newClash: func(base, secret string) clashapi.Client {
			return clashapi.NewFake()
		},
		newMonitor: func(_ monitor.Config, _ clashapi.Client, _ monitor.RouteOf, _ clock.Clock, sink func(monitor.Failure)) monitorRunner {
			h.mu.Lock()
			h.sink = sink
			h.mu.Unlock()
			return h.mon
		},
		newStatus: func(status.Config, status.Source, clashapi.Client) (statusRunner, error) {
			return h.status, nil
		},
		uplink:      func(context.Context) (netif.Interface, error) { h.mu.Lock(); defer h.mu.Unlock(); return h.uplink, nil },
		watchUplink: func(context.Context) (<-chan netif.Event, error) { return h.netEv, nil },
		resetDialer: func() { h.mu.Lock(); h.resets++; h.mu.Unlock() },
		// The real Ensure restricts directories to SYSTEM and Administrators on Windows,
		// which a non-elevated test run could not write to.
		ensure: func(p paths.Paths) error {
			for _, d := range []string{p.Config, p.Data, p.Logs, p.Secrets, p.Run} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					return err
				}
			}
			return nil
		},
		isAdmin: func() (bool, error) { return h.admin, nil },
		nftTable: func(context.Context) (string, bool, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.nftList, h.nft, nil
		},
		ipRuleList: func(context.Context) (string, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.ipRules, h.ipErr
		},
		lookupHost: func(_ context.Context, host string) ([]netip.Addr, error) { return h.lookup(host) },
		singCheck: func(_ context.Context, _ string, file string) error {
			h.mu.Lock()
			h.checks++
			h.mu.Unlock()
			return h.check(file)
		},
		parentUp:   func(context.Context, int) bool { return h.parent() },
		newSecret:  func() (string, error) { return "test-secret", nil },
		findBinary: func(name, _ string) (string, error) { return "/bin/" + name, nil },
		exePath:    "/bin/splitter",
	}
	all := append([]Option{withDeps(h.d), withTunnelOutbounds(singbox.Outbounds{Tunnel: json.RawMessage(tunnelJSON)})}, opts...)
	app, err := New(h.cfg, h.p, all...)
	if err != nil {
		t.Fatal(err)
	}
	h.app = app
	return h
}

// start runs the app and returns a function that cancels it and returns Run's error.
func (h *harness) start() (stop func() error) {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.app.Run(ctx) }()
	var once sync.Once
	var result error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case result = <-done:
			case <-time.After(15 * time.Second):
				h.t.Fatal("Run did not return after cancel")
			}
		})
		return result
	}
	h.t.Cleanup(func() { _ = stop() })
	return stop
}

func (h *harness) waitOp(op string) {
	h.t.Helper()
	waitUntil(h.t, "op "+op, func() bool { return h.log.index(op) >= 0 })
}

func (h *harness) waitCount(op string, n int) {
	h.t.Helper()
	waitUntil(h.t, op, func() bool { return h.log.count(op) >= n })
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// consumed waits until the reload loop has taken the published table off the channel and armed its timer.
func (h *harness) consumed() {
	h.t.Helper()
	waitUntil(h.t, "table consumed", func() bool { return len(h.chk.sub) == 0 })
	time.Sleep(30 * time.Millisecond)
}

// settle publishes a table, waits for the debounce timer, fires it, and waits for the reload to finish.
func (h *harness) publishAndFire(t route.Table) {
	h.t.Helper()
	h.chk.publish(t)
	waitUntil(h.t, "debounce timer", func() bool { return h.fc.Waiters() >= 1 })
	h.fc.Advance(reloadDebounce)
}

func (h *harness) singBoxFile() []byte {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.p.Run, singBoxFileName))
	if err != nil {
		h.t.Fatal(err)
	}
	return b
}

func (h *harness) ops() string { return strings.Join(h.log.all(), " | ") }

func baseTable(t *testing.T) route.Table {
	return tbl(t, ent("yt.example", route.Desync), ent("blocked.example", route.Tunnel), ent("plain.example", route.Direct))
}

func TestStartupOrder(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.start()
	h.waitOp("status.start")

	want := []string{"checker.start", "cleanup sing-box,winws2,nfqws2", "start winws2", "start sing-box"}
	got := h.log.all()
	var seq []string
	for _, w := range want {
		if i := h.log.index(w); i < 0 {
			t.Fatalf("missing %q in %s", w, h.ops())
		} else {
			seq = append(seq, got[i])
		}
	}
	// The desync engine is up before the TUN, the monitor and status come last.
	prev := -1
	for _, w := range want[1:] {
		i := h.log.index(w)
		if i < prev {
			t.Fatalf("%q ran too early: %s", w, h.ops())
		}
		prev = i
	}
	// The monitor and the status page start after sing-box; they run in their own
	// goroutines, so their relative order is not defined.
	h.waitOp("monitor.start")
	for _, late := range []string{"monitor.start", "status.start"} {
		if h.log.index(late) < h.log.index("start sing-box") {
			t.Fatalf("%q ran before sing-box: %s", late, h.ops())
		}
	}
	if h.app.State() != StateConnected {
		t.Fatalf("state = %s", h.app.State())
	}
	sb, _ := h.procs.spec(nameSingBox)
	if sb.Path != "/bin/sing-box" || sb.Dir != h.p.Run || sb.Ready == nil ||
		len(sb.Args) != 3 || sb.Args[2] != filepath.Join(h.p.Run, singBoxFileName) {
		t.Fatalf("sing-box spec = %+v", sb)
	}
	eng, _ := h.procs.spec("winws2")
	hostlist, err := os.ReadFile(filepath.Join(h.p.Run, "hostlist.txt"))
	if err != nil || string(hostlist) != "yt.example\n" {
		t.Fatalf("hostlist = %q, %v", hostlist, err)
	}
	if eng.Dir != h.p.Run || !contains(eng.Args, "--hostlist=hostlist.txt") {
		t.Fatalf("engine spec = %+v", eng)
	}
	// The tunnel server is protected without the user listing it.
	if !strings.Contains(strings.Join(eng.Args, " "), "ip.DstAddr == 203.0.113.7") {
		t.Fatalf("tunnel server not excluded: %q", eng.Args)
	}
	cfg := h.singBoxFile()
	for _, own := range []string{"splitter", "sing-box", "winws2"} {
		if !strings.Contains(string(cfg), `"`+own+`"`) {
			t.Errorf("own process %q missing from the sing-box config", own)
		}
	}
	if !strings.Contains(string(cfg), "test-secret") {
		t.Error("clash secret not in the generated config")
	}
	if fi, err := os.Stat(filepath.Join(h.p.Run, singBoxFileName)); err != nil {
		t.Fatal(err)
	} else if perm := fi.Mode().Perm(); perm&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("config permissions %v expose the secret", perm)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestEmptyDesyncListMeansNoEngine(t *testing.T) {
	h := newHarness(t, tbl(t, ent("a.example", route.Tunnel), ent("b.example", route.Direct)))
	h.start()
	h.waitOp("status.start")
	if h.log.index("start winws2") >= 0 || h.log.index("start nfqws2") >= 0 {
		t.Fatalf("engine started with no desync domains: %s", h.ops())
	}
	if h.log.index("start sing-box") < 0 {
		t.Fatal("sing-box not started")
	}
}

func TestUIStartsDisconnectedAndConnects(t *testing.T) {
	h := newHarness(t, baseTable(t), withUI(true))
	stop := h.start()
	h.waitOp("status.start")
	if h.app.State() != StateDisconnected {
		t.Fatalf("state = %s", h.app.State())
	}
	if h.log.index("start sing-box") >= 0 || h.log.index("monitor.start") >= 0 {
		t.Fatalf("processes started while disconnected: %s", h.ops())
	}
	ctx := context.Background()

	if err := h.app.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if h.app.State() != StateConnected || h.log.count("start sing-box") != 1 || h.log.count("start winws2") != 1 {
		t.Fatalf("after connect: %s (%s)", h.app.State(), h.ops())
	}
	if err := h.app.Connect(ctx); err != nil {
		t.Fatalf("second Connect must be a no-op: %v", err)
	}
	if h.log.count("start sing-box") != 1 {
		t.Fatal("second Connect started sing-box again")
	}

	h.log.clear()
	if err := h.app.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	// Monitor, then sing-box, then the desync engine; the checker keeps running.
	if m, s, e := h.log.index("monitor.stop"), h.log.index("stop sing-box"), h.log.index("stop winws2"); m < 0 || !(m < s && s < e) {
		t.Fatalf("disconnect order: %s", h.ops())
	}
	if h.log.index("checker.stop") >= 0 || h.app.State() != StateDisconnected {
		t.Fatalf("disconnect touched the checker or left state %s: %s", h.app.State(), h.ops())
	}

	h.log.clear()
	if err := h.app.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	h.waitOp("monitor.start")
	if h.log.count("start sing-box") != 1 || h.log.count("start winws2") != 1 || h.log.count("monitor.start") != 1 {
		t.Fatalf("reconnect: %s", h.ops())
	}
	if h.chk.runs != 1 {
		t.Fatalf("checker was started %d times", h.chk.runs)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestConnectNeedsRunningApp(t *testing.T) {
	h := newHarness(t, baseTable(t))
	if err := h.app.Connect(context.Background()); err == nil {
		t.Fatal("Connect without Run accepted")
	}
}

func TestConnectFailureRollsBack(t *testing.T) {
	h := newHarness(t, baseTable(t), withUI(true))
	h.start()
	h.waitOp("status.start")
	h.procs.startErr[nameSingBox] = errors.New("boom")
	err := h.app.Connect(context.Background())
	if err == nil || exitCode(err) != exitRuntime {
		t.Fatalf("err = %v, code %d", err, exitCode(err))
	}
	if h.app.State() != StateDisconnected {
		t.Fatalf("state = %s", h.app.State())
	}
	if h.log.index("stop winws2") < 0 {
		t.Fatalf("engine left running after a failed connect: %s", h.ops())
	}
}

func TestTableChangeWhileDisconnectedRestartsNothing(t *testing.T) {
	h := newHarness(t, baseTable(t), withUI(true))
	h.start()
	h.waitOp("status.start")
	waitUntil(t, "reload loop", func() bool { return h.fc.Waiters() == 0 })
	h.log.clear()

	h.publishAndFire(tbl(t, ent("yt.example", route.Desync), ent("new.example", route.Tunnel)))
	waitUntil(t, "sing-box.json written", func() bool {
		b, err := os.ReadFile(filepath.Join(h.p.Run, singBoxFileName))
		return err == nil && strings.Contains(string(b), "new.example")
	})
	time.Sleep(50 * time.Millisecond)
	for _, op := range h.log.all() {
		if strings.HasPrefix(op, "start") || strings.HasPrefix(op, "restart") || strings.HasPrefix(op, "stop") {
			t.Fatalf("process operation while disconnected: %s", h.ops())
		}
	}
	if h.checks == 0 {
		t.Fatal("the new config was not checked")
	}
}

func TestTunnelOnlyChangeRestartsOnlySingBox(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.start()
	h.waitOp("status.start")
	h.fc.Advance(time.Minute) // past the minimum gap between sing-box restarts
	h.log.clear()

	h.publishAndFire(tbl(t, ent("yt.example", route.Desync), ent("blocked.example", route.Tunnel),
		ent("plain.example", route.Direct), ent("another.example", route.Tunnel)))
	h.waitOp("restart sing-box")
	time.Sleep(50 * time.Millisecond)
	if got := h.ops(); got != "restart sing-box" {
		t.Fatalf("ops = %s", got)
	}
	if !strings.Contains(string(h.singBoxFile()), "another.example") {
		t.Fatal("config file not updated")
	}
}

func TestDesyncChangeRestartsEngineBeforeSingBox(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.start()
	h.waitOp("status.start")
	h.fc.Advance(time.Minute)
	h.log.clear()

	h.publishAndFire(tbl(t, ent("yt.example", route.Desync), ent("vid.example", route.Desync),
		ent("blocked.example", route.Tunnel), ent("plain.example", route.Direct)))
	h.waitOp("restart sing-box")
	e, s := h.log.index("restart winws2"), h.log.index("restart sing-box")
	if e < 0 || e > s {
		t.Fatalf("engine must be restarted before sing-box: %s", h.ops())
	}
	if b, _ := os.ReadFile(filepath.Join(h.p.Run, "hostlist.txt")); string(b) != "vid.example\nyt.example\n" {
		t.Fatalf("hostlist = %q", b)
	}
}

func TestEngineStopsAndStartsWithDesyncList(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.start()
	h.waitOp("status.start")
	h.fc.Advance(time.Minute)
	h.log.clear()

	h.publishAndFire(tbl(t, ent("blocked.example", route.Tunnel), ent("plain.example", route.Direct)))
	h.waitOp("restart sing-box") // the desync rule left the config too
	if h.log.index("stop winws2") < 0 || h.log.index("restart winws2") >= 0 {
		t.Fatalf("engine not stopped when the list became empty: %s", h.ops())
	}
	h.fc.Advance(time.Minute)
	h.log.clear()

	h.publishAndFire(baseTable(t))
	h.waitOp("restart sing-box")
	if h.log.index("start winws2") < 0 {
		t.Fatalf("engine not started when the list stopped being empty: %s", h.ops())
	}
}

func TestFailedCheckKeepsOldConfigAndProcess(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.start()
	h.waitOp("status.start")
	h.fc.Advance(time.Minute)
	before := h.singBoxFile()
	h.log.clear()
	h.check = func(string) error { return errors.New("sing-box says no") }

	h.publishAndFire(tbl(t, ent("yt.example", route.Desync), ent("blocked.example", route.Tunnel),
		ent("plain.example", route.Direct), ent("bad.example", route.Tunnel)))
	waitUntil(t, "check call", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.checks >= 2 })
	time.Sleep(50 * time.Millisecond)
	if string(h.singBoxFile()) != string(before) {
		t.Fatal("a rejected config replaced the running one")
	}
	if h.log.index("restart sing-box") >= 0 || h.log.index("stop sing-box") >= 0 {
		t.Fatalf("sing-box touched after a failed check: %s", h.ops())
	}
	if _, err := os.Stat(filepath.Join(h.p.Run, singBoxFileName+".new")); !os.IsNotExist(err) {
		t.Fatal("rejected candidate left behind")
	}
}

func TestDebounceMergesBurst(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.start()
	h.waitOp("status.start")
	h.fc.Advance(time.Minute)
	h.log.clear()

	h.chk.publish(tbl(t, ent("a.example", route.Tunnel)))
	h.consumed()
	h.fc.Advance(2 * time.Second)
	h.chk.publish(tbl(t, ent("a.example", route.Tunnel), ent("b.example", route.Tunnel)))
	h.consumed()
	h.fc.Advance(2 * time.Second) // 4 s after the first event, 2 s after the second: still waiting
	time.Sleep(50 * time.Millisecond)
	if h.log.count("restart sing-box") != 0 {
		t.Fatalf("reloaded before the burst ended: %s", h.ops())
	}
	h.chk.publish(tbl(t, ent("a.example", route.Tunnel), ent("b.example", route.Tunnel), ent("c.example", route.Tunnel)))
	h.consumed()
	h.fc.Advance(reloadDebounce)
	h.waitOp("restart sing-box")
	time.Sleep(50 * time.Millisecond)
	if n := h.log.count("restart sing-box"); n != 1 {
		t.Fatalf("%d restarts for one burst: %s", n, h.ops())
	}
	if !strings.Contains(string(h.singBoxFile()), "c.example") {
		t.Fatal("the reload did not use the latest table")
	}
}

func TestSingBoxRestartRespectsMinimumGap(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.start()
	h.waitOp("status.start")
	h.fc.Advance(10 * time.Second) // only 10 s after sing-box started
	h.log.clear()

	h.publishAndFire(tbl(t, ent("yt.example", route.Desync), ent("blocked.example", route.Tunnel),
		ent("plain.example", route.Direct), ent("late.example", route.Tunnel)))
	waitUntil(t, "retry timer", func() bool { return h.fc.Waiters() == 1 })
	time.Sleep(50 * time.Millisecond)
	if h.log.count("restart sing-box") != 0 {
		t.Fatalf("restarted inside the gap: %s", h.ops())
	}
	h.fc.Advance(singBoxMinGap)
	h.waitOp("restart sing-box")
	if !strings.Contains(string(h.singBoxFile()), "late.example") {
		t.Fatal("postponed restart used a stale config")
	}
}

func TestUplinkChangeRestartsEngineWithNewInterface(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.start()
	h.waitOp("status.start")
	h.log.clear()

	h.mu.Lock()
	h.uplink = netif.Interface{Name: "wlan0", Index: 8}
	h.mu.Unlock()
	h.netEv <- netif.Event{Kind: netif.UplinkChanged}
	waitUntil(t, "debounce timer", func() bool { return h.fc.Waiters() >= 1 })
	h.fc.Advance(reloadDebounce)
	h.waitOp("restart winws2")
	eng, _ := h.procs.spec("winws2")
	if !strings.Contains(strings.Join(eng.Args, " "), "ifIdx == 8") {
		t.Fatalf("engine args = %q", eng.Args)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.resets != 1 {
		t.Fatalf("direct dialer reset %d times", h.resets)
	}
}

func TestReverseShutdownOrder(t *testing.T) {
	h := newHarness(t, baseTable(t))
	stop := h.start()
	h.waitOp("status.start")
	h.log.clear()
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	order := []string{"status.stop", "monitor.stop", "stop sing-box", "stop winws2", "checker.stop"}
	prev := -1
	for _, op := range order {
		i := h.log.index(op)
		if i < 0 || i < prev {
			t.Fatalf("shutdown order wrong at %q: %s", op, h.ops())
		}
		prev = i
	}
}

func TestNeedsAdminExitCode(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.admin = false
	err := h.app.Run(context.Background())
	if exitCode(err) != exitManual || !strings.Contains(err.Error(), elevationHint) {
		t.Fatalf("err = %v, code %d", err, exitCode(err))
	}
	if h.log.index("checker.start") >= 0 {
		t.Fatal("something started without admin rights")
	}

	h = newHarness(t, baseTable(t))
	h.procs.startErr[nameSingBox] = procman.ErrNeedsAdmin
	err = h.app.Run(context.Background())
	if exitCode(err) != exitManual {
		t.Fatalf("err = %v, code %d", err, exitCode(err))
	}
	if h.log.index("stop winws2") < 0 || h.log.index("checker.stop") < 0 {
		t.Fatalf("failed start did not clean up: %s", h.ops())
	}
}

func TestMissingNFTTablePrintsSetupAndExits2(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.app.cfg.Desync.Engine = desync.NFQWS
	h.nft = false
	err := h.app.Run(context.Background())
	if exitCode(err) != exitManual {
		t.Fatalf("err = %v, code %d", err, exitCode(err))
	}
	for _, want := range []string{"nft add table inet splitter", "queue num 200", "nft delete table inet splitter"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q:\n%v", want, err)
		}
	}
	if h.log.index("start nfqws2") >= 0 || h.log.index("start sing-box") >= 0 {
		t.Fatalf("started despite the missing table: %s", h.ops())
	}
}

func TestMonitorFailureReachesChecker(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.start()
	h.waitOp("monitor.start")
	at := time.Unix(5, 0)
	h.mu.Lock()
	sink := h.sink
	h.mu.Unlock()
	sink(monitor.Failure{Domain: "yt.example", Route: route.Desync, Verdict: "STALL_16_20", At: at})
	h.chk.mu.Lock()
	defer h.chk.mu.Unlock()
	if len(h.chk.reports) != 1 {
		t.Fatalf("reports = %+v", h.chk.reports)
	}
	if got := h.chk.reports[0]; got.Domain != "yt.example" || got.Route != route.Desync || got.Verdict != "STALL_16_20" || !got.At.Equal(at) {
		t.Fatalf("report = %+v", got)
	}
}

func TestParentExitShutsDown(t *testing.T) {
	var alive = true
	var mu sync.Mutex
	h := newHarness(t, baseTable(t), withUI(true), withParentPID(4242))
	h.parent = func() bool { mu.Lock(); defer mu.Unlock(); return alive }
	done := make(chan error, 1)
	go func() { done <- h.app.Run(context.Background()) }()
	h.waitOp("status.start")
	waitUntil(t, "parent ticker", func() bool { return h.fc.Waiters() >= 1 })
	h.fc.Advance(parentPoll) // parent still alive
	select {
	case err := <-done:
		t.Fatalf("exited while the parent lives: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	mu.Lock()
	alive = false
	mu.Unlock()
	h.fc.Advance(parentPoll)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("did not shut down after the parent exited")
	}
	if h.log.index("checker.stop") < 0 {
		t.Fatalf("not a clean shutdown: %s", h.ops())
	}
}

func TestNoExcludeAddressIsConfigError(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.app.outbounds.Tunnel = json.RawMessage(`{"type":"vless","server":"vpn.example.org","server_port":443}`)
	err := h.app.Run(context.Background())
	if exitCode(err) != exitConfig || !strings.Contains(err.Error(), "exclude_ips") {
		t.Fatalf("err = %v, code %d", err, exitCode(err))
	}
	// An unrelated configured exclude does not protect a host-name tunnel
	// that did not resolve.
	h = newHarness(t, baseTable(t))
	h.app.outbounds.Tunnel = json.RawMessage(`{"type":"vless","server":"vpn.example.org","server_port":443}`)
	h.app.cfg.Desync.ExcludeIPs = []netip.Prefix{netip.MustParsePrefix("198.51.100.5/32")}
	err = h.app.Run(context.Background())
	if exitCode(err) != exitConfig || !strings.Contains(err.Error(), "could not be resolved") {
		t.Fatalf("unresolved host-name tunnel: err = %v, code %d", err, exitCode(err))
	}
	if h.log.index("start winws2") >= 0 || h.log.index("start sing-box") >= 0 {
		t.Fatalf("started without protecting the tunnel: %s", h.ops())
	}
}

func TestStatusSourceSnapshot(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.start()
	h.waitOp("status.start")
	h.mon.stat = monitor.Stats{Bytes: map[route.Route]int64{route.Tunnel: 100, route.Direct: 250, route.Desync: 150}}
	h.fc.Advance(time.Minute)
	h.publishAndFire(tbl(t, ent("yt.example", route.Desync), ent("blocked.example", route.Direct), ent("plain.example", route.Direct)))
	h.waitOp("restart sing-box")

	snap := (&statusSource{a: h.app}).Snapshot()
	if snap.Network != "AS64500 Example" || snap.Routes[route.Tunnel] != 2 || len(snap.Warnings) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.Savings != 0.8 || snap.Traffic[route.Direct] != 250 {
		t.Fatalf("savings %v traffic %v", snap.Savings, snap.Traffic)
	}
	if len(snap.Processes) != 2 {
		t.Fatalf("processes = %+v", snap.Processes)
	}
	if len(snap.Recent) != 1 || snap.Recent[0].Domain != "blocked.example" || snap.Recent[0].From != route.Tunnel || snap.Recent[0].To != route.Direct {
		t.Fatalf("recent = %+v", snap.Recent)
	}
}

func TestRouteByTag(t *testing.T) {
	for chain, want := range map[string]route.Route{"direct": route.Direct, "desync": route.Desync, "tunnel": route.Tunnel, "proxy": route.Proxy, "weird": route.Direct} {
		if got := routeByTag(clashapi.Connection{Chains: []string{"first", chain}}); got != want {
			t.Errorf("%s → %s, want %s", chain, got, want)
		}
	}
	if routeByTag(clashapi.Connection{}) != route.Direct {
		t.Error("empty chain")
	}
}

func TestExitCodes(t *testing.T) {
	cases := map[string]struct {
		err  error
		want int
	}{
		"nil":         {nil, exitOK},
		"config":      {fail(exitConfig, "x"), exitConfig},
		"wrapped":     {errors.Join(errors.New("a"), fail(exitManual, "x")), exitManual},
		"needs admin": {procman.ErrNeedsAdmin, exitManual},
		"other":       {errors.New("boom"), exitRuntime},
	}
	for name, c := range cases {
		if got := exitCode(c.err); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}

func TestWriteAtomicReplaces(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x.json")
	for _, content := range []string{"one", "two"} {
		if err := writeAtomic(f, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(f); string(b) != "two" {
		t.Fatalf("content = %q", b)
	}
	entries, _ := os.ReadDir(filepath.Dir(f))
	if len(entries) != 1 {
		t.Fatalf("temp files left: %v", entries)
	}
}
