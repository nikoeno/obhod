# Module: netwatch

| Field | Value |
|---|---|
| Path | internal/lib/checker/netwatch |
| Kind | child |
| Binary | – |
| Purpose | identifies the current network (networkId) and emits change events |
| Consumers | internal/lib/checker |
| Children | – |
| Uses libs | internal/lib/checker/lib/model, internal/lib/netif, internal/lib/analyzer, internal/lib/clock |
| Build tags | – |
| Status | planned |

## Responsibility

Answers "which network am I on?" with a stable ID, so decisions can be kept per ISP. The checker root wraps it in a small adapter implementing `checker.NetworkSource`.

## Public API

```go
package netwatch

type Network struct {
    ID       model.NetworkID
    ASN      uint32
    ASName   string
    PublicIP netip.Addr // kept in memory only, never persisted
    Uplink   string     // interface name
    Known    bool
}

type Deps struct {
    Clock    clock.Clock
    Resolver analyzer.Resolver                          // DoH, direct path
    PublicIP func(ctx context.Context) (netip.Addr, error) // nil → default echo endpoints
    Uplink   func(ctx context.Context) (netif.Interface, error) // nil → netif.Uplink
    Events   func(ctx context.Context) (<-chan netif.Event, error) // nil → netif.Watch
}

type Watcher struct{ /* ... */ }

func New(cfg Config, deps Deps) *Watcher
func (w *Watcher) Run(ctx context.Context) error
func (w *Watcher) Current() Network
func (w *Watcher) Changes() <-chan Network // only emits when ID changes
```

## Behavior / contract

- **ID derivation:** `asn:<number>` when the public IP → ASN lookup succeeds. Otherwise `gw:<hash(gatewayMAC+SSID)>`. If `cfg.IncludeGateway` is set, `asn:<n>/gw:<hash>` (splits e.g. home vs mobile on the same ISP).
- Re-identify on every netif event (debounced by `cfg.Debounce`, default 3 s) and every `cfg.Refresh` (default 30 min).
- The public-IP lookup must go **direct** (not via tunnel), otherwise the ID would be the VPS's.
- Hashes use a per-install random salt, so IDs aren't linkable across installs.
- Identification failure → keep the last known ID if the uplink didn't change; otherwise `Known=false`.

## Config

```go
type Config struct {
    Debounce       time.Duration
    Refresh        time.Duration
    IncludeGateway bool
    EchoEndpoints  []string // public-IP echo services
    Salt           string   // generated once by the app, stored in Data dir
}
```

## Errors

`Run` returns only on ctx cancel.

## Testing

Fake uplink/events/public-IP/resolver: ASN path, gateway fallback, debounce, "no change → no event", ID stability.

## Out of scope

Deciding routes, choosing interfaces for probes.

## Bootstrap (M0)

`doc.go` only.
