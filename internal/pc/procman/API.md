# Module: procman

| Field | Value |
|---|---|
| Path | internal/pc/procman |
| Kind | child |
| Binary | – |
| Purpose | starts, stops, restarts and supervises child processes that die with the parent |
| Consumers | internal/pc |
| Children | – |
| Uses libs | internal/lib/proc, internal/lib/clock |
| Build tags | – |
| Status | planned |

## Responsibility

Lifecycle of external binaries (sing-box, winws/nfqws): ordered start/stop, restart with backoff, output capture, and a guarantee of no orphans.

## Public API

```go
package procman

type Spec struct {
    Name        string
    Path        string
    Args        []string
    Dir         string
    Env         []string
    Ready       func(ctx context.Context) error // optional readiness probe
    StopTimeout time.Duration                   // graceful stop before kill; default 5s
    Restart     RestartPolicy                   // never | on-failure (default)
}

type State string // starting, running, stopping, stopped, failed

type ProcStatus struct {
    Name     string
    State    State
    PID      int
    Started  time.Time
    Restarts int
    LastErr  string
}

type Event struct {
    Name  string
    State State
    At    time.Time
    Err   error
}

type Config struct {
    MaxRestarts    int           // per window; default 5
    RestartWindow  time.Duration // default 10m
    LogLines       int           // ring buffer per process; default 200
}

type Manager struct{ /* ... */ }

func New(cfg Config, c clock.Clock, procs proc.Lister) *Manager
func (m *Manager) Start(ctx context.Context, s Spec) error   // waits for Ready
func (m *Manager) Stop(ctx context.Context, name string) error
func (m *Manager) Restart(ctx context.Context, name string, s Spec) error
func (m *Manager) StopAll(ctx context.Context) error          // reverse start order
func (m *Manager) Status() []ProcStatus
func (m *Manager) Events() <-chan Event
func (m *Manager) Logs(name string) []string

// CleanupStale kills leftovers from a previous crash (matched by name + our path).
func (m *Manager) CleanupStale(ctx context.Context, names ...string) error
```

## Behavior / contract

- Windows: every child is assigned to a Job Object with `KILL_ON_JOB_CLOSE`. Linux: `Pdeathsig=SIGKILL` + its own process group.
- The manager never elevates privileges. If a child fails because admin is needed, it returns `ErrNeedsAdmin` with guidance.
- stdout/stderr go to a ring buffer and the app log. They are never parsed for decisions.

## Config

See above.

## Errors

`ErrNeedsAdmin`, `ErrNotFound`, `ErrAlreadyRunning`, start/readiness errors.

## Testing

A tiny helper binary built in tests (sleeper, crasher, slow-stopper) covers ordering, restart backoff (fake clock), kill-on-parent-exit (subprocess test), and CleanupStale with the fake proc lister. Windows-smoke covers Job Objects.

## Out of scope

Knowing what the processes are. Service installation.

## Bootstrap (M0)

`doc.go` only.
