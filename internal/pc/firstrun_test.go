package pc

// First-run regression tests (see "First-run regression tests" in api/desktop/API.md).
//
// They run the App on the REAL default config that `splitter run --ui` writes on a fresh machine (the
// text is taken from api/desktop, not copied), with the REAL checker (lists, store, scheduler, decisions).
// Only the things that need admin rights or the network are fake: sing-box, the desync engine, the
// status server and the prober (a counting runner). The real-OS variant also uses the REAL netwatch and
// netif.Uplink of the machine that runs the tests.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/checker"
	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/paths"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/desync"
	"github.com/nikoejento/obhod/internal/pc/status"
)

// writeRealDefaultConfig writes the app.yaml and the starter lists that `run --ui` creates on the first
// start, taken from the sources in api/desktop, and returns the loaded config. The test fails if the
// sources cannot be read or no longer have the expected shape.
func writeRealDefaultConfig(t *testing.T, p paths.Paths) Config {
	t.Helper()
	src := filepath.Join("..", "..", "api", "desktop")
	code, err := os.ReadFile(filepath.Join(src, "defaultconfig.go"))
	if err != nil {
		t.Fatalf("the default config source is gone: %v", err)
	}
	m := regexp.MustCompile("(?s)const defaultConfigYAML = `(.*?)`").FindSubmatch(code)
	if m == nil || !bytes.Contains(m[1], []byte("checker:")) {
		t.Fatal("defaultConfigYAML not found in api/desktop/defaultconfig.go (did it move? update this helper)")
	}
	if err := os.MkdirAll(filepath.Join(p.Config, "lists"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"candidates.txt", "proxy.txt", "block.txt"} {
		data, err := os.ReadFile(filepath.Join(src, "defaultlists", n))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p.Config, "lists", n), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(p.Config, "app.yaml")
	if err := os.WriteFile(file, m[1], 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(file)
	if err != nil {
		t.Fatalf("the default config does not load: %v", err)
	}
	return cfg
}

// countingRunner is a prober that never touches the network: every target is reachable directly. It
// records the size of each batch.
type countingRunner struct {
	mu      sync.Mutex
	batches []int
}

func (r *countingRunner) Run(_ context.Context, b analyzer.Batch) (analyzer.Report, error) {
	r.mu.Lock()
	r.batches = append(r.batches, len(b.Targets))
	r.mu.Unlock()
	rep := analyzer.Report{Calibration: analyzer.Calibration{Internet: analyzer.CheckOK}}
	for _, t := range b.Targets {
		rep.Results = append(rep.Results, analyzer.ProbeResult{Target: t, Route: route.Direct, Verdict: "OK"})
	}
	return rep, nil
}

func (r *countingRunner) calls() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.batches...)
}

// firstRun is an App on a fresh machine: default config, empty data directory, real checker.
type firstRun struct {
	*harness
	runner *countingRunner
	logs   *syncBuffer
	real   *checker.Checker
}

// newFirstRun builds it. net is the checker's network source; nil means the real netwatch of this machine.
func newFirstRun(t *testing.T, net checker.NetworkSource) *firstRun {
	t.Helper()
	h := newHarness(t, route.Table{})
	cfg := writeRealDefaultConfig(t, h.p)
	if err := os.MkdirAll(h.p.Data, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &countingRunner{}
	ck, err := checker.New(cfg.Checker, checker.Deps{Runner: runner, Network: net, DataDir: h.p.Data})
	if err != nil {
		t.Fatalf("the checker does not start on the default config: %v", err)
	}
	h.d.checker = ck
	logs := &syncBuffer{}
	app, err := New(cfg, h.p, withDeps(h.d), WithUI(true), withLog(logs))
	if err != nil {
		t.Fatal(err)
	}
	h.app = app
	return &firstRun{harness: h, runner: runner, logs: logs, real: ck}
}

// minCandidates is the least the starter list must put in the table (the list has about 140 domains).
const minCandidates = 100

// waitBatch waits until the prober has been asked about at least minCandidates sites in one batch.
func (f *firstRun) waitBatch(what string) {
	f.t.Helper()
	waitUntil(f.t, what, func() bool {
		for _, n := range f.runner.calls() {
			if n >= minCandidates {
				return true
			}
		}
		return false
	})
}

// checkFirstRunTable asserts what the Sites page needs on a fresh install, before any network is known.
func (f *firstRun) checkFirstRunTable() {
	f.t.Helper()
	// The table is published and visible at once, whatever the network.
	if n := len(f.app.currentTable().Entries); n < minCandidates {
		f.t.Fatalf("the route table has %d entries, want >= %d (starter lists not referenced?)", n, minCandidates)
	}
	routes := f.app.Routes()
	if len(routes) < minCandidates {
		f.t.Fatalf("Routes() has %d entries, want >= %d: the Sites page would be empty", len(routes), minCandidates)
	}
	for _, r := range routes {
		if r.Domain == "" || !r.Route.Valid() {
			f.t.Fatalf("bad row %+v", r)
		}
	}
	if got := f.app.Snapshot().Tunnel; got != status.TunnelNone {
		f.t.Fatalf("Tunnel = %q, want none (no server on a first run)", got)
	}
}

// connectWithoutServer connects the way the Start button does and asserts the degraded mode works.
func (f *firstRun) connectWithoutServer() {
	f.t.Helper()
	if err := f.app.Connect(context.Background()); err != nil {
		f.t.Fatalf("Connect without a detour server failed: %v", err)
	}
	if le := f.app.LastError(); le != nil {
		f.t.Fatalf("LastError = %+v after a connect without a server (tunnel_missing is a regression)", le)
	}
	if f.app.State() != StateConnected {
		f.t.Fatalf("state = %s", f.app.State())
	}
	if _, ok := f.procs.spec(nameSingBox); !ok {
		f.t.Fatalf("sing-box not started: %s", f.ops())
	}
	for _, r := range f.app.Routes() {
		if r.Route == route.Tunnel && !r.Degraded {
			f.t.Fatalf("%s: a tunnel site without a server must be flagged degraded", r.Domain)
		}
	}
}

// Regressions 2, 3 and 5 at the App level: the default config yields a non-empty table and Routes() with
// the network still unknown, connect works without a server, and the sites are probed as soon as the
// network is identified.
func TestFirstRunDefaultConfigRoutesAndProbes(t *testing.T) {
	net := checker.NewFakeNetwork(checker.Network{}) // not identified yet
	f := newFirstRun(t, net)
	f.start()
	f.waitRunning()

	f.checkFirstRunTable()
	if got := f.app.Snapshot().Network; got != "unknown network" {
		t.Fatalf("Network = %q before identification", got)
	}
	time.Sleep(50 * time.Millisecond) // the startup check has had its chance
	if n := f.runner.calls(); len(n) != 0 {
		t.Fatalf("probed %v with an unknown network", n)
	}

	f.connectWithoutServer()

	net.Set(checker.Network{ID: "local:0123456789abcdef", Known: true})
	f.waitBatch("a check cycle that probes the candidates")
	waitUntil(t, "decisions in the table", func() bool {
		for _, r := range f.app.Routes() {
			if r.Route == route.Direct && r.Source == route.SourceChecked {
				return true
			}
		}
		return false
	})
	if len(f.app.Routes()) < minCandidates {
		t.Fatalf("Routes() shrank to %d after a check", len(f.app.Routes()))
	}
	if got := f.app.Snapshot().Network; got != "This connection" {
		t.Fatalf("Network label = %q for a local ID", got)
	}

	// "Check sites" probes everything again.
	before := len(f.runner.calls())
	if err := f.app.CheckNow(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the manual check", func() bool { return len(f.runner.calls()) > before })
}

// Regression 5 with the real OS: the real netif.Uplink and the real netwatch (no echo endpoints, so no
// network access) must identify this machine's connection, so the first check probes the sites. Skipped
// when the machine has no uplink with a default gateway (a CI container without a network).
func TestFirstRunRealNetworkIdentificationProbes(t *testing.T) {
	ifc, err := netif.Uplink(context.Background())
	if err != nil || !ifc.Gateway.IsValid() {
		t.Skipf("no physical uplink with a default gateway on this machine (err=%v): nothing to identify", err)
	}
	f := newFirstRun(t, nil) // nil: the checker builds the real netwatch
	f.start()
	f.waitRunning()
	f.checkFirstRunTable()

	waitUntil(t, "the real network to be identified", func() bool { return f.real.Status().Network.Known })
	id := f.real.Status().Network.ID
	if !strings.HasPrefix(id, "local:") && !strings.HasPrefix(id, "gw:") {
		t.Fatalf("network ID %q: want a local: or gw: ID without any echo endpoint", id)
	}
	f.waitBatch("the first check after identification")
	f.connectWithoutServer()
}

// Regression 4: a missing helper program ends the connect with its own code and a fixed sentence, not the
// generic connect_failed, and no path reaches the sentence.
func TestConnectMissingProgramIsSpecific(t *testing.T) {
	const secretDir = `C:\Users\someone\AppData\secret-dir`
	missing := func(only ...string) func(name, configured string) (string, error) {
		return func(name, _ string) (string, error) {
			for _, n := range only {
				if name == n {
					return "", errors.New(name + " binary not found at " + secretDir)
				}
			}
			return "/bin/" + name, nil
		}
	}
	cases := map[string]struct {
		table route.Table
		find  func(string, string) (string, error)
	}{
		"sing-box":                     {baseTable(t), missing("sing-box")},
		"desync engine, site needs it": {baseTable(t), missing("winws2", "nfqws2")},
		"both":                         {baseTable(t), missing("sing-box", "winws2", "nfqws2")},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			logs := &syncBuffer{}
			h := newHarness(t, c.table, WithUI(true), withLog(logs))
			h.d.findBinary = c.find
			h.start()
			h.waitRunning()
			err := h.app.Connect(context.Background())
			if err == nil {
				t.Fatal("connect succeeded without the program")
			}
			le := h.app.LastError()
			if le == nil || le.Code != CodeProgramMissing {
				t.Fatalf("LastError = %+v, want %s", le, CodeProgramMissing)
			}
			if le.Message != "A required program is missing next to the app. Reinstall the app." {
				t.Fatalf("message = %q", le.Message)
			}
			if strings.Contains(le.Message, "secret-dir") || strings.Contains(le.Message, `\`) || strings.Contains(le.Message, "/") {
				t.Fatalf("the message leaks a path: %q", le.Message)
			}
			if h.app.State() != StateDisconnected {
				t.Fatalf("state = %s", h.app.State())
			}
			if _, ok := h.procs.spec(nameSingBox); ok {
				t.Fatal("sing-box was started")
			}
			// The log, which stays on the machine, says which program (the hint behind "see the engine log").
			if s := logs.String(); !strings.Contains(s, "program_missing") || !strings.Contains(s, "not found") {
				t.Fatalf("the engine log does not explain the failure:\n%s", s)
			}
		})
	}

	// The default preset names fake payload files next to the engine; a missing one is the same packaging
	// problem as a missing program.
	t.Run("a preset payload is missing", func(t *testing.T) {
		h := newHarness(t, baseTable(t), WithUI(true))
		h.app.cfg.Desync.Strategy = nil // no custom strategy: the default preset applies
		var asked []string
		h.d.fileExists = func(p string) bool { asked = append(asked, p); return false }
		h.start()
		h.waitRunning()
		if err := h.app.Connect(context.Background()); err == nil {
			t.Fatal("connect succeeded without the fake payloads")
		}
		le := h.app.LastError()
		if le == nil || le.Code != CodeProgramMissing || le.Message != "A required program is missing next to the app. Reinstall the app." {
			t.Fatalf("LastError = %+v", le)
		}
		if len(asked) == 0 || !strings.Contains(asked[0], "/fake/") {
			t.Fatalf("payload files asked for: %v", asked)
		}
		if _, ok := h.procs.spec(nameSingBox); ok {
			t.Fatal("sing-box was started")
		}
	})

	t.Run("no site needs the desync engine", func(t *testing.T) {
		h := newHarness(t, tbl(t, ent("plain.example", route.Direct), ent("blocked.example", route.Tunnel)), WithUI(true))
		h.d.findBinary = missing("winws2", "nfqws2")
		h.start()
		h.waitRunning()
		if err := h.app.Connect(context.Background()); err != nil {
			t.Fatalf("connect must not need the desync engine when no site uses it: %v", err)
		}
	})
}

// The default app.yaml names the default preset and lists every preset a user can choose, so the file and the
// catalogue cannot drift apart.
func TestDefaultConfigNamesEveryPreset(t *testing.T) {
	code, err := os.ReadFile(filepath.Join("..", "..", "api", "desktop", "defaultconfig.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("(?s)const defaultConfigYAML = `(.*?)`").FindSubmatch(code)
	if m == nil {
		t.Fatal("defaultConfigYAML not found")
	}
	text := string(m[1])
	if !strings.Contains(text, "\ndesync:\n  preset: "+desync.DefaultPreset+"\n") {
		t.Errorf("the default config does not set desync.preset to %q", desync.DefaultPreset)
	}
	for _, id := range desync.PresetIDs() {
		if !strings.Contains(text, id) {
			t.Errorf("the default config does not mention preset %q", id)
		}
	}
	for _, word := range []string{"custom", "auto"} {
		if !strings.Contains(text, "preset: "+word) {
			t.Errorf("the default config does not explain preset: %s", word)
		}
	}
	cfg := writeRealDefaultConfig(t, paths.FromRoot(t.TempDir()))
	if cfg.Desync.Preset != desync.DefaultPreset {
		t.Errorf("loaded preset = %q", cfg.Desync.Preset)
	}
}

func TestClassifyProgramMissing(t *testing.T) {
	for name, err := range map[string]error{
		"sing-box":    fail(ExitConfig, "%w", errors.Join(errProgramMissing, errors.New("x"))),
		"desync":      fail(ExitConfig, "%w: the desync engine binary was not found", errProgramMissing),
		"plain wrap":  errors.Join(errors.New("ctx"), errProgramMissing),
		"wrapped one": errors.Join(errProgramMissing),
	} {
		if got := Classify(err); got == nil || got.Code != CodeProgramMissing {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if got := Classify(errors.New("boom")); got.Code != CodeConnectFailed {
		t.Errorf("plain error = %+v", got)
	}
	// The sing-box check of a bad port is not a missing program.
	if got := Classify(fail(ExitConfig, "sing-box config: %w", errors.New("x"))); got.Code != CodeConnectFailed {
		t.Errorf("config error = %+v", got)
	}
}
