# Module: store

| Field | Value |
|---|---|
| Path | internal/lib/checker/store |
| Kind | child |
| Binary | – |
| Purpose | persists decisions per network and domain with atomic writes |
| Consumers | internal/lib/checker |
| Children | – |
| Uses libs | internal/lib/checker/lib/model, internal/lib/clock |
| Build tags | – |
| Status | planned |

## Responsibility

Durable decisions (`decisions.json`) that survive restarts and are safe against crashes and power loss (routers).

## Public API

```go
package store

type Store interface {
    Get(net model.NetworkID, domain string) (model.Decision, bool)
    All(net model.NetworkID) []model.Decision
    Put(net model.NetworkID, ds ...model.Decision) error
    Networks() []NetworkInfo
    // Prune removes networks not seen since before and decisions for domains not in keep.
    Prune(before time.Time, keep []string) error
    Close() error
}

type NetworkInfo struct {
    ID       model.NetworkID
    Label    string // e.g. ASN name, for status pages
    LastSeen time.Time
    Count    int
}

type Config struct {
    File          string        // default "<DataDir>/decisions.json"
    FlushInterval time.Duration // coalesce writes; default 0 (immediate), routers: 5m
}

func Open(cfg Config, c clock.Clock) (Store, error)
func NewMemory() Store // tests
```

## Behavior / contract

- File format: `{"schema":1,"networks":{"<id>":{"label":…,"last_seen":…,"decisions":{"<domain>":Decision}}}}`.
- Atomic write: temp file in the same dir → fsync → rename → fsync dir (where supported). The file is 0600.
- A corrupt file → rename it to `decisions.json.corrupt-<ts>`, start empty, and report via the returned warning (don't crash).
- Goroutine-safe. `Put` of an unchanged decision doesn't count as a write (saves flash).

## Config

See above.

## Errors

`Open`: unreadable dir/permissions. `Put`: write failures.

## Testing

Round-trip, a crash mid-write (a leftover temp file is ignored), corrupt-file recovery, flush coalescing with the fake clock, concurrent access (`-race`).

## Out of scope

Deciding anything, encryption at rest.

## Bootstrap (M0)

`doc.go` only.
