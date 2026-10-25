package pc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"

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
	Logs(name string) []string
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
	Listen() error // binds the port; called at startup so a busy port fails before the tunnel is up
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
	resetDialer func() // drops the checker's cached uplink binding at once (it also expires by itself)

	ensure       func(paths.Paths) error // creates the owner-only directories
	lockInstance func(dir string) (release func(), err error)
	isAdmin      func() (bool, error)
	// nftTable lists the "inet splitter" table (Linux); ok is false when it is
	// missing or unreadable. Other systems report ok with an empty listing.
	nftTable func(ctx context.Context) (listing string, ok bool, err error)
	// ipRuleList lists the IPv4 policy routing rules (`ip rule show`, read-only;
	// Linux). Other systems return errors.ErrUnsupported. nil: not checked.
	ipRuleList func(ctx context.Context) (listing string, err error)
	lookupHost func(ctx context.Context, host string) ([]netip.Addr, error)
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
		uplink:       netif.Uplink,
		watchUplink:  netif.Watch,
		ensure:       paths.Paths.Ensure,
		lockInstance: lockInstance,
		isAdmin:      isAdmin,
		lookupHost: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		nftTable:   nftTableExists,
		ipRuleList: ipRuleShow,
		singCheck:  singbox.Check,
		newSecret:  randomHex,
		parentUp:   processAlive,
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
	lazy := newLazyDirect(d.uplink, func(ifc netif.Interface) (analyzer.Dialer, error) {
		return analyzer.NewDirectDialer(analyzer.DirectOptions{BindIface: true, Uplink: ifc})
	}, d.clk)
	d.resetDialer = lazy.reset
	// Network identification looks the public IP's ASN up over DoH, dialed from the uplink like the probes.
	doh, err := analyzer.NewDoHResolver(cfg.Analyzer, lazy)
	if err != nil {
		return nil, fmt.Errorf("doh resolver: %w", err)
	}
	ck, err := checker.New(cc, checker.Deps{
		Runner:   runner,
		Resolver: doh,
		Dialers:  []analyzer.Dialer{lazy},
		DataDir:  p.Data,
	})
	if err != nil {
		return nil, fmt.Errorf("checker: %w", err)
	}
	d.checker = ck
	return d, nil
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
		// procman starts children with Dir set to the run directory, so a
		// relative path must be made absolute against the current directory now.
		if abs, err := filepath.Abs(configured); err == nil {
			configured = abs
		}
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
