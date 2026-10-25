# Module: fakedns

| Field | Value |
|---|---|
| Path | lab/fakedns |
| Kind | lab |
| Binary | – |
| Purpose | serves the lab's DoH and plain DNS with a static zone, poisoning and a query log |
| Consumers | lab |
| Children | – |
| Uses libs | internal/lib/analyzer (tests) |
| Build tags | – |
| Status | implemented (lab L3) |

## Responsibility

The only DNS in the lab. In `inet` it is the DoH server the app and the analyzer use (`analyzer.doh`, `singbox.direct_doh`); in each ISP namespace it is the plain resolver the client's system resolver points at, optionally poisoning names like a real ISP. Every query is logged, so a scenario can prove which names reached which server (DNS leak checks).

## Public API

```go
package fakedns

// TXTFunc answers TXT queries the zone does not hold (the lab wires fakeasn.TXT here); nil → NXDOMAIN.
type TXTFunc func(name string) []string

type Config struct {
    Zone     map[string][]netip.Addr // name → A/AAAA answers (exact names, lowercase, no trailing dot)
    Poisoned map[string]netip.Addr   // plain mode only: answer this address instead (suffix match)
    TXT      TXTFunc
    TTL      uint32                  // default 60
}

type Query struct {
    Name   string // lowercase, no trailing dot
    Type   string // "A", "AAAA", "TXT", "HTTPS", ...
    From   netip.AddrPort
    Via    string // "doh" | "udp" | "tcp"
    At     time.Time
}

type Server struct {
    Addr netip.AddrPort
}

// RunDoH serves RFC 8484 (GET and POST on /dns-query) over HTTPS with cert. It stops when ctx is done.
func RunDoH(ctx context.Context, listen string, cert tls.Certificate, cfg Config) (*Server, error)

// RunPlain serves UDP and TCP DNS on the same address. It stops when ctx is done.
func RunPlain(ctx context.Context, listen string, cfg Config) (*Server, error)

func (s *Server) Queries() []Query // oldest first (a copy)
func (s *Server) SetZone(z map[string][]netip.Addr) // atomic swap, for sites added during a scenario
func (s *Server) Close()                           // idempotent
```

## Behavior / contract

- Names outside the zone get NXDOMAIN (TXT goes to `TXT` first). Types other than A, AAAA and TXT get an empty NOERROR answer. Every query is logged, answered or not.
- Poisoning applies to A and AAAA in plain mode only (a DoH server that poisons would not be "DoH resolves while the system resolver lies").
- Built on `github.com/miekg/dns` (approved). The certificate is the caller's (the lab issues it from `testca`), so this module does not import a test-only module.
- `Run*` create their listeners before they return, so they can be called inside a network namespace (`netns.Do`).
- Pure Go with no OS-specific code, so its tests run on any OS without root.

## Config

From the lab: zone from the sites, poisoning from the profiles.

## Errors

Listen failures; an invalid zone name or address.

## Testing

Over loopback: zone answers, NXDOMAIN, TXT hook, poisoning in plain mode only, the query log for DoH, UDP and TCP. The analyzer's DoH client (`analyzer.Config.DoH` pointing at the server, test CA trusted) resolves through it.

## Out of scope

Recursion, DNSSEC, DoT, anything real-internet.

## Bootstrap (M0)

`doc.go` only.
