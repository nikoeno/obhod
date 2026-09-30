# Module: decide

| Field | Value |
|---|---|
| Path | internal/lib/checker/decide |
| Kind | child |
| Binary | – |
| Purpose | turns probe results and the current decision into a new decision, with hysteresis (pure) |
| Consumers | internal/lib/checker |
| Children | – |
| Uses libs | internal/lib/checker/lib/model, internal/lib/analyzer, internal/lib/route |
| Build tags | – |
| Status | implemented |

## Responsibility

The single place where facts become a route. Pure function, trivially testable.

## Public API

```go
package decide

type Policy struct {
    Confirmations int           // results needed before switching; default 2
    TTL           time.Duration // default 24h
    Order         []route.Route // preference among working routes; default direct, desync, tunnel
}

type Input struct {
    Domain  string
    Current *model.Decision                        // nil if none
    Results map[route.Route]analyzer.ProbeResult   // routes probed this run
    Now     time.Time
    Policy  Policy
}

type Output struct {
    Decision model.Decision
    Changed  bool   // Route differs from Current.Route
    Reason   string // human-readable, for logs/status
}

func Decide(in Input) Output

// Candidate computes the route this run points to, ignoring hysteresis.
// ok=false → no conclusion (e.g. only LOCAL_ERROR / UNKNOWN, or site down).
func Candidate(results map[route.Route]analyzer.ProbeResult, order []route.Route) (r route.Route, ok bool, reason string)
```

## Behavior / contract

- **Full results** (several routes probed): the first route in `Order` whose result is `OK`/`OK_REDIRECT` wins. If all probed routes fail → "site down", no conclusion, keep current.
- **Direct only** (current scope): use `analyzer.SuggestRoute(direct)`. `LOCAL_ERROR`/`UNKNOWN` → no conclusion.
- **Hysteresis:** the candidate differs from current → increment `Pending`. Switch when `Pending.Count >= Confirmations`. The candidate equals current → clear `Pending`.
- **No current decision:** adopt the first conclusive candidate immediately (there's nothing to protect).
- `CheckedAt`/`ExpiresAt` are refreshed on every conclusive run, even without a change.
- Never outputs `proxy`/`block` (static only).

## Config

`Policy`.

## Errors

None (pure).

## Testing

Exhaustive table tests: every verdict combination, hysteresis sequences (flip-flop never switches), site down, first decision, TTL refresh.

## Out of scope

Storage, scheduling, networkId.

## Bootstrap

Done: M2.
