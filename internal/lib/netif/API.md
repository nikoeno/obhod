# Module: netif

| Field | Value |
|---|---|
| Path | internal/lib/netif |
| Kind | lib |
| Binary | – |
| Purpose | finds the physical uplink interface and binds sockets to it, bypassing TUN adapters |
| Consumers | internal/lib/analyzer/dialer, internal/lib/checker/netwatch, internal/pc/desync |
| Children | – |
| Uses libs | internal/lib/clock |
| Build tags | – |
| Status | implemented (SSID is always empty; GatewayMAC is Linux only) |

## Responsibility

OS networking facts: which interface really carries traffic to the internet (ignoring wintun/tun devices), its gateway, and socket options that pin a socket to it. Also change notifications for network switches.

## Public API

```go
package netif

type Interface struct {
    Name       string
    Index      int
    Addrs      []netip.Prefix
    Gateway    netip.Addr       // invalid if unknown
    GatewayMAC net.HardwareAddr // nil if unknown
    SSID       string           // "" if not Wi-Fi or unknown
    Virtual    bool             // TUN/TAP/wintun/loopback
}

// Uplink returns the physical interface of the default IPv4 route, skipping virtual ones.
func Uplink(ctx context.Context) (Interface, error)

func List(ctx context.Context) ([]Interface, error)

// BindControl returns a net.Dialer.Control func that pins the socket to ifc
// (Windows: IP_UNICAST_IF / IPV6_UNICAST_IF; Linux: SO_BINDTODEVICE).
func BindControl(ifc Interface) func(network, address string, c syscall.RawConn) error

// MarkControl sets SO_MARK (Linux only; ErrUnsupported elsewhere).
func MarkControl(mark uint32) func(network, address string, c syscall.RawConn) error

type Event struct {
    At   time.Time
    Kind EventKind // UplinkChanged, AddressChanged, LinkDown
}

// Watch reports routing/address changes. Implementations may poll; events may be coalesced.
func Watch(ctx context.Context) (<-chan Event, error)
```

## Behavior / contract

- Virtual detection: by adapter type/driver on Windows (wintun, TAP), and by `/sys/class/net/<if>` type plus name heuristics (`tun*`, `wg*`, `sing*`) on Linux.
- `Uplink` fills Gateway (default IPv4 route) and, on Linux, GatewayMAC (from the ARP table); SSID is best effort and currently always "". Windows: Gateway only (GetAdaptersInfo). Other OSes: empty.
- `Watch` polls every 2 s (snapshot of uplink name/index, addresses, gateway, link up); one event per poll, highest-priority change wins (LinkDown, UplinkChanged, AddressChanged). It guarantees at least one event within a few seconds of an uplink change. Consumers must debounce.
- `SO_BINDTODEVICE` and `SO_MARK` need `CAP_NET_RAW` / `CAP_NET_ADMIN`. Without them, return `ErrPermission`; never escalate.

## Config

None (callers pass what they need).

## Errors

`ErrNoUplink`, `ErrPermission`, `ErrUnsupported`.

## Testing

Pure parts (virtual detection from fixtures, route table parsing) are unit-tested. Binding is covered in the Linux lab (a socket bound to the uplink bypasses a default route via a fake TUN). The Windows part is covered by windows-smoke.

## Out of scope

Changing routes or interfaces. Firewall rules.

## Bootstrap (M0)

`doc.go` only.
