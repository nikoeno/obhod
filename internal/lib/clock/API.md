# Module: clock

| Field | Value |
|---|---|
| Path | internal/lib/clock |
| Kind | lib |
| Binary | – |
| Purpose | provides an injectable clock with a real and a fake implementation |
| Consumers | internal/lib/analyzer, internal/lib/checker, internal/lib/checker/netwatch, internal/lib/checker/schedule, internal/lib/checker/store, internal/pc, internal/pc/monitor, internal/pc/procman, internal/router |
| Children | – |
| Uses libs | – |
| Build tags | – |
| Status | planned |

## Responsibility

All time-dependent code gets time from a `Clock`, so TTLs, jitter, hysteresis, stall timeouts and failure windows are testable without sleeping.

## Public API

```go
package clock

type Clock interface {
    Now() time.Time
    Since(t time.Time) time.Duration
    NewTimer(d time.Duration) Timer
    NewTicker(d time.Duration) Ticker
    // Sleep waits for d or until ctx is done; returns ctx.Err() if cancelled.
    Sleep(ctx context.Context, d time.Duration) error
}

type Timer interface {
    C() <-chan time.Time
    Stop() bool
    Reset(d time.Duration) bool
}

type Ticker interface {
    C() <-chan time.Time
    Stop()
    Reset(d time.Duration)
}

func Real() Clock

// Fake is a manually advanced clock for tests.
type Fake struct{ /* ... */ }

func NewFake(start time.Time) *Fake
func (f *Fake) Advance(d time.Duration) // fires due timers/tickers in order
func (f *Fake) Set(t time.Time)          // must not move backwards
func (f *Fake) Waiters() int             // number of pending timers/tickers/sleeps
// BlockUntil waits (real time, bounded) until at least n waiters exist; for test sync.
func (f *Fake) BlockUntil(ctx context.Context, n int) error
```

## Behavior / contract

- `Fake` is goroutine-safe. `Advance` fires every timer whose deadline is ≤ the new time, in deadline order.
- Tickers on `Fake` fire at most once per `Advance` call per elapsed period (no unbounded bursts).
- `Real()` is a thin wrapper around the `time` package.

## Config

None.

## Errors

`Sleep` returns `ctx.Err()`.

## Testing

Fake: ordering of multiple timers, Reset/Stop semantics, ticker periods, `Sleep` with cancel, `BlockUntil`.

## Out of scope

Monotonic vs wall-clock subtleties beyond what the `time` package provides.

## Bootstrap (M0)

`doc.go` only.
