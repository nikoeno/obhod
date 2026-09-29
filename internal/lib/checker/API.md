# Module: checker

| Field | Value |
|---|---|
| Path | internal/lib/checker |
| Kind | lib |
| Binary | – |
| Purpose | runs the network-aware probe → decide → store cycle and publishes the current RouteTable |
| Consumers | internal/pc, internal/router |
| Children | internal/lib/checker/decide, internal/lib/checker/store, internal/lib/checker/netwatch, internal/lib/checker/lists, internal/lib/checker/schedule, internal/lib/checker/lib/model |
| Uses libs | internal/lib/analyzer, internal/lib/route, internal/lib/clock |
| Build tags | – |
| Status | planned |

## Responsibility

The brain shared by the PC app and the router service. It owns decisions: apps never touch storage, decide logic or network detection directly. They get a `route.Table` and events, and they report live failures back.

## Public API

```go
package checker

type Checker struct{ /* ... */ }

type Deps struct {
    Runner  analyzer.Runner
    Dialers []analyzer.Dialer // routes the checker may probe (currently: direct)
    Clock   clock.Clock
    DataDir string            // where store keeps decisions
    // Optional overrides for tests; nil → real implementations from children.
    Network NetworkSource
}

// NetworkSource is what the checker needs from network detection (netwatch implements it).
type NetworkSource interface {
    Current() Network
    Changes() <-chan Network
    Run(ctx context.Context) error
}

type Network struct {
    ID     string // opaque networkId, e.g. "asn:12389"
    ASN    uint32
    ASName string
    Known  bool   // false until identified
}

func New(cfg Config, deps Deps) (*Checker, error)

// Run blocks until ctx is done: watches the network, schedules checks, updates decisions.
func (c *Checker) Run(ctx context.Context) error

// Table returns the current table for the current network (never blocks).
func (c *Checker) Table() route.Table

// Subscribe delivers a new Table whenever it changes (latest-wins, buffered 1).
func (c *Checker) Subscribe() (<-chan route.Table, func())

// ReportFailure feeds a live connection failure (from a monitor) into the failure threshold.
func (c *Checker) ReportFailure(f Failure)

// CheckNow schedules an immediate check of the given domains (all candidates if none).
func (c *Checker) CheckNow(domains ...string)

func (c *Checker) Status() Status

type Failure struct {
    Domain  string
    Route   route.Route
    Verdict analyzer.Verdict
    At      time.Time
}

type Status struct {
    Network       Network
    LastCheck     time.Time
    NextCheck     time.Time
    Running       bool
    Counts        map[route.Route]int
    Expired       int
    LastReport    *analyzer.Report // most recent, for status pages
    Warnings      []string
}
```

## Behavior / contract

The cycle, on each trigger from `schedule`:

1. Pick targets: the trigger's domains, or all candidates from `lists`.
2. `Runner.Run` with the configured dialers. If calibration fails (no internet), make no decision changes.
3. For each domain, `decide.Decide(current, results)` → `store.Put`.
4. Rebuild the table: static entries (proxy/block, never changed) + stored decisions for this network + `SourceDefault` entries for candidates with no decision.
5. Publish it if it differs from the previous table.

Other rules:

- **Network change:** switch to that network's stored decisions immediately (publish), then trigger a check.
- **Unknown network:** candidates get `cfg.UnknownDefault` (tunnel) until the first check.
- **Expired entries** are still used, and trigger a recheck.
- Only one check runs at a time. Triggers arriving during a run are merged.

## Config

```go
type Config struct {
    Lists          lists.Config
    Schedule       schedule.Config
    Decide         decide.Policy
    Store          store.Config
    Netwatch       netwatch.Config
    UnknownDefault route.Route // default tunnel
}
```

(Child config types are re-exported by the checker root as `checker.ListsConfig` etc., so apps don't import children.)

## Errors

`New` fails on invalid config or an unreadable store. `Run` returns only on ctx cancel or a fatal store error. Probe problems never stop the loop.

## Testing

With a fake Runner, fake NetworkSource, fake clock and in-memory store, cover:

- startup on an unknown network;
- network switch restores saved decisions;
- hysteresis over two runs;
- a failure threshold triggers a recheck;
- static entries are never overridden;
- no changes when calibration fails.

## Out of scope

Generating any config file, starting processes, UI.

## Bootstrap (M0)

`doc.go` only.
