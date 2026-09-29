# Module: model

| Field | Value |
|---|---|
| Path | internal/lib/checker/lib/model |
| Kind | lib |
| Binary | – |
| Purpose | defines the decision and network identity types shared inside the checker |
| Consumers | internal/lib/checker, internal/lib/checker/decide, internal/lib/checker/netwatch, internal/lib/checker/schedule, internal/lib/checker/store |
| Children | – |
| Uses libs | internal/lib/route, internal/lib/analyzer |
| Build tags | – |
| Status | planned |

## Responsibility

Plain data types exchanged between the checker's children.

## Public API

```go
package model

// NetworkID is an opaque label for the current network, e.g. "asn:12389" or "gw:3f9a…".
type NetworkID string

func (id NetworkID) Valid() bool

// Decision is the stored route for one domain on one network.
type Decision struct {
    Domain    string
    Route     route.Route
    Source    route.Source // checked | default
    DecidedAt time.Time
    CheckedAt time.Time    // last probe, even if the route didn't change
    ExpiresAt time.Time    // CheckedAt + TTL
    Pending   *Pending     // candidate route awaiting confirmation (hysteresis)
    Last      map[route.Route]analyzer.Verdict // last verdict per probed route
}

type Pending struct {
    Route route.Route
    Count int // consecutive agreeing results
    Since time.Time
}

func (d Decision) Expired(now time.Time) bool
```

## Behavior / contract

JSON-serializable with stable field names (used by the store file format).

## Config

None.

## Errors

None.

## Testing

JSON round-trip, `Expired`.

## Out of scope

Logic (decide), persistence (store).

## Bootstrap (M0)

`doc.go` only.
