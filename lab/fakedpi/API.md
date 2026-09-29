# Module: fakedpi

| Field | Value |
|---|---|
| Path | lab/fakedpi |
| Kind | lab |
| Binary | – |
| Purpose | emulates TSPU-like blocking on packets via NFQUEUE inside a lab namespace |
| Consumers | lab |
| Children | – |
| Uses libs | – |
| Build tags | linux |
| Status | planned |

## Responsibility

A deliberately simple censor, good enough to tell desync-works from tunnel-needed in tests.

## Public API

```go
//go:build linux

package fakedpi

type Kind string

const (
    NaiveSNI    Kind = "naive-sni"    // match SNI only in a single, unfragmented ClientHello → RST
    FirstHello  Kind = "first-hello"  // decide on the first TLS record seen, even if it is a fake one
    Reassemble  Kind = "reassemble"   // reassemble split ClientHello → RST (defeats simple split)
    Stall       Kind = "stall"        // drop server→client data after N bytes for matching flows
    IPBlock     Kind = "ip-block"     // drop SYNs to a prefix
    QUICBlock   Kind = "quic-block"   // drop UDP/443
)

type Rule struct {
    Kind    Kind
    Domains []string     // for SNI-based kinds (suffix match)
    Prefix  netip.Prefix // for ip-block / stall
    Bytes   int          // for stall
}

type Engine struct{ /* ... */ }

func Start(ctx context.Context, queue uint16, rules []Rule) (*Engine, error)
func (e *Engine) SetRules(rules []Rule) // atomic swap
func (e *Engine) Stats() map[Kind]int
func (e *Engine) Close() error
```

## Behavior / contract

- Per-flow state keyed by 5-tuple, with a short TTL.
- Injected RSTs use correct sequence numbers so the client stack accepts them.
- **Needs an NFQUEUE library** (e.g. a Go netlink NFQUEUE binding). That's a new dependency, so it must be approved before implementation.

## Config

Rules from lab profiles.

## Errors

Queue bind failures.

## Testing

Unit tests on packet fixtures (ClientHello parsing, reassembly, stall counting); integration in lab scenarios.

## Out of scope

Realistic performance, IPv6 (initially), anything outside lab namespaces.

## Bootstrap (M0)

`doc.go` only (with `//go:build linux`).
