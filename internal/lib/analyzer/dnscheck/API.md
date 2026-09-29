# Module: dnscheck

| Field | Value |
|---|---|
| Path | internal/lib/analyzer/dnscheck |
| Kind | child |
| Binary | – |
| Purpose | resolves via DoH, detects DNS poisoning, FakeIP and ISP stubs, and looks up ASNs |
| Consumers | internal/lib/analyzer |
| Children | – |
| Uses libs | internal/lib/analyzer/lib/verdict |
| Build tags | – |
| Status | design ready |

## Responsibility

Everything DNS: the trusted resolver used by probes, the system-vs-DoH comparison, stub learning, and ASN lookup.

## Public API

```go
package dnscheck

// DialFunc is how this package reaches the network (injected; it doesn't import dialer).
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

type Resolver interface {
    LookupIP(ctx context.Context, host string) ([]netip.Addr, error) // A (+AAAA if enabled)
    LookupTXT(ctx context.Context, name string) ([]string, error)
}

func NewDoH(endpoints []string, dial DialFunc, fingerprint string) (Resolver, error)
func NewSystem() Resolver
func NewFake(answers map[string][]netip.Addr, txt map[string][]string) Resolver

type Check struct {
    Host    string
    System  []netip.Addr
    DoH     []netip.Addr
    Verdict verdict.Verdict // OK | DNS_POISONED | DNS_FAIL
    FakeIP  bool            // system answer in 198.18.0.0/15 (local TUN client)
    Reason  string
}

// Compare resolves host with both resolvers and classifies the difference.
func Compare(ctx context.Context, host string, sys, doh Resolver, stubs *Stubs) Check

// Stubs are ISP block-page IPs/hosts learned during calibration.
type Stubs struct{ /* ... */ }

func NewStubs() *Stubs
func (s *Stubs) AddIP(ip netip.Addr)
func (s *Stubs) AddHost(host string)
func (s *Stubs) HasIP(ip netip.Addr) bool
func (s *Stubs) HasHost(host string) bool

type ASN struct {
    Number  uint32
    Name    string
    Country string
    Prefix  netip.Prefix
}

// LookupASN queries Team Cymru (origin.asn.cymru.com + asn.cymru.com TXT) through r.
func LookupASN(ctx context.Context, r Resolver, ip netip.Addr) (ASN, error)
```

## Behavior / contract

- "Different answers" is not poisoning by itself (CDNs differ). Report poisoning only when the system answer is a known stub, a bogon/private address, or NXDOMAIN while DoH resolves.
- FakeIP is informational, never `DNS_POISONED`.
- DoH over HTTPS uses the injected dialer and a uTLS fingerprint.

## Config

None directly; the analyzer passes endpoints and dial.

## Errors

Resolver errors wrap the endpoint. `LookupASN` returns `ErrNoASN`.

## Testing

Fake resolvers for every `Compare` outcome; a local DoH test server; parsing of Cymru TXT fixtures.

## Out of scope

Changing system DNS. DoT/DoQ (maybe later).

## Bootstrap (M0)

`doc.go` only.
