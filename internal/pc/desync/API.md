# Module: desync

| Field | Value |
|---|---|
| Path | internal/pc/desync |
| Kind | child |
| Binary | – |
| Purpose | builds the local desync engine plan (zapret2: winws2 or nfqws2) for the desync domains |
| Consumers | internal/pc |
| Children | internal/pc/desync/winws, internal/pc/desync/nfqws |
| Uses libs | internal/lib/route, internal/lib/netif |
| Build tags | – |
| Status | planned |

## Responsibility

One interface for "mangle packets for these domains only", with the engine chosen per OS. It enforces the safety exclusions: never touch the TUN, the VPS IP, or probe ports.

## Public API

```go
package desync

type Engine string

const (
    WinWS Engine = "winws2" // Windows (zapret2)
    NFQWS Engine = "nfqws2" // Linux (zapret2)
)

type Config struct {
    Engine       Engine   // default by GOOS
    Binary       string
    Strategy     []string // zapret2 profile args (e.g. --filter-l7=tls --payload=tls_client_hello --lua-desync=...)
    LuaInit      []string // Lua libraries to load; default zapret-lib.lua, zapret-antidpi.lua
    ExcludeIPs   []netip.Prefix // VPS IP(s); required
    ExcludePorts []PortRange    // probe source-port ranges (future)
    Ports        []uint16       // TCP ports to act on; default 443, 80
}

// PortRange is an inclusive port range: [0] is the first port, [1] the last.
// The children (winws, nfqws) take the same shape as [][2]uint16.
type PortRange [2]uint16

type Plan struct {
    Engine   Engine
    Path     string
    Args     []string
    Files    map[string][]byte // name → content (hostlist), written to Run dir by the app
    Setup    []string          // commands a human must run once (nfqws: nftables rules); never run by us
    Teardown []string
}

// Build creates the plan for the desync domains in t, pinned to the physical uplink.
func Build(cfg Config, uplink netif.Interface, t route.Table) (Plan, error)
```

## Behavior / contract

- Only `t.Domains(route.Desync)` go into the hostlist. An empty list → a valid plan that the app doesn't start.
- Uplink-only: winws gets a WinDivert filter limited to the uplink interface index; nfqws gets nftables rules limited to the uplink `oifname`.
- `ExcludeIPs` must not be empty (it protects the tunnel). Missing → error.

## Config

See above.

## Errors

`ErrNoExcludeIP`, `ErrUnsupported` (engine not available on this OS).

## Testing

Delegation to children, exclusion enforcement, empty-list behavior.

## Out of scope

Choosing strategies (future strategy finder), running processes.

## Bootstrap (M0)

`doc.go` only.
