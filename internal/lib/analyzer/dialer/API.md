# Module: dialer

| Field | Value |
|---|---|
| Path | internal/lib/analyzer/dialer |
| Kind | child |
| Binary | – |
| Purpose | opens probe connections through a given route without being captured by TUN or desync |
| Consumers | internal/lib/analyzer |
| Children | – |
| Uses libs | internal/lib/analyzer/lib/verdict, internal/lib/route, internal/lib/netif |
| Build tags | – |
| Status | implemented |

## Responsibility

Route-specific connection setup behind one interface. Everything above the dialer is route-agnostic.

## Public API

```go
package dialer

type Dialer interface {
    Route() route.Route
    DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

type DirectOptions struct {
    Uplink    netif.Interface // zero value → netif.Uplink() at construction
    Mark      uint32          // Linux: optional SO_MARK
    BindIface bool            // pin sockets to the uplink (bypasses a TUN default route); false = plain dial
}

func NewDirect(o DirectOptions) (Dialer, error)

// Fake: scripted connections for tests.
type Step struct {
    Addr string
    Err  error    // returned from DialContext
    Conn net.Conn // e.g. one end of net.Pipe
}

func NewFake(r route.Route, steps ...Step) Dialer

// Deferred (not implemented now): NewSourcePortRange (desync probe), NewSOCKS5 (tunnel/proxy/ByeDPI).
```

## Behavior / contract

- Setup failures (no uplink, bind not permitted) are returned wrapped with `verdict.Local`, so classify maps them to `LOCAL_ERROR`.
- Network failures (RST, timeout) are returned unwrapped, as the OS gives them.
- The dialer holds no state across dials, except the resolved uplink.

## Config

Via `DirectOptions`.

## Errors

`verdict.LocalErr`-wrapped setup errors; raw network errors.

## Testing

Fake: script order and error passthrough. Direct: dial to a local listener with `BindIface=false`; binding is covered in the lab.

## Out of scope

Desync/tunnel/proxy dialers (deferred), DNS.

## Bootstrap (M0)

Done (M0-M8).
