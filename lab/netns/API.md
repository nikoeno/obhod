# Module: netns

| Field | Value |
|---|---|
| Path | lab/netns |
| Kind | lab |
| Binary | – |
| Purpose | creates and tears down network namespaces, veth links and routes for the lab |
| Consumers | lab |
| Children | – |
| Uses libs | – |
| Build tags | linux |
| Status | implemented (lab L1) |

## Responsibility

Thin, safe wrappers around `ip netns` / `ip link` / `ip route` / `nft` inside namespaces.

## Public API

```go
//go:build linux

package netns

type NS struct{ Name string }

type Topology struct{ /* ... */ }

func New(prefix string) *Topology
func (t *Topology) Add(name string) (*NS, error)

// Link creates a veth pair between a and b and names each end inside its namespace
// (aIf, bIf; e.g. "eth0" on the client side so netif counts it as physical).
func (t *Topology) Link(a, b *NS, aIf, bIf string, aAddr, bAddr netip.Prefix) error
func (t *Topology) AddAddr(ns *NS, ifname string, addr netip.Prefix) error // extra addresses (one per fake site)
func (t *Topology) SetLink(ns *NS, ifname string, up bool) error
func (t *Topology) Route(ns *NS, dst netip.Prefix, via netip.Addr) error
func (t *Topology) DelRoute(ns *NS, dst netip.Prefix) error
func (t *Topology) Forwarding(ns *NS, on bool) error             // net.ipv4/ipv6 forwarding of that namespace only
func (t *Topology) SetResolver(ns *NS, addr netip.Addr) error    // writes /etc/netns/<ns>/resolv.conf

// Exec runs argv in ns (ip netns exec) and returns stdout; Start runs a long-lived process there.
func (t *Topology) Exec(ctx context.Context, ns *NS, argv ...string) ([]byte, error)
func (t *Topology) Start(ctx context.Context, ns *NS, env []string, argv ...string) (*exec.Cmd, error)

// Do runs fn on a locked OS thread switched into ns, then switches back. Sockets created inside fn
// stay in ns after it returns, so listeners and the NFQUEUE socket can be served from any goroutine.
func (ns *NS) Do(fn func() error) error

func (t *Topology) Teardown() error // idempotent; removes everything with the prefix
```

## Behavior / contract

- Every created object carries the prefix. `Teardown` also removes leftovers from crashed runs with the same prefix (namespaces and `/etc/netns/<prefix>*`).
- Never touches the root namespace's routes or firewall. `Forwarding` and nft rules apply only inside the given namespace.
- `Do`: if switching back fails, the thread stays locked so the Go runtime discards it instead of reusing a thread in the wrong namespace. `setns` is a raw syscall; the package still compiles for every Linux target in `make cross`.
- Processes from `Start` are killed in `Teardown` if still running.

## Config

None.

## Errors

Command failures include argv and stderr.

## Testing

Self-test: create, link, ping, a listener opened with `Do` is reachable only from its namespace, teardown leaves nothing and the root namespace's routes and nft ruleset are unchanged (root only; skipped otherwise).

## Out of scope

Packet manipulation (fakedpi).

## Bootstrap (M0)

`doc.go` only (with `//go:build linux`).
