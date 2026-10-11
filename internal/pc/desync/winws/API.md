# Module: winws

| Field | Value |
|---|---|
| Path | internal/pc/desync/winws |
| Kind | child |
| Binary | – |
| Purpose | renders winws2 (zapret2, WinDivert) arguments and hostlist |
| Consumers | internal/pc/desync |
| Children | – |
| Uses libs | – |
| Build tags | – |
| Status | implemented |
| Pinned | zapret2 1.0.5.2 (`third_party/manifest.yaml`) |

## Responsibility

Knows the command-line format of winws2 (zapret2) and nothing else. zapret2 strategies are Lua: the engine loads the Lua libraries shipped next to the binary, and `Strategy` holds the profile arguments (`--filter-*`, `--payload`, `--out-range`, `--lua-desync=...`) unchanged.

## Public API

```go
package winws

type Input struct {
    Binary       string   // path to winws2.exe (WinDivert.dll, WinDivert64.sys, cygwin1.dll next to it)
    LuaDir       string   // default: <dir of Binary>/lua
    LuaInit      []string // Lua files in LuaDir to load, in order; default zapret-lib.lua, zapret-antidpi.lua
    Strategy     []string // zapret2 profile args, passed through verbatim
    Hostlist     []string // domains
    IfIndex      int      // uplink interface index
    ExcludeIPs   []netip.Prefix
    ExcludePorts [][2]uint16
    Ports        []uint16
}

type Output struct {
    Path     string
    Args     []string
    Hostlist []byte // file content; args reference it by name "hostlist.txt"
}

func Render(in Input) (Output, error)
```

## Behavior / contract

- Argument order: `--wf-tcp-out=<Ports>`, `--wf-raw-filter=<filter>`, one `--lua-init=@<LuaDir>/<file>` per `LuaInit` entry, `--hostlist=hostlist.txt`, then `Strategy`.
- `--wf-raw-filter` is combined with the rest of the interception filter by AND (zapret2 README). It holds `ifIdx == IfIndex`, not `ExcludeIPs` (`ip.DstAddr` / `ipv6.DstAddr`), not `ExcludePorts` (as `tcp.SrcPort`).
- Interception is outbound only (`--wf-tcp-out`), as in the contract's "uplink only" rule. Incoming interception (`--wf-tcp-in`, needed by some `zapret-auto.lua` orchestrators) is out of scope until a strategy needs it.
- Flag names and the WinDivert filter syntax must be verified against the **pinned zapret2 version** (1.0.5.2).
- Deterministic output.

- WinDivert has no CIDR syntax: each exclude prefix becomes an address range (`ip.DstAddr >= a and ip.DstAddr <= b`; a host prefix becomes `==`). `not (...)` on one address family lets the other family through. The raw filter does not repeat `outbound`, `tcp` or the ports; winws2 ANDs it with `--wf-tcp-out`.
- `Ports` default to 80 and 443 and are rendered sorted (`--wf-tcp-out=80,443`). The hostlist is sorted and deduplicated, `
` endings. `LuaInit` names must be plain file names.
- `LuaDir` is joined with `/` (`@<LuaDir>/<file>`); a trailing separator is trimmed.
- All emitted flag names exist in the pinned `winws2.exe`. `winws2.exe` asks for elevation even for `--help`, so the integration test (`-tags integration`, Windows) checks the binary's strings instead of running it. Semantics of the flags were not exercised here.
- Exported beyond the contract: `Filter(ifIndex, excludeIPs, excludePorts)` (the filter builder, for tests and debugging), `ErrInvalid`, `HostlistName`.

## Config

None.

## Errors

Invalid input (no domains, zero IfIndex, empty LuaInit file name).

## Testing

Golden args/hostlist; filter string unit tests.

## Out of scope

Starting winws, admin rights.

## Bootstrap (M0)

`doc.go` only. (Implemented in M4.)
