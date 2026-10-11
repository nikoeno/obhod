package pc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/checker"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/paths"
	"github.com/nikoejento/obhod/internal/lib/proc"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/lib/clashapi"
	"github.com/nikoejento/obhod/internal/pc/monitor"
	"github.com/nikoejento/obhod/internal/pc/procman"
	"github.com/nikoejento/obhod/internal/pc/singbox"
	"github.com/nikoejento/obhod/internal/pc/status"
)

// The small interfaces the App needs from its children and libs. The real
// types satisfy them; the wiring tests inject fakes.

type procRunner interface {
	Start(ctx context.Context, s procman.Spec) error
	Stop(ctx context.Context, name string) error
	Restart(ctx context.Context, name string, s procman.Spec) error
	StopAll(ctx context.Context) error
	Status() []procman.ProcStatus
	Events() <-chan procman.Event
	CleanupStale(ctx context.Context, names ...string) error
}

type checkerAPI interface {
	Run(ctx context.Context) error
	Table() route.Table
	Subscribe() (<-chan route.Table, func())
	ReportFailure(f checker.Failure)
	Status() checker.Status
}

type monitorRunner interface {
	Run(ctx context.Context) error
	Stats() monitor.Stats
}

type statusRunner interface {
	Run(ctx context.Context) error
}

// deps is everything the App touches outside itself.
type deps struct {
	clk        clock.Clock
	checker    checkerAPI
	procs      procRunner
	newClash   func(baseURL, secret string) clashapi.Client
	newMonitor func(cfg monitor.Config, api clashapi.Client, routeOf monitor.RouteOf, c clock.Clock, sink func(monitor.Failure)) monitorRunner
	newStatus  func(cfg status.Config, src status.Source, live clashapi.Client) (statusRunner, error)

	uplink      func(ctx context.Context) (netif.Interface, error)
	watchUplink func(ctx context.Context) (<-chan netif.Event, error)
	resetDialer func() // the checker's direct dialer is bound to the uplink it was built for

	ensure     func(paths.Paths) error // creates the owner-only directories
	isAdmin    func() (bool, error)
	nftTable   func(ctx context.Context) (bool, error) // Linux: does the "inet splitter" table exist?
	singCheck  func(ctx context.Context, binary, file string) error
	parentUp   func(ctx context.Context, pid int) bool
	newSecret  func() (string, error)
	findBinary func(name, configured string) (string, error)
	exePath    string
}

// realDeps wires the real children and libs.
func realDeps(cfg Config, p paths.Paths) (*deps, error) {
	exe, _ := os.Executable()
	d := &deps{
		clk:      clock.Real(),
		exePath:  exe,
		newClash: func(base, secret string) clashapi.Client { return clashapi.New(base, secret, nil) },
		newMonitor: func(c monitor.Config, api clashapi.Client, r monitor.RouteOf, clk clock.Clock, sink func(monitor.Failure)) monitorRunner {
			return monitor.New(c, api, r, clk, sink)
		},
		newStatus: func(c status.Config, src status.Source, live clashapi.Client) (statusRunner, error) {
			return status.New(c, src, live)
		},
		uplink:      netif.Uplink,
		watchUplink: netif.Watch,
		ensure:      paths.Paths.Ensure,
		isAdmin:     isAdmin,
		nftTable:    nftTableExists,
		singCheck:   singbox.Check,
		newSecret:   randomHex,
		parentUp:    processAlive,
	}
	d.findBinary = func(name, configured string) (string, error) {
		return findBinary(name, configured, exe, fileExists)
	}

	// The procman manager kills stale processes only below the directory of our binaries.
	pc := cfg.Procman
	if bin, err := d.findBinary("sing-box", cfg.SingBox.Binary); err == nil {
		pc.BinDir = filepath.Dir(bin)
	}
	d.procs = procman.New(pc, d.clk, proc.System())

	salt, err := loadSalt(p)
	if err != nil {
		return nil, err
	}
	cc := cfg.Checker
	cc.Netwatch.Salt = salt
	runner, err := analyzer.NewRunner(cfg.Analyzer, analyzer.Deps{System: analyzer.NewSystemResolver(), Procs: analyzer.SystemProcs()})
	if err != nil {
		return nil, fmt.Errorf("analyzer: %w", err)
	}
	lazy := &lazyDirect{}
	d.resetDialer = lazy.reset
	ck, err := checker.New(cc, checker.Deps{
		Runner:  runner,
		Dialers: []analyzer.Dialer{lazy},
		DataDir: p.Data,
	})
	if err != nil {
		return nil, fmt.Errorf("checker: %w", err)
	}
	d.checker = ck
	return d, nil
}

// lazyDirect is the checker's direct dialer. It is created on first use, so the
// app starts without a network, and rebuilt after an uplink change, because
// the real dialer is pinned to one interface (G12: probes never enter the TUN).
type lazyDirect struct {
	mu sync.Mutex
	d  analyzer.Dialer
}

func (l *lazyDirect) get() (analyzer.Dialer, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.d == nil {
		d, err := analyzer.NewDirectDialer(analyzer.DirectOptions{BindIface: true})
		if err != nil {
			return nil, err
		}
		l.d = d
	}
	return l.d, nil
}

func (l *lazyDirect) reset() {
	l.mu.Lock()
	l.d = nil
	l.mu.Unlock()
}

func (l *lazyDirect) Route() route.Route { return route.Direct }

func (l *lazyDirect) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d, err := l.get()
	if err != nil {
		return nil, err
	}
	return d.DialContext(ctx, network, addr)
}

func randomHex() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// loadSalt returns the per-installation salt that anonymizes network IDs, creating it once.
func loadSalt(p paths.Paths) (string, error) {
	file := filepath.Join(p.Data, "netwatch.salt")
	if b, err := os.ReadFile(file); err == nil && len(b) >= 32 {
		return string(b), nil
	}
	s, err := randomHex()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(p.Data, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(file, []byte(s), 0o600); err != nil {
		return "", err
	}
	return s, nil
}

func processAlive(ctx context.Context, pid int) bool {
	ps, err := proc.System().List(ctx)
	if err != nil {
		return true // cannot tell: do not shut down on a guess
	}
	for _, p := range ps {
		if p.PID == pid {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// findBinary returns the configured path, or looks for name next to the
// executable and in third_party/bin/<os>-<arch> (the repository layout).
func findBinary(name, configured, exe string, exists func(string) bool) (string, error) {
	if configured != "" {
		if !exists(configured) {
			return "", fmt.Errorf("%s binary %q does not exist", name, configured)
		}
		return configured, nil
	}
	file := name
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	plat := runtime.GOOS + "-" + runtime.GOARCH
	var dirs []string
	if exe != "" {
		ed := filepath.Dir(exe)
		dirs = append(dirs, ed, filepath.Join(ed, "bin"), filepath.Join(ed, "third_party", "bin", plat))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, "third_party", "bin", plat))
	}
	for _, d := range dirs {
		if c := filepath.Join(d, file); exists(c) {
			return c, nil
		}
	}
	return "", errors.New(name + " binary not found; set its path in the config or put it next to the executable")
}
