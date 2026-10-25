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
| Status | implemented |
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
    NFTSetup []string // e.g. "nft add table inet splitter" …, then the ip rule exemption (printed for the human)
    NFTDown  []string // "nft delete table inet splitter", then the matching ip rule del
}

func Render(in Input) (Output, error)
```

## Behavior / contract

- Rules live in their own `inet splitter` table, so teardown is one command and never touches other tables.
- The setup is re-runnable: `nft add table inet splitter` is followed by `nft flush table inet splitter`, so running the commands again (for example after an uplink change) replaces the rules instead of appending a second copy. Nothing outside our table is flushed.
- Following the zapret2 README: a `postrouting` chain (priority 101) matches `oifname`, TCP dport `Ports`, not `ExcludeIPs`, not `ExcludePorts` (sport), `meta mark & Mark == 0`, `ct original packets 1-12` → `queue num QueueNum bypass`; plus an `output` chain (priority -401) that sets `notrack` on packets carrying `Mark`.
- Routing exemption: the last `NFTSetup` command is `ip rule add fwmark 0x40000000/0x40000000 lookup main priority 8999` and the last `NFTDown` command is the same with `ip rule del`. The mark is the value of `Mark` (the one used in the nft rules and in `--fwmark`, a single value). Reason: with sing-box `auto_route`, the rule `not from all iif lo lookup 2022` (priority 9003) sends every locally generated packet that is not bound to a device into the TUN, and nfqws2 injects its pieces through raw sockets that carry only the mark; priority 8999 is evaluated before sing-box's 9000+ rules and sends marked packets through the main table (the default route via the uplink). IPv4 only: the plan has no IPv6 switch, and `ip -6` policy routing may be absent on the host. The `ip rule` line is not idempotent (a second `add` creates a duplicate); the app prints it only when the listing lacks it. The two lists therefore hold `ip` commands as well as `nft` ones (the field names are kept).
- Outgoing only. Queuing replies (a `prerouting` chain plus `sysctl net.netfilter.nf_conntrack_tcp_be_liberal=1`, a global system setting) is out of scope until a strategy needs it.
- Args: `--qnum=QueueNum`, one `--lua-init=@<LuaDir>/<file>` per `LuaInit`, `--hostlist=hostlist.txt` (relative to the engine's working directory), `--uid=0:0`, then `Strategy`.
- Flags must be verified against the **pinned zapret2 version** (1.0.5.2).

- Rules are emitted as `nft '<statement>'` lines so that braces and semicolons survive a shell. Excluded destination addresses and source ports are early `return` rules in the postrouting chain, before the queue rule: a match such as `ip daddr != X` inside the queue rule would exclude every IPv6 packet from queuing.
- `Mark` 0 means the default `0x40000000`; `--fwmark=` is passed only for a non-default mark (flag name from the zapret2 README, not verified: the packed `nfqws2` binary shows no strings). `QueueNum` 0 means 200. `Ports` default to 80 and 443 (sorted).
- `Uplink` must match `[A-Za-z0-9_.:@+-]{1,15}`; anything else is an error (it ends up inside an nft string).
- Identity: the arguments always carry `--uid=0:0` (`Identity`), after the app-owned options and before `Strategy`, so nfqws2 keeps the uid of the app (root) and does not drop to uid 2147483647. Evidence (lab, real kernel): started as root with the default drop, nfqws2 logged `Running as UID=2147483647 GID=2147483647`, then `file_open_test: Permission denied` and `cannot access hostlist file '<absolute path of run/hostlist.txt>'`, exit 1 (crash loop). nfqws2 resolves `--hostlist` to an absolute path and, after the drop, needs search (x) on every ancestor directory of it (and re-checks the file at runtime). A readable hostlist would therefore need every ancestor of `Paths.Run` world-searchable, including directories the app does not own (a home directory when `paths.root` points there), which is rejected. A hostlist subdirectory with 0755/0644 was tried and does not help.
- Exported beyond the contract: `ErrInvalid`, `HostlistName`, `Identity`, `DefaultMark`, `DefaultQueueNum`.
- The commands were not run against a real kernel; the lab (Level 3) owns that test.

## Config

None.

## Errors

Invalid input.

## Testing

Golden args/rules; used by the lab (Level 3) for real packet tests.

## Out of scope

Applying nft rules (a human does it or the lab does it in a namespace).

## Bootstrap (M0)

`doc.go` only. (Implemented in M5.)
