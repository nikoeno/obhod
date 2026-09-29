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
| Status | planned |

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
func (t *Topology) Link(a, b *NS, aAddr, bAddr netip.Prefix) error
func (t *Topology) Route(ns *NS, dst netip.Prefix, via netip.Addr) error
func (t *Topology) Exec(ctx context.Context, ns *NS, argv ...string) ([]byte, error)
func (t *Topology) Teardown() error // idempotent; removes everything with the prefix
```

## Behavior / contract

- Every created object carries the prefix. `Teardown` also removes leftovers from crashed runs with the same prefix.
- Never touches the root namespace's routes or firewall.

## Config

None.

## Errors

Command failures include argv and stderr.

## Testing

Self-test: create, link, ping, teardown (root only; skipped otherwise).

## Out of scope

Packet manipulation (fakedpi).

## Bootstrap (M0)

`doc.go` only (with `//go:build linux`).
