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
| Status | implemented |

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
    Setup    []string          // commands a human must run once (nfqws: nftables rules, then an `ip rule` exemption for the engine's fwmark); never run by us
    Teardown []string
}

// Build creates the plan for the desync domains in t, pinned to the physical uplink.
func Build(cfg Config, uplink netif.Interface, t route.Table) (Plan, error)
```

## Behavior / contract

- Only `t.Domains(route.Desync)` go into the hostlist. An empty list → a valid plan that the app doesn't start.
- Uplink-only: winws gets a WinDivert filter limited to the uplink interface index; nfqws gets nftables rules limited to the uplink `oifname`.
- nfqws on Linux: `Setup` ends with `ip rule add fwmark 0x40000000/0x40000000 lookup main priority 8999` and `Teardown` with the matching `ip rule del` (see `nfqws/API.md`); the app checks it with `ip rule show` before it starts the engine. winws has no such command.
- `ExcludeIPs` must not be empty (it protects the tunnel). Missing → error.

- Check order in `Build`: `ExcludeIPs` empty → `ErrNoExcludeIP` (before everything, even for an empty hostlist); unknown engine → `ErrUnsupported`; `Config.Check` → `ErrInvalidConfig`; no desync domains → `Plan{Engine}` (nothing to run); virtual uplink → `ErrVirtualUplink`; then the child renders.
- An explicit `Config.Engine` works on any OS (the renderers are pure); only the default depends on `runtime.GOOS`.
- Exported beyond the contract: `ErrVirtualUplink`, `ErrInvalidConfig`, `DefaultEngine()`, `HostlistFile`, `func (Config) Check() error`.
- `Config.Check` rejects a `Strategy` that carries an option the app sets itself (any `--wf*` option, `--new`, `--skip`, `--hostlist*`, `--ipset*`, `--qnum`, `--fwmark`, `--lua-init`, `--uid`, `--user` (nfqws2 runs with `--uid=0:0`, set by the renderer), or an `@file` argument): each could undo the uplink restriction, the tunnel exclusion or the hostlist-only rule. It also rejects port 0, an empty or reversed exclude port range, an invalid prefix and a `LuaInit` name with a path. The app calls it when it loads its config.
- IPv4-mapped IPv6 excludes (`::ffff:a.b.c.d/n`, n ≥ 96) are turned into IPv4 prefixes before rendering; otherwise they would become IPv6 clauses that never match IPv4 packets.

## Config

See above.

## Errors

`ErrNoExcludeIP`, `ErrUnsupported` (engine not available on this OS), `ErrVirtualUplink`, `ErrInvalidConfig`.

## Testing

Delegation to children, exclusion enforcement, empty-list behavior.

## Out of scope

Choosing strategies (future strategy finder), running processes.

## Bootstrap (M0)

`doc.go` only. (Implemented in M6.)
