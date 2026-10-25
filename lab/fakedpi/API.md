# Module: fakedpi

| Field | Value |
|---|---|
| Path | lab/fakedpi |
| Kind | lab |
| Binary | – |
| Purpose | emulates TSPU-like blocking on packets via NFQUEUE inside a lab namespace |
| Consumers | lab |
| Children | – |
| Uses libs | – |
| Build tags | – (queue I/O: linux) |
| Status | implemented (lab L4a, L4b) |

## Responsibility

A deliberately simple censor, good enough to tell desync-works from tunnel-needed in tests. It sits in an ISP namespace on the forward path, sees every client packet through NFQUEUE, and logs what it saw, so a scenario can also assert that a tunnel domain's SNI never crossed the ISP.

## Public API

```go
package fakedpi

type Kind string

const (
    NaiveSNI    Kind = "naive-sni"    // match SNI only in a single, unfragmented ClientHello → RST
    FirstHello  Kind = "first-hello"  // decide on the first TLS record seen, even if it is a fake one
    Reassemble  Kind = "reassemble"   // reassemble split ClientHello → RST (defeats simple split)
    Stall       Kind = "stall"        // drop server→client data after N bytes for matching flows
    IPBlock     Kind = "ip-block"     // drop SYNs to a prefix
    QUICBlock   Kind = "quic-block"   // drop UDP/443
)

type Rule struct {
    Kind    Kind
    Domains []string     // for SNI-based kinds and stall (suffix match)
    Prefix  netip.Prefix // for ip-block / stall
    Bytes   int          // for stall
}

type Verdict string

const (
    Accept Verdict = "accept"
    Drop   Verdict = "drop"
    Reset  Verdict = "reset" // drop, and inject RSTs to both ends
)

// Decision is what the engine does with one packet.
type Decision struct {
    Verdict Verdict
    Rule    Kind     // the rule that fired; "" for Accept without a match
    Inject  [][]byte // raw IP packets to send (the RSTs), in order
}

// Flow is one connection as the censor saw it.
type Flow struct {
    Proto    string         // "tcp" | "udp"
    Src, Dst netip.AddrPort
    SNI      string         // first SNI parsed (single record or reassembled); "" if none
    Hellos   int            // ClientHello records seen (fakes count too)
    Rule     Kind           // the rule that acted on the flow; "" if none
    Verdict  Verdict        // the strongest verdict taken on the flow
    First    time.Time
}

type Engine struct{ /* ... */ }

// New returns the pure packet engine (any OS, no root).
func New(rules []Rule) *Engine
func (e *Engine) Process(pkt []byte, now time.Time) Decision // one raw IPv4/IPv6 packet, either direction
func (e *Engine) SetRules(rules []Rule)                      // atomic swap; flows keep their state
func (e *Engine) Stats() map[Kind]int                       // packets acted on per rule
func (e *Engine) Seen() []Flow                              // every flow so far, oldest first (a copy)

// Linux only. Listen binds NFQUEUE queue and opens the raw socket for injected packets, both in the
// calling thread's network namespace (call it inside netns.Do). Run feeds every queued packet through
// Process, sets the verdict and sends Inject; it returns when ctx is done.
func (e *Engine) Listen(queue uint16) (*Queue, error)

type Queue struct{ /* ... */ }

func (q *Queue) Run(ctx context.Context) error
func (q *Queue) Close() error // idempotent
```

## Behavior / contract

- Per-flow state keyed by 5-tuple, with a short TTL (2 min idle). Both directions map to the same flow.
- Injected RSTs use correct sequence numbers so both stacks accept them.
- `NaiveSNI` reads only a ClientHello that fits one TCP segment; `FirstHello` acts on the first TLS record of the flow even when it is a fake one (low TTL or bad checksum ones included), so a fake with a harmless SNI lets the real one pass; `Reassemble` buffers the client's first bytes until the ClientHello is complete (up to 16 KiB) and then matches.
- IPv4 and IPv6 are parsed; QUIC is matched by port only.
- The engine never runs `nft` or `ip`: the `lab` root installs the queue rule (`forward` chain of the ISP namespace, `queue num <n>`), so this module stays free of process execution.
- NFQUEUE through `github.com/florianl/go-nfqueue` (approved 2026-10-11), pinned as module `github.com/florianl/go-nfqueue/v2` v2.1.0 with its `mdlayher/netlink`, `mdlayher/socket`, `golang.org/x/sync` and `golang.org/x/sys` dependencies; imported only by this module's `*_linux.go` files.

## Config

Rules from lab profiles.

## Errors

Queue bind failures (missing `nfnetlink_queue`, not root), raw socket failures.

## Testing

`Process` on packet fixtures, without root and on any OS: ClientHello parsing, a clean hello vs the split and fake-packet patterns nfqws2 produces against each rule, reassembly limits, stall counting, RST sequence numbers, the `Seen` log. The queue path is tested by the `lab` root (it needs namespaces from the sibling `lab/netns`).

## Out of scope

Realistic performance, anything outside lab namespaces.

## Bootstrap (M0)

`doc.go` only.
