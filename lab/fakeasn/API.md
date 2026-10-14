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
| Build tags | – |
| Status | implemented (lab L3) |

## Responsibility

Lets networkId detection work inside the lab: each fake ISP gets its own ASN. The echo tells a client its public (NAT) address; the TXT answers map that address to an ASN. The app asks Cymru TXT only through DoH, so this module has no DNS listener: `lab/fakedns` serves the answers through its TXT hook, wired by the `lab` root.

## Public API

```go
package fakeasn

type Mapping struct {
    Prefix netip.Prefix // client source prefix → ISP
    ASN    uint32
    Name   string
}

type Server struct {
    Echo netip.AddrPort // HTTP public-IP echo
}

// Run starts the echo server on echoListen. It stops when ctx is done.
func Run(ctx context.Context, echoListen string, m []Mapping) (*Server, error)

// TXT answers Cymru-style names (<reversed ip>.origin.asn.cymru.com, origin6, AS<n>.asn.cymru.com)
// from the mappings; nil for any other name. It fits fakedns.TXTFunc.
func TXT(m []Mapping, name string) []string
```

## Behavior / contract

- The echo answers `GET /` with the caller's source address as plain text, the format the netwatch echo lookup reads.
- The TXT answer format mirrors Team Cymru's (`"ASN | prefix | CC | registry | date"`), so the real parser in `dnscheck` is exercised. CC is `ZZ`, registry `lab`.
- `Run` creates its listener before it returns, so it can be called inside a network namespace (`netns.Do`).
- Pure Go with no OS-specific code, so its tests run on any OS without root.

## Config

Mappings from the lab profiles.

## Errors

Listen failures.

## Testing

Query `TXT` through the real parser (the analyzer's public `LookupASN` with a resolver backed by `TXT`); `GET` the echo over loopback.

## Out of scope

Anything real-internet. DNS serving (`lab/fakedns`).

## Bootstrap (M0)

`doc.go` only.
