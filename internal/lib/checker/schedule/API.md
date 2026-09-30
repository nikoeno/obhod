# Module: schedule

| Field | Value |
|---|---|
| Path | internal/lib/checker/schedule |
| Kind | child |
| Binary | – |
| Purpose | decides when to run checks: timer with jitter, network change, expired TTL, failure threshold |
| Consumers | internal/lib/checker |
| Children | – |
| Uses libs | internal/lib/checker/lib/model, internal/lib/route, internal/lib/clock |
| Build tags | – |
| Status | implemented |

## Responsibility

Turns events into check triggers, with rate limiting so checks never pile up.

## Public API

```go
package schedule

type Kind string

const (
    Startup       Kind = "startup"
    Timer         Kind = "timer"
    NetworkChange Kind = "network_change"
    Expired       Kind = "expired"
    Failures      Kind = "failures"
    Manual        Kind = "manual"
)

type Trigger struct {
    Kind    Kind
    Network model.NetworkID
    Domains []string // nil = all candidates
    At      time.Time
}

type Config struct {
    Interval      time.Duration // default 1h
    Jitter        time.Duration // default ±10m
    MinGap        time.Duration // min time between full checks; default 5m
    FailThreshold int           // default 3
    FailWindow    time.Duration // default 5m
}

type Scheduler struct{ /* ... */ }

func New(cfg Config, c clock.Clock) *Scheduler
func (s *Scheduler) Run(ctx context.Context) error
func (s *Scheduler) Triggers() <-chan Trigger

func (s *Scheduler) NetworkChanged(id model.NetworkID)
func (s *Scheduler) Expired(id model.NetworkID, domains []string)
func (s *Scheduler) Failure(id model.NetworkID, domain string, r route.Route, at time.Time)
func (s *Scheduler) Manual(domains []string)
func (s *Scheduler) Done(t Trigger) // the checker finished handling t (exactly once per trigger)
func (s *Scheduler) Next() time.Time // when the periodic check is due (zero before Run)
```

## Behavior / contract

- At most one outstanding trigger. New events merge into it (the union of domains; `nil` absorbs everything).
- Network change → immediate trigger, bypassing `MinGap`. `MinGap` only delays full timer/expiry/startup checks: manual requests and targeted rechecks (`Domains != nil`) go out at once.
- Merged trigger: `Kind` by priority (network_change > failures > manual > expired > timer > startup); a network change replaces the pending `Network`. Events for a network other than the current one (after `NetworkChanged`) are ignored.
- Triggers are offered one at a time; the next is offered only after `Done`. Emitting a full check restarts the periodic countdown. `Interval - Jitter` is clamped to be at least `MinGap`.
- The methods never block; `Run` returns nil when ctx is done.
- Failure counting is per (network, domain, route) within `FailWindow`. Reaching the threshold → a trigger for that domain, and the counter resets.
- Timer interval = `Interval ± rand(Jitter)` (seeded RNG injectable for tests).

## Config

See above.

## Errors

None.

## Testing

Fake clock: jitter bounds, merge semantics, MinGap, threshold/window, network-change priority.

## Out of scope

Running probes.

## Bootstrap

Done: M5.
