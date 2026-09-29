# Module: nfqws

| Field | Value |
|---|---|
| Path | internal/pc/desync/nfqws |
| Kind | child |
| Binary | – |
| Purpose | renders nfqws arguments, hostlist and nftables rules |
| Consumers | internal/pc/desync |
| Children | – |
| Uses libs | – |
| Build tags | – |
| Status | planned |

## Responsibility

Knows nfqws's command line and the nftables rules that feed it.

## Public API

```go
package nfqws

type Input struct {
    Binary       string
    Strategy     []string
    Hostlist     []string
    Uplink       string // oifname
    QueueNum     uint16 // default 200
    ExcludeIPs   []netip.Prefix
    ExcludePorts [][2]uint16
    Ports        []uint16
    Mark         uint32 // mark for packets already processed (avoid loops)
}

type Output struct {
    Path     string
    Args     []string
    Hostlist []byte
    NFTSetup []string // e.g. "nft add table inet splitter" … (printed for the human)
    NFTDown  []string
}

func Render(in Input) (Output, error)
```

## Behavior / contract

- Rules live in their own `inet splitter` table, so teardown is one command and never touches other tables.
- They match `oifname`, TCP `Ports`, not `ExcludeIPs`, not `ExcludePorts` (sport), and not `Mark` → queue.
- Flags must be verified against the **pinned zapret version**.

## Config

None.

## Errors

Invalid input.

## Testing

Golden args/rules; used by the lab (Level 3) for real packet tests.

## Out of scope

Applying nft rules (a human does it or the lab does it in a namespace).

## Bootstrap (M0)

`doc.go` only.
