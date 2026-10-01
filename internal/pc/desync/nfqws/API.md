# Module: nfqws

| Field | Value |
|---|---|
| Path | internal/pc/desync/nfqws |
| Kind | child |
| Binary | – |
| Purpose | renders nfqws2 (zapret2) arguments, hostlist and nftables rules |
| Consumers | internal/pc/desync |
| Children | – |
| Uses libs | – |
| Build tags | – |
| Status | planned |
| Pinned | zapret2 1.0.5.2 (`third_party/manifest.yaml`) |

## Responsibility

Knows the command line of nfqws2 (zapret2) and the nftables rules that feed it. Strategies are Lua, as in winws2: Lua libraries are loaded with `--lua-init`, and `Strategy` holds the profile arguments unchanged.

## Public API

```go
package nfqws

type Input struct {
    Binary       string   // path to nfqws2
    LuaDir       string   // default: <dir of Binary>/lua
    LuaInit      []string // default zapret-lib.lua, zapret-antidpi.lua
    Strategy     []string // zapret2 profile args, passed through verbatim
    Hostlist     []string
    Uplink       string // oifname
    QueueNum     uint16 // default 200
    ExcludeIPs   []netip.Prefix
    ExcludePorts [][2]uint16
    Ports        []uint16
    Mark         uint32 // mark nfqws2 puts on its own packets (avoid loops); default 0x40000000 (zapret2 default)
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
- Following the zapret2 README: a `postrouting` chain (priority 101) matches `oifname`, TCP dport `Ports`, not `ExcludeIPs`, not `ExcludePorts` (sport), `meta mark & Mark == 0`, `ct original packets 1-12` → `queue num QueueNum bypass`; plus an `output` chain (priority -401) that sets `notrack` on packets carrying `Mark`.
- Outgoing only. Queuing replies (a `prerouting` chain plus `sysctl net.netfilter.nf_conntrack_tcp_be_liberal=1`, a global system setting) is out of scope until a strategy needs it.
- Args: `--qnum=QueueNum`, one `--lua-init=@<LuaDir>/<file>` per `LuaInit`, `--hostlist=hostlist.txt`, then `Strategy`.
- Flags must be verified against the **pinned zapret2 version** (1.0.5.2).

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
