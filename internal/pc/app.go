package pc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nikoejento/obhod/internal/lib/paths"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/desync"
	"github.com/nikoejento/obhod/internal/pc/lib/clashapi"
	"github.com/nikoejento/obhod/internal/pc/procman"
	"github.com/nikoejento/obhod/internal/pc/singbox"
	"github.com/nikoejento/obhod/internal/pc/status"
)

// Exit codes of the splitter command.
const (
	exitOK      = 0
	exitConfig  = 1 // config or usage error
	exitManual  = 2 // needs admin rights or a manual setup step (the message says which)
	exitRuntime = 3 // runtime failure
)

// exitError carries the exit code of a failed Run.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func fail(code int, format string, a ...any) error {
	return &exitError{code: code, err: fmt.Errorf(format, a...)}
}

// exitCode maps the error of Run to the process exit code.
func exitCode(err error) int {
	var ee *exitError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &ee):
		return ee.code
	case errors.Is(err, procman.ErrNeedsAdmin):
		return exitManual
	}
	return exitRuntime
}

// State is the connection state of the App.
type State string

// The connection states. In Disconnected the checker and the status server
// run; sing-box, the desync engine and the monitor do not.
const (
	StateDisconnected  State = "disconnected"
	StateConnecting    State = "connecting"
	StateConnected     State = "connected"
	StateDisconnecting State = "disconnecting"
)

const (
	nameSingBox = "sing-box"

	reloadDebounce  = 3 * time.Second
	singBoxMinGap   = 30 * time.Second // G14: a sing-box reload is a restart that drops connections
	startTimeout    = 45 * time.Second
	stepTimeout     = 15 * time.Second
	parentPoll      = 2 * time.Second
	recentChanges   = 20
	singBoxFileName = "sing-box.json"
)

// Option customizes New.
type Option func(*App)

// WithLogger sets the logger; the default discards everything.
func WithLogger(l *slog.Logger) Option { return func(a *App) { a.log = l } }

func withUI(ui bool) Option        { return func(a *App) { a.ui = ui } }
func withParentPID(pid int) Option { return func(a *App) { a.parentPID = pid } }
func withDeps(d *deps) Option      { return func(a *App) { a.d = d } }
func withTunnelOutbounds(ob singbox.Outbounds) Option {
	return func(a *App) { a.outbounds, a.haveOutbounds = ob, true }
}

// App is the composition root: it owns the lifecycle of the checker, sing-box,
// the desync engine, the monitor and the status server.
type App struct {
	cfg       Config
	p         paths.Paths
	log       *slog.Logger
	d         *deps
	ui        bool
	parentPID int

	outbounds     singbox.Outbounds
	haveOutbounds bool

	// op serializes connect, disconnect and reload.
	op sync.Mutex

	mu         sync.Mutex
	runCtx     context.Context
	cancelRun  context.CancelFunc
	state      State
	secret     string
	table      route.Table
	recent     []status.Change
	uplinkName string
	engine     engineState
	lastSBRun  time.Time
	monitor    monitorRunner
	monCancel  context.CancelFunc
	monDone    chan struct{}
	dialClash  clashapi.Client
	binaries   binaries
}

// task is a background goroutine that can be stopped and waited for.
type task struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// stop cancels the task and waits, bounded, for it to finish. A zero task is a no-op.
func (t task) stop() {
	if t.cancel == nil {
		return
	}
	t.cancel()
	select {
	case <-t.done:
	case <-time.After(stepTimeout):
	}
}

// engineState remembers what the desync engine runs with, to tell a change from a no-op.
type engineState struct {
	running bool
	name    string
	args    string // joined; a change restarts the engine
	files   map[string]string
}

type binaries struct{ singBox, desync string }

// New builds the App and every child. Nothing is started and no file is
// changed. With real dependencies it reads the check lists and the decision store.
func New(cfg Config, p paths.Paths, opts ...Option) (*App, error) {
	a := &App{cfg: cfg, p: p, log: slog.New(slog.NewTextHandler(io.Discard, nil)), state: StateDisconnected}
	for _, o := range opts {
		o(a)
	}
	if err := a.cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if a.d == nil {
		d, err := realDeps(a.cfg, p)
		if err != nil {
			return nil, err
		}
		a.d = d
	}
	return a, nil
}

// State returns the current connection state.
func (a *App) State() State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

func (a *App) setState(s State) {
	a.mu.Lock()
	a.state = s
	a.mu.Unlock()
	a.log.Info("state", "state", string(s))
}

// Run starts the app and blocks until ctx is done or a fatal error happens,
// then shuts everything down in reverse order. In terminal mode it connects at
// once; with the ui option it waits for Connect.
func (a *App) Run(ctx context.Context) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 1. Pre-flight.
	if err := a.preflight(ctx); err != nil {
		return err
	}
	secret, err := a.d.newSecret()
	if err != nil {
		return fail(exitRuntime, "generate clash api secret: %v", err)
	}
	a.mu.Lock()
	a.runCtx, a.cancelRun, a.secret = ctx, cancel, secret
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.runCtx, a.cancelRun = nil, nil
		a.mu.Unlock()
	}()

	// 2. The checker, and its first table (the store or the defaults).
	var wg sync.WaitGroup
	fatal := make(chan error, 4)
	run := func(name string, f func(context.Context) error) task {
		c, cancel := context.WithCancel(context.WithoutCancel(ctx))
		done := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(done)
			if err := f(c); err != nil && !errors.Is(err, context.Canceled) {
				fatal <- fmt.Errorf("%s: %w", name, err)
			}
		}()
		return task{cancel: cancel, done: done}
	}
	checkerTask := run("checker", a.d.checker.Run)
	a.setTable(a.d.checker.Table())

	var statusTask task
	shutdown := func() {
		a.log.Info("shutting down")
		statusTask.stop()
		dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), 3*stepTimeout)
		defer dcancel()
		if derr := a.disconnect(dctx); derr != nil {
			err = errors.Join(err, derr)
		}
		checkerTask.stop()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(stepTimeout):
			a.log.Warn("a background task did not stop in time")
		}
	}

	// Connected state: steps 3-5 of the startup order.
	if !a.ui {
		if cerr := a.Connect(ctx); cerr != nil {
			err = cerr
			shutdown()
			return err
		}
	}
	srv, serr := a.d.newStatus(a.cfg.Status, &statusSource{a: a}, a.liveClient())
	if serr != nil {
		err = fail(exitConfig, "status: %v", serr)
		shutdown()
		return err
	}
	statusTask = run("status", srv.Run)

	// Background loops.
	reloadCtx, stopReload := context.WithCancel(context.WithoutCancel(ctx))
	wg.Add(1)
	go func() { defer wg.Done(); a.reloadLoop(reloadCtx) }()
	defer stopReload()
	if a.parentPID > 0 {
		wg.Add(1)
		go func() { defer wg.Done(); a.watchParent(reloadCtx, cancel) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); a.logProcEvents(reloadCtx) }()

	select {
	case <-ctx.Done():
	case ferr := <-fatal:
		err = fail(exitRuntime, "%v", ferr)
	}
	stopReload()
	shutdown()
	return err
}

func (a *App) preflight(ctx context.Context) error {
	ok, err := a.d.isAdmin()
	if err != nil {
		a.log.Warn("cannot determine admin rights", "err", err)
	} else if !ok {
		return fail(exitManual, "administrator rights are required (TUN and packet interception): %s", elevationHint)
	}
	if err := a.d.ensure(a.p); err != nil {
		if !errors.Is(err, paths.ErrACL) {
			return fail(exitRuntime, "create directories: %v", err)
		}
		a.log.Warn("could not restrict directory permissions", "err", err)
	}
	if !a.haveOutbounds {
		ob, err := loadSecrets(a.cfg.Secrets, a.p)
		if err != nil {
			return fail(exitConfig, "%v", err)
		}
		a.outbounds, a.haveOutbounds = ob, true
	}
	if err := a.d.procs.CleanupStale(ctx, nameSingBox, string(desync.WinWS), string(desync.NFQWS)); err != nil {
		a.log.Warn("cleanup of stale processes", "err", err)
	}
	return nil
}

func (a *App) setTable(t route.Table) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.table.NetworkID != "" || len(a.table.Entries) > 0 {
		for _, c := range route.Diff(a.table, t) {
			a.recent = append(a.recent, status.Change{Domain: c.Domain, From: c.From, To: c.To, At: a.d.clk.Now()})
		}
		if n := len(a.recent); n > recentChanges {
			a.recent = append([]status.Change(nil), a.recent[n-recentChanges:]...)
		}
	}
	a.table = t
}

func (a *App) currentTable() route.Table {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.table
}

func (a *App) clashAddr() string {
	if a.cfg.SingBox.ClashAPI != "" {
		return a.cfg.SingBox.ClashAPI
	}
	return "127.0.0.1:9090"
}

// liveClient returns the Clash API client for the current run's secret.
func (a *App) liveClient() clashapi.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dialClash == nil {
		a.dialClash = a.d.newClash("http://"+a.clashAddr(), a.secret)
	}
	return a.dialClash
}

// Connect starts the desync engine, sing-box and the monitor. It is a no-op
// when already connected. On failure everything it started is stopped again.
func (a *App) Connect(ctx context.Context) error {
	a.op.Lock()
	defer a.op.Unlock()
	if a.State() == StateConnected {
		return nil
	}
	a.mu.Lock()
	running := a.runCtx != nil
	a.mu.Unlock()
	if !running {
		return errors.New("pc: Connect needs a running App")
	}
	a.setState(StateConnecting)
	if err := a.connectLocked(ctx); err != nil {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*stepTimeout)
		defer cancel()
		_ = a.disconnectLocked(stopCtx)
		return err
	}
	a.setState(StateConnected)
	return nil
}

// Disconnect stops the monitor, sing-box and the desync engine, in that
// order, and keeps the checker running.
func (a *App) Disconnect(ctx context.Context) error {
	a.op.Lock()
	defer a.op.Unlock()
	return a.disconnectLocked(ctx)
}

func (a *App) disconnect(ctx context.Context) error {
	a.op.Lock()
	defer a.op.Unlock()
	return a.disconnectLocked(ctx)
}

func (a *App) disconnectLocked(ctx context.Context) error {
	if a.State() == StateDisconnected {
		return nil
	}
	a.setState(StateDisconnecting)
	var errs []error
	a.stopMonitor()
	for _, name := range []string{nameSingBox, string(desync.WinWS), string(desync.NFQWS)} {
		if err := a.d.procs.Stop(ctx, name); err != nil && !errors.Is(err, procman.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	a.mu.Lock()
	a.engine = engineState{}
	a.mu.Unlock()
	a.setState(StateDisconnected)
	return errors.Join(errs...)
}

// connectLocked is steps 3-5 of the startup order: the desync engine first (it
// must be up before the TUN), then sing-box, then the monitor.
func (a *App) connectLocked(ctx context.Context) error {
	table := a.currentTable()
	bins, err := a.resolveBinaries()
	if err != nil {
		return fail(exitConfig, "%v", err)
	}
	r, err := a.render(ctx, bins, table, true)
	if err != nil {
		return err
	}

	// Desync engine.
	if r.plan.Path != "" {
		if err := a.checkNFT(ctx, r.plan); err != nil {
			return err
		}
		if err := a.writePlanFiles(r.plan); err != nil {
			return fail(exitRuntime, "write desync files: %v", err)
		}
		sctx, cancel := context.WithTimeout(ctx, stepTimeout)
		err := a.d.procs.Start(sctx, a.engineSpec(r.plan))
		cancel()
		if err != nil {
			return a.startError("desync engine", err)
		}
		a.rememberEngine(r.plan)
	}

	// sing-box.
	cfgFile := filepath.Join(a.p.Run, singBoxFileName)
	if err := a.stageSingBox(ctx, bins, r.singBox, cfgFile); err != nil {
		return fail(exitConfig, "%v", err)
	}
	sctx, cancel := context.WithTimeout(ctx, startTimeout)
	err = a.d.procs.Start(sctx, a.singBoxSpec(bins, cfgFile))
	cancel()
	if err != nil {
		return a.startError("sing-box", err)
	}
	a.mu.Lock()
	a.lastSBRun = a.d.clk.Now()
	a.mu.Unlock()

	a.startMonitor()
	return nil
}

func (a *App) startError(what string, err error) error {
	if errors.Is(err, procman.ErrNeedsAdmin) {
		return fail(exitManual, "%s: %v: %s", what, err, elevationHint)
	}
	return fail(exitRuntime, "start %s: %v", what, err)
}

// checkNFT, on Linux, refuses to start when the nftables table that feeds the
// engine is missing. The app never applies firewall rules; it prints the commands.
func (a *App) checkNFT(ctx context.Context, plan desync.Plan) error {
	if len(plan.Setup) == 0 {
		return nil
	}
	ok, err := a.d.nftTable(ctx)
	if err != nil {
		return fail(exitManual, "cannot check the nftables table (is nft installed?): %v", err)
	}
	if ok {
		return nil
	}
	var b strings.Builder
	b.WriteString("the nftables table \"inet splitter\" does not exist. Apply these commands as root, then start again:\n")
	for _, c := range plan.Setup {
		b.WriteString("  " + c + "\n")
	}
	b.WriteString("Remove the rules later with:\n")
	for _, c := range plan.Teardown {
		b.WriteString("  " + c + "\n")
	}
	return &exitError{code: exitManual, err: errors.New(strings.TrimRight(b.String(), "\n"))}
}

func (a *App) startMonitor() {
	live := a.liveClient()
	m := a.d.newMonitor(a.cfg.Monitor, live, routeByTag, a.d.clk, a.failureSink())
	mctx, cancel := context.WithCancel(context.WithoutCancel(a.runContext()))
	done := make(chan struct{})
	a.mu.Lock()
	a.monitor, a.monCancel, a.monDone = m, cancel, done
	a.mu.Unlock()
	go func() {
		defer close(done)
		_ = m.Run(mctx)
	}()
}

func (a *App) stopMonitor() {
	a.mu.Lock()
	cancel, done := a.monCancel, a.monDone
	a.monCancel, a.monDone = nil, nil
	a.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(stepTimeout):
		a.log.Warn("monitor did not stop in time")
	}
	a.mu.Lock()
	a.monitor = nil
	a.mu.Unlock()
}

func (a *App) runContext() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runCtx == nil {
		return context.Background()
	}
	return a.runCtx
}

// watchParent shuts the app down when the UI that started it is gone: an
// elevated process cannot join the UI's job object, so it follows by polling.
func (a *App) watchParent(ctx context.Context, stop context.CancelFunc) {
	tk := a.d.clk.NewTicker(parentPoll)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C():
			if !a.d.parentUp(ctx, a.parentPID) {
				a.log.Info("parent process is gone")
				a.mu.Lock()
				cancel := a.cancelRun
				a.mu.Unlock()
				if cancel != nil {
					cancel()
				}
				stop()
				return
			}
		}
	}
}

func (a *App) logProcEvents(ctx context.Context) {
	ev := a.d.procs.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-ev:
			if !ok {
				return
			}
			if e.Err != nil {
				a.log.Warn("process", "name", e.Name, "state", string(e.State), "err", e.Err)
			} else {
				a.log.Info("process", "name", e.Name, "state", string(e.State))
			}
		}
	}
}

// resolveBinaries finds sing-box and, when needed, the desync engine.
func (a *App) resolveBinaries() (binaries, error) {
	a.mu.Lock()
	b := a.binaries
	a.mu.Unlock()
	if b.singBox == "" {
		sb, err := a.d.findBinary("sing-box", a.cfg.SingBox.Binary)
		if err != nil {
			return binaries{}, err
		}
		b.singBox = sb
	}
	if b.desync == "" {
		name := string(desync.DefaultEngine())
		if a.cfg.Desync.Engine != "" {
			name = string(a.cfg.Desync.Engine)
		}
		if name != "" {
			// The engine is only required when some domain needs it; a missing binary is
			// reported when a plan is built.
			if d, err := a.d.findBinary(name, a.cfg.Desync.Binary); err == nil {
				b.desync = d
			}
		}
	}
	a.mu.Lock()
	a.binaries = b
	a.mu.Unlock()
	return b, nil
}

// ownProcesses are the process names sing-box must route direct: ourselves and
// the binaries we start, plus anything the user listed.
func (a *App) ownProcesses(b binaries) []string {
	names := append([]string(nil), a.cfg.SingBox.OwnProcesses...)
	for _, p := range []string{a.d.exePath, b.singBox, b.desync} {
		if p != "" {
			names = append(names, filepath.Base(p))
		}
	}
	return names
}

func (a *App) singBoxSpec(b binaries, cfgFile string) procman.Spec {
	path, args := singbox.Command(singbox.Config{Binary: b.singBox}, cfgFile)
	live := a.liveClient()
	return procman.Spec{
		Name: nameSingBox,
		Path: path,
		Args: args,
		Dir:  a.p.Run,
		Ready: func(ctx context.Context) error {
			_, err := live.Version(ctx)
			return err
		},
	}
}

func (a *App) engineSpec(plan desync.Plan) procman.Spec {
	return procman.Spec{Name: string(plan.Engine), Path: plan.Path, Args: plan.Args, Dir: a.p.Run}
}

func (a *App) rememberEngine(plan desync.Plan) {
	files := make(map[string]string, len(plan.Files))
	for k, v := range plan.Files {
		files[k] = string(v)
	}
	a.mu.Lock()
	a.engine = engineState{running: true, name: string(plan.Engine), args: strings.Join(plan.Args, "\x00"), files: files}
	a.mu.Unlock()
}

// writePlanFiles writes the engine's files (the hostlist) into the Run directory.
func (a *App) writePlanFiles(plan desync.Plan) error {
	for name, data := range plan.Files {
		if !plainFileName(name) {
			return fmt.Errorf("unsafe file name %q in plan", name)
		}
		if err := writeAtomic(filepath.Join(a.p.Run, name), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// writeAtomic writes data to a temporary file in the same directory and renames it over path.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
