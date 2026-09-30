# Module: route

| Field | Value |
|---|---|
| Path | internal/lib/route |
| Kind | lib |
| Binary | – |
| Purpose | defines the route kinds, domain normalization and the RouteTable shared by all modules |
| Consumers | internal/blockanalyzer, internal/lib/analyzer, internal/lib/analyzer/dialer, internal/lib/checker, internal/lib/checker/decide, internal/lib/checker/lib/model, internal/lib/checker/lists, internal/lib/checker/schedule, internal/pc, internal/pc/desync, internal/pc/monitor, internal/pc/singbox, internal/pc/status, internal/router, internal/router/podkop |
| Children | – |
| Uses libs | – |
| Build tags | – |
| Status | implemented: Route, Parse, Normalize, Source, Entry, Table, Diff |

## Responsibility

The shared vocabulary of the app: what a route is, how a domain is written, and the `Table` that the checker produces and the generators consume. Pure data and pure functions, with no I/O.

## Public API

```go
package route

// Route is where a connection for a domain goes.
type Route string

const (
    Direct Route = "direct" // not blocked, pass through
    Desync Route = "desync" // local DPI desync engine
    Tunnel Route = "tunnel" // encrypted tunnel to the VPS
    Proxy  Route = "proxy"  // separate SOCKS/HTTP proxy
    Block  Route = "block"  // drop
)

func All() []Route                  // stable order: direct, desync, tunnel, proxy, block
func Parse(s string) (Route, error)
func (r Route) Valid() bool
func (r Route) Checkable() bool     // true for direct, desync, tunnel (the checker may change these)

// Source says where an entry came from.
type Source string

const (
    SourceStatic  Source = "static"  // from proxy/block lists; never changed by the checker
    SourceChecked Source = "checked" // decided by the checker from probes
    SourceDefault Source = "default" // safe default for an unknown network (no probe yet)
)

// Entry routes a domain and all its subdomains.
type Entry struct {
    Domain string // normalized, see Normalize
    Route  Route
    Source Source
}

// Table is the complete routing decision for one network at one moment.
type Table struct {
    NetworkID string
    Generated time.Time
    Entries   []Entry // sorted by Domain; one entry per Domain
}

func NewTable(networkID string, generated time.Time, entries []Entry) (Table, error) // validates, dedups, sorts
func (t Table) Lookup(domain string) (Entry, bool) // longest-suffix match
func (t Table) Domains(r Route) []string           // sorted
func (t Table) Counts() map[Route]int
func (t Table) Equal(o Table) bool                 // ignores Generated
func Diff(old, new Table) []Change

type Change struct {
    Domain string
    From   Route // "" if newly added
    To     Route // "" if removed
}

// Normalize lowercases, trims a trailing dot and whitespace, converts IDN to punycode,
// and rejects IPs, wildcards, schemes, ports and paths.
func Normalize(domain string) (string, error)
```

## Behavior / contract

- `Lookup("a.b.example.com")` matches the entry `example.com` unless a longer entry such as `b.example.com` exists.
- `NewTable` returns an error for invalid routes or domains. For duplicate domains the last entry wins, but a `SourceStatic` entry is never overridden by other sources.
- `Table` is a value. Consumers must not mutate `Entries` in place.

## Config

None.

## Errors

`ErrInvalidRoute` and `ErrInvalidDomain` (wrapped with the offending value).

## Testing

Table tests for `Normalize` (IDN, trailing dot, rejects `*.x`, `1.2.3.4`, `https://x`, `x:443`), `Lookup` suffix rules, `NewTable` dedup/static precedence, and `Diff`.

## Out of scope

Any file format (lists, sing-box, podkop). Those live in their modules.

## Bootstrap (M0)

`doc.go` only.
