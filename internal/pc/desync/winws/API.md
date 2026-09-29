# Module: winws

| Field | Value |
|---|---|
| Path | internal/pc/desync/winws |
| Kind | child |
| Binary | – |
| Purpose | renders winws (zapret, WinDivert) arguments and hostlist |
| Consumers | internal/pc/desync |
| Children | – |
| Uses libs | – |
| Build tags | – |
| Status | planned |

## Responsibility

Knows winws's command-line format and nothing else.

## Public API

```go
package winws

type Input struct {
    Binary       string
    Strategy     []string
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

- The WinDivert filter includes: outbound, TCP on `Ports`, `IfIdx == IfIndex`, not `ExcludeIPs`, not `ExcludePorts` (as local port).
- Flag names (`--wf-*`, `--hostlist`, raw filter syntax) must be verified against the **pinned zapret version**.
- Deterministic output.

## Config

None.

## Errors

Invalid input (no domains, zero IfIndex).

## Testing

Golden args/hostlist; filter string unit tests.

## Out of scope

Starting winws, admin rights.

## Bootstrap (M0)

`doc.go` only.
