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
| Status | implemented |

## Responsibility

Lifecycle of external binaries (sing-box, winws2/nfqws2): ordered start/stop, restart with backoff, output capture, and a guarantee of no orphans.

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

// RestartPolicy says what the manager does when a process exits on its own.
type RestartPolicy string

const (
    RestartOnFailure RestartPolicy = "on-failure" // default (also for ""): restart with backoff
    RestartNever     RestartPolicy = "never"
)

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
    BinDir         string        // directory of our binaries; CleanupStale kills only processes below it
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

- Children never outlive the manager's process, even when it crashes or is killed:
  - Windows: every child is assigned to a Job Object with `KILL_ON_JOB_CLOSE`. The Win32 calls (`CreateJobObjectW`, `SetInformationJobObject`, `AssignProcessToJobObject`) are loaded from `kernel32.dll` via `syscall.NewLazyDLL`, so no extra dependency is needed. Children get their own process group (`CREATE_NEW_PROCESS_GROUP`), so Ctrl+C in the app's console does not stop them behind the supervisor's back. The structure mirrors are 64-bit only; a 32-bit Windows build fails to compile on purpose.
  - Linux: `Pdeathsig=SIGKILL` + its own process group. Linux sends the signal when the *thread* that started the child exits, so all children are started from one goroutine locked to its OS thread (`runtime.LockOSThread`) that lives as long as the manager.
- The manager never elevates privileges. If a child fails because admin is needed, it returns `ErrNeedsAdmin` with guidance.
- stdout/stderr go to a ring buffer (`Logs`, including a last line without a trailing newline); the app copies them to its log when a process fails. They are never parsed for decisions.

- `Start` polls `Ready` every 100 ms (clock) until it succeeds, the process exits, or ctx ends; the caller's ctx is the readiness timeout. A failed probe stops the process.
- Restart backoff on unexpected exit: 1 s, 2 s, 4 s ... capped at 30 s; at most `MaxRestarts` restarts inside `RestartWindow`, so the exit after the last allowed restart (exit number `MaxRestarts`+1) makes the state `failed`. A restarted process is not re-probed.
- A process with a `Ready` probe that exits before its first successful probe is never restarted, whatever the policy: the state becomes `failed` and `Start` returns at once with the exit reason and the last probe error.
- `Spec.Env` entries are added to the manager's own environment. `Restart` keeps the process's place in the stop order.
- `CleanupStale` refuses to run without `Config.BinDir`, skips processes whose path is unreadable, and never kills the current process.
- Windows has no graceful stop for a child without a console: `Stop` terminates it at once. Linux sends SIGTERM to the process group, then SIGKILL after `StopTimeout`.
- Output goes only to the ring buffer (`Logs`); the app decides what to do with it.
- Linux tests (`TestStopTimeoutKills`, Pdeathsig) are written but were only compile-checked on the Windows development machine.

## Config

See above.

## Errors

`ErrNeedsAdmin`, `ErrNotFound`, `ErrAlreadyRunning`, start/readiness errors.

## Testing

A tiny helper binary built in tests (sleeper, crasher, slow-stopper) covers ordering, restart backoff (fake clock), kill-on-parent-exit (subprocess test), and CleanupStale with the fake proc lister. Windows-smoke covers Job Objects.

## Out of scope

Knowing what the processes are. Service installation.

## Bootstrap (M0)

`doc.go` only. (Implemented in M3.)
