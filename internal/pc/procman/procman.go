package procman

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/proc"
)

// Errors returned by the Manager.
var (
	// ErrNeedsAdmin means the OS refused to start the child without elevated rights.
	ErrNeedsAdmin = errors.New("procman: administrator rights are required to start this process; run the app elevated")
	// ErrNotFound means the binary does not exist or the process name is unknown.
	ErrNotFound = errors.New("procman: not found")
	// ErrAlreadyRunning means a process with this name is already started.
	ErrAlreadyRunning = errors.New("procman: already running")
)

// RestartPolicy says what the manager does when a process exits on its own.
type RestartPolicy string

// The restart policies.
const (
	RestartOnFailure RestartPolicy = "on-failure" // default (also for ""): restart with backoff
	RestartNever     RestartPolicy = "never"
)

// State is the lifecycle state of a managed process.
type State string

// The process states.
const (
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
)

// Spec describes one process to run.
type Spec struct {
	Name        string
	Path        string
	Args        []string
	Dir         string
	Env         []string                        // added to the environment of the manager's process
	Ready       func(ctx context.Context) error // optional readiness probe, polled until it succeeds
	StopTimeout time.Duration                   // graceful stop before kill; default 5s
	Restart     RestartPolicy                   // never | on-failure (default)
}

// ProcStatus is a snapshot of one managed process.
type ProcStatus struct {
	Name     string
	State    State
	PID      int
	Started  time.Time
	Restarts int
	LastErr  string
}

// Event reports a state transition.
type Event struct {
	Name  string
	State State
	At    time.Time
	Err   error
}

// Config tunes the Manager.
type Config struct {
	MaxRestarts   int           `yaml:"max_restarts"`   // per window; default 5
	RestartWindow time.Duration `yaml:"restart_window"` // default 10m
	LogLines      int           `yaml:"log_lines"`      // ring buffer per process; default 200
	// BinDir is the directory our third-party binaries live in. CleanupStale
	// kills only processes running from below it; without it nothing is killed.
	BinDir string `yaml:"-"` // set by the app, not by the user
}

const (
	defaultMaxRestarts   = 5
	defaultRestartWindow = 10 * time.Minute
	defaultLogLines      = 200
	defaultStopTimeout   = 5 * time.Second
	readyPoll            = 100 * time.Millisecond
	backoffBase          = time.Second
	backoffMax           = 30 * time.Second
	eventBuffer          = 64
)

type entry struct {
	spec     Spec
	state    State
	cmd      *exec.Cmd
	pid      int
	started  time.Time
	restarts int
	lastErr  string
	stopping bool
	ready    bool        // the readiness probe succeeded (or there is none)
	failures []time.Time // unexpected exits inside the restart window
	ring     *ring
	cancel   context.CancelFunc // wakes the supervisor out of a backoff sleep
	done     chan struct{}      // closed when the supervisor has finished
}

// Manager starts, supervises and stops child processes.
type Manager struct {
	cfg   Config
	clk   clock.Clock
	procs proc.Lister
	plat  platform

	mu      sync.Mutex
	order   []string
	entries map[string]*entry
	events  chan Event
}

// New returns a Manager. Zero Config fields take their defaults.
func New(cfg Config, c clock.Clock, procs proc.Lister) *Manager {
	if cfg.MaxRestarts <= 0 {
		cfg.MaxRestarts = defaultMaxRestarts
	}
	if cfg.RestartWindow <= 0 {
		cfg.RestartWindow = defaultRestartWindow
	}
	if cfg.LogLines <= 0 {
		cfg.LogLines = defaultLogLines
	}
	return &Manager{
		cfg:     cfg,
		clk:     c,
		procs:   procs,
		plat:    newPlatform(),
		entries: make(map[string]*entry),
		events:  make(chan Event, eventBuffer),
	}
}

// Events returns the channel of state transitions. When the buffer is full the
// oldest event is dropped, so a slow reader never blocks the manager.
func (m *Manager) Events() <-chan Event { return m.events }

// emit must be called with m.mu held or from a single goroutine per event; it never blocks.
func (m *Manager) emit(name string, st State, err error) {
	ev := Event{Name: name, State: st, At: m.clk.Now(), Err: err}
	for {
		select {
		case m.events <- ev:
			return
		default:
		}
		select {
		case <-m.events:
		default:
		}
	}
}

func (m *Manager) setState(e *entry, name string, st State, err error) {
	e.state = st
	m.emit(name, st, err)
}

// Start launches the process and, when Spec.Ready is set, waits until it succeeds,
// the process dies, or ctx ends. If readiness fails the process is stopped.
func (m *Manager) Start(ctx context.Context, s Spec) error {
	if s.Name == "" || s.Path == "" {
		return errors.New("procman: spec needs a name and a path")
	}
	if s.StopTimeout <= 0 {
		s.StopTimeout = defaultStopTimeout
	}
	switch s.Restart {
	case "", RestartOnFailure:
		s.Restart = RestartOnFailure
	case RestartNever:
	default:
		return fmt.Errorf("procman: unknown restart policy %q", s.Restart)
	}

	m.mu.Lock()
	if old, ok := m.entries[s.Name]; ok {
		switch old.state {
		case StateStarting, StateRunning, StateStopping:
			m.mu.Unlock()
			return fmt.Errorf("%w: %s", ErrAlreadyRunning, s.Name)
		}
	} else {
		m.order = append(m.order, s.Name)
	}
	e := &entry{spec: s, state: StateStarting, ring: newRing(m.cfg.LogLines), done: make(chan struct{})}
	m.entries[s.Name] = e
	m.emit(s.Name, StateStarting, nil)

	cmd, err := m.launch(e)
	if err != nil {
		e.lastErr = err.Error()
		m.setState(e, s.Name, StateFailed, err)
		close(e.done)
		m.mu.Unlock()
		return err
	}
	supCtx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.cmd, e.pid, e.started = cmd, cmd.Process.Pid, m.clk.Now()
	if s.Ready == nil {
		e.ready = true
		m.setState(e, s.Name, StateRunning, nil)
	}
	m.mu.Unlock()

	go m.supervise(supCtx, e, cmd)

	if s.Ready == nil {
		return nil
	}
	if err := m.waitReady(ctx, e); err != nil {
		stopCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), s.StopTimeout+10*time.Second)
		defer stop()
		_ = m.Stop(stopCtx, s.Name)
		return err
	}
	return nil
}

// launch builds and starts a command for e. m.mu must be held.
func (m *Manager) launch(e *entry) (*exec.Cmd, error) {
	s := e.spec
	cmd := exec.Command(s.Path, s.Args...)
	cmd.Dir = s.Dir
	if len(s.Env) > 0 {
		cmd.Env = append(os.Environ(), s.Env...)
	}
	cmd.Stdout, cmd.Stderr = e.ring, e.ring
	cmd.WaitDelay = 2 * time.Second // do not hang on a pipe kept open by a grandchild
	if err := m.plat.start(cmd); err != nil {
		switch {
		case m.plat.needsAdmin(err):
			return nil, fmt.Errorf("%w (%s)", ErrNeedsAdmin, s.Name)
		case errors.Is(err, os.ErrNotExist), errors.Is(err, exec.ErrNotFound):
			return nil, fmt.Errorf("%w: %s: %v", ErrNotFound, s.Name, err)
		}
		return nil, fmt.Errorf("procman: start %s: %w", s.Name, err)
	}
	return cmd, nil
}

// waitReady polls the readiness probe until it succeeds.
func (m *Manager) waitReady(ctx context.Context, e *entry) error {
	name := e.spec.Name
	var last error
	for {
		last = e.spec.Ready(ctx)
		if last == nil {
			m.mu.Lock()
			e.ready = true
			if e.state == StateStarting {
				m.setState(e, name, StateRunning, nil)
			}
			m.mu.Unlock()
			return nil
		}
		select {
		case <-e.done:
			m.mu.Lock()
			reason := e.lastErr
			m.mu.Unlock()
			return fmt.Errorf("procman: %s exited before it was ready (%s): %w", name, reason, last)
		default:
		}
		if err := m.clk.Sleep(ctx, readyPoll); err != nil {
			return fmt.Errorf("procman: %s not ready: %w (last probe: %v)", name, err, last)
		}
	}
}

// supervise waits for the process, restarting it under the policy.
func (m *Manager) supervise(ctx context.Context, e *entry, cmd *exec.Cmd) {
	name := e.spec.Name
	defer close(e.done)
	var launchErr error // set when relaunching failed; counted like a crash
	for {
		var waitErr error
		if cmd != nil {
			waitErr = cmd.Wait()
		} else {
			waitErr = launchErr
		}

		m.mu.Lock()
		if e.stopping {
			e.pid, e.cmd = 0, nil
			m.setState(e, name, StateStopped, nil)
			m.mu.Unlock()
			return
		}
		e.pid, e.cmd = 0, nil
		e.lastErr = exitReason(waitErr)
		// A process that dies before its first readiness is a start failure,
		// not a crash to restart: Start reports it at once instead of waiting
		// through the whole backoff schedule.
		if e.spec.Restart == RestartNever || !e.ready {
			st := StateFailed
			if waitErr == nil && e.ready {
				st = StateStopped
			}
			m.setState(e, name, st, waitErr)
			m.mu.Unlock()
			return
		}
		now := m.clk.Now()
		kept := e.failures[:0]
		for _, t := range e.failures {
			if now.Sub(t) < m.cfg.RestartWindow {
				kept = append(kept, t)
			}
		}
		e.failures = kept
		if len(e.failures) >= m.cfg.MaxRestarts {
			e.lastErr = fmt.Sprintf("gave up after %d restarts: %s", len(e.failures), e.lastErr)
			m.setState(e, name, StateFailed, errors.New(e.lastErr))
			m.mu.Unlock()
			return
		}
		delay := backoff(len(e.failures))
		e.failures = append(e.failures, now)
		m.setState(e, name, StateStarting, waitErr)
		m.mu.Unlock()

		if err := m.clk.Sleep(ctx, delay); err != nil {
			m.mu.Lock()
			e.pid, e.cmd = 0, nil
			m.setState(e, name, StateStopped, nil)
			m.mu.Unlock()
			return
		}

		m.mu.Lock()
		if e.stopping {
			m.setState(e, name, StateStopped, nil)
			m.mu.Unlock()
			return
		}
		next, err := m.launch(e)
		if err != nil {
			m.mu.Unlock()
			cmd, launchErr = nil, err
			continue
		}
		cmd = next
		e.cmd, e.pid, e.started = next, next.Process.Pid, m.clk.Now()
		e.restarts++
		m.setState(e, name, StateRunning, nil)
		m.mu.Unlock()
	}
}

// backoff returns the delay before restart number n (0-based): 1s, 2s, 4s … 30s.
func backoff(n int) time.Duration {
	d := backoffBase
	for i := 0; i < n && d < backoffMax; i++ {
		d *= 2
	}
	return min(d, backoffMax)
}

func exitReason(err error) string {
	if err == nil {
		return "exited"
	}
	return err.Error()
}

// Stop stops a process gracefully, then kills it after Spec.StopTimeout.
// Stopping an already stopped or failed process is a no-op.
func (m *Manager) Stop(ctx context.Context, name string) error {
	m.mu.Lock()
	e, ok := m.entries[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	select {
	case <-e.done:
		m.mu.Unlock()
		return nil
	default:
	}
	if !e.stopping {
		e.stopping = true
		m.setState(e, name, StateStopping, nil)
		e.cancel()
	}
	cmd, timeout := e.cmd, e.spec.StopTimeout
	m.mu.Unlock()

	if cmd != nil && cmd.Process != nil && !isDone(e.done) {
		if sent, _ := m.plat.signalStop(cmd); sent {
			t := m.clk.NewTimer(timeout)
			select {
			case <-e.done:
			case <-t.C():
				_ = m.plat.kill(cmd)
			case <-ctx.Done():
				t.Stop()
				_ = m.plat.kill(cmd)
				return ctx.Err()
			}
			t.Stop()
		} else {
			_ = m.plat.kill(cmd)
		}
	}
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func isDone(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// Restart stops the process (if running) and starts it again with s. The
// process keeps its place in the stop order.
func (m *Manager) Restart(ctx context.Context, name string, s Spec) error {
	if s.Name == "" {
		s.Name = name
	}
	if s.Name != name {
		return fmt.Errorf("procman: restart %q with a spec named %q", name, s.Name)
	}
	if err := m.Stop(ctx, name); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return m.Start(ctx, s)
}

// StopAll stops every process in reverse start order and joins the errors.
func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	names := append([]string(nil), m.order...)
	m.mu.Unlock()
	var errs []error
	for i := len(names) - 1; i >= 0; i-- {
		if err := m.Stop(ctx, names[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Status returns the state of every process in start order.
func (m *Manager) Status() []ProcStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ProcStatus, 0, len(m.order))
	for _, name := range m.order {
		e := m.entries[name]
		out = append(out, ProcStatus{
			Name: name, State: e.state, PID: e.pid, Started: e.started,
			Restarts: e.restarts, LastErr: e.lastErr,
		})
	}
	return out
}

// Logs returns the last lines the process wrote to stdout or stderr.
func (m *Manager) Logs(name string) []string {
	m.mu.Lock()
	e := m.entries[name]
	m.mu.Unlock()
	if e == nil {
		return nil
	}
	return e.ring.Lines()
}

// CleanupStale kills leftovers of a previous crash: processes with one of the
// names whose executable lives below Config.BinDir. A same-named process from
// anywhere else, one whose path is unreadable, or the current process is never
// touched.
func (m *Manager) CleanupStale(ctx context.Context, names ...string) error {
	if m.cfg.BinDir == "" {
		return errors.New("procman: CleanupStale needs Config.BinDir")
	}
	found, err := proc.Find(ctx, m.procs, names...)
	if err != nil {
		return fmt.Errorf("procman: list processes: %w", err)
	}
	var errs []error
	for _, p := range found {
		if p.PID == os.Getpid() || p.Path == "" || !isUnder(p.Path, m.cfg.BinDir) {
			continue
		}
		if err := killPID(p.PID); err != nil {
			errs = append(errs, fmt.Errorf("procman: kill stale %s (pid %d): %w", p.Name, p.PID, err))
		}
	}
	return errors.Join(errs...)
}

func killPID(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

// isUnder reports whether path is inside dir (case-insensitive on Windows).
func isUnder(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	if runtime.GOOS == "windows" {
		path, dir = strings.ToLower(path), strings.ToLower(dir)
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
