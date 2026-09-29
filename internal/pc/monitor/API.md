# Module: monitor

| Field | Value |
|---|---|
| Path | internal/pc/monitor |
| Kind | child |
| Binary | – |
| Purpose | watches live connections via the Clash API, detects failures and counts traffic per route |
| Consumers | internal/pc |
| Children | – |
| Uses libs | internal/pc/lib/clashapi, internal/lib/analyzer, internal/lib/route, internal/lib/clock |
| Build tags | – |
| Status | planned |

## Responsibility

Connection-level observation only (no content inspection). It detects failure patterns, sends them to a sink, and keeps per-route traffic totals for the status page.

## Public API

```go
package monitor

type Failure struct {
    Domain  string
    Route   route.Route
    Verdict analyzer.Verdict // TLS_RST, STALL_16_20, TCP_TIMEOUT… (best-effort)
    At      time.Time
}

type Stats struct {
    Since    time.Time
    Bytes    map[route.Route]int64 // up+down
    Conns    map[route.Route]int64
    Failures map[route.Route]int64
}

// RouteOf maps a Clash connection (outbound chain / rule) to a route.
type RouteOf func(c clashapi.Connection) route.Route

type Config struct {
    PollInterval  time.Duration // default 2s
    StallMinBytes int64         // default 12 KB
    StallMaxBytes int64         // default 36 KB
    StallMinAge   time.Duration // default 10s
}

type Monitor struct{ /* ... */ }

func New(cfg Config, api clashapi.Client, routeOf RouteOf, c clock.Clock, sink func(Failure)) *Monitor
func (m *Monitor) Run(ctx context.Context) error
func (m *Monitor) Stats() Stats
```

## Behavior / contract

- Polls `/connections`. It detects closed connections by diffing snapshots, and derives download/upload deltas.
- Heuristics:
  - closed with ~0 download after a small upload within 1–2 s → `TLS_RST`-like;
  - open, download stuck inside the stall window for ≥ `StallMinAge` → `STALL_16_20`-like;
  - ended with 0 bytes after a connect timeout → `TCP_TIMEOUT`-like.
- Heuristics are best-effort: they only trigger rechecks, never decisions.
- Nothing is logged with full hostnames unless debug is on. Stats are aggregated per route, not per domain.

## Config

See above.

## Errors

`Run` keeps retrying the API with backoff. It returns only on ctx cancel.

## Testing

Fake Clash API with recorded snapshot sequences, one fixture per heuristic plus no false positive on a normal long download.

## Out of scope

Decisions, packet capture, HTTP status codes.

## Bootstrap (M0)

`doc.go` only.
