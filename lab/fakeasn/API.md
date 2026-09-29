# Module: fakeasn

| Field | Value |
|---|---|
| Path | lab/fakeasn |
| Kind | lab |
| Binary | – |
| Purpose | serves fake public-IP echo and Cymru-style ASN answers for the lab |
| Consumers | lab |
| Children | – |
| Uses libs | internal/lib/analyzer (tests) |
| Build tags | linux |
| Status | planned |

## Responsibility

Lets networkId detection work inside the lab: each fake ISP gets its own ASN.

## Public API

```go
//go:build linux

package fakeasn

type Mapping struct {
    Prefix netip.Prefix // client source prefix → ISP
    ASN    uint32
    Name   string
}

type Server struct {
    DNS  netip.AddrPort // answers Cymru-style TXT queries
    Echo netip.AddrPort // HTTP public-IP echo
}

func Run(ctx context.Context, dnsListen, echoListen string, m []Mapping) (*Server, error)
```

## Behavior / contract

The TXT answer format mirrors Team Cymru's (`"ASN | prefix | CC | registry | date"`), so the real parser in `dnscheck` is exercised.

## Config

Mappings from the lab profiles.

## Errors

Listen failures.

## Testing

Query it with the real `dnscheck.LookupASN` parser (via the analyzer's public `LookupASN`).

## Out of scope

Anything real-internet.

## Bootstrap (M0)

`doc.go` only (with `//go:build linux`).
