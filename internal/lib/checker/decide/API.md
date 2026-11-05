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
- **Direct only** (current scope): use `analyzer.SuggestRoute(direct)`. `LOCAL_ERROR`/`UNKNOWN` → no conclusion. Since 2026-11-01 (the rule is in the analyzer, `DESIGN.md` §7) a TLS-stage failure (`TLS_RST`, `TLS_TIMEOUT`, `TLS_EOF`, `TLS_ALERT`, `TLS_SPOOF`) and `HTTP_RST` give `desync` whatever the SNI swap says; only `TCP_TIMEOUT`/`TCP_REFUSED` (and `STALL_16_20`, `TLS_MITM`, `HTTP_STUB`) give `tunnel`. The preference order stays direct > desync > tunnel (`Policy.Order`) when several routes were probed. A decision `desync` made this way is a *candidate* until a desync trial has reached the site; that bookkeeping is not here: the decision stays `desync`/`checked` and `internal/pc` tracks `pending | verified | failed` on top of it (`internal/pc/API.md`, "Desync presets: automatic selection").
- **Hysteresis:** the candidate differs from current → increment `Pending`. Switch when `Pending.Count >= Confirmations`. The candidate equals current → clear `Pending`.
- **No current decision:** adopt the first conclusive candidate immediately (there's nothing to protect).
- `CheckedAt`/`ExpiresAt` are refreshed on every conclusive run, even without a change.
- Never outputs `proxy`/`block` (static only).

## Config

`Policy`.

## Errors

None (pure).

## Testing

Exhaustive table tests (2026-11-01: `TestCandidateDirectOnly` for every verdict, `TestTLSBlockSuggestsDesyncWhateverTheSwapSays` for the swap outcomes and the order direct > desync > tunnel): every verdict combination, hysteresis sequences (flip-flop never switches), site down, first decision, TTL refresh.

## Out of scope

Storage, scheduling, networkId.

## Bootstrap

Done: M2.
