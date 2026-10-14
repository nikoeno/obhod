# Fix stale uplink binding of the checker dialer

Task card `task_b1399485` (proposed 2026-10-11, not started). Module: `internal/pc`.

## Problem

After the client switches uplink (old interface admin-down, new interface up with the default route), every checker calibration fails with "internet check failed", forever. No decision is stored for the new network and the status page stays on defaults.

Evidence from a root run in WSL namespaces:

- A fresh `blockanalyzer --calibrate-only` in the same namespace passes.
- The interface counters show no probe traffic on the new uplink. The old interface is down, and the new one only carries the network-identification echo and ARP.

So the checker's direct dialer stays bound to the old interface.

## Where to look

- `internal/pc/deps.go`, `lazyDirect`: created on first use with `analyzer.NewDirectDialer(BindIface: true)`. That pins sockets with `SO_BINDTODEVICE` to the interface `netif.Uplink()` returned at construction time.
- It is reset only by `resetDialer`, called from `reloadLoop` in `internal/pc/reload.go` when the pc uplink watcher fires. That watcher polls every 2 s, independently of netwatch.
- Likely races: an in-flight probe recreates the dialer between the switch and the route change, or the reset happens before the new uplink is chosen. The stale binding then persists until the next uplink event.

## Wanted

Make a switch of uplink always get picked up without waiting for another event. For example: resolve the uplink per dial or on a short cache, or invalidate on dial failure or on a generation change.

Keep the TUN-bypass property: probes must never enter the TUN (G12 in `internal/pc/PLAN.md`).

Add unit tests with fakes (no root, no network).

## Rules

Read root `AGENTS.md` first: one module per task, `API.md` is the contract. Ask before changing public signatures. Done means `go vet ./...`, `go test ./...`, `go test -tags lab ./...` and the cross builds pass. Do not commit.

Other pc fixes touch `internal/pc/deps.go` too (card 03, DoH resolver for the checker). Keep this change small and rebase if needed.

## How it is verified

Lab test `TestChecker/6_network_change` (`lab/checker_test.go`, finding 2 in `lab/PLAN.md` under L7). The main session re-runs it in WSL. Case 6 should then pass, except for the separate ASN-label finding (card 03).

## Follow-up (2026-10-14): second pinned dialer, the real cause

Card 02's first fix (`internal/pc/direct.go`, re-resolving the checker's probe dialer) did not make the lab case pass: after the fix, `TestChecker/6_network_change` still fails and the app's calibration still fails on the new network, while a fresh `blockanalyzer --calibrate-only` in the same namespace passes.

The remaining pin is in `internal/lib/analyzer/prober.go`, `(*prober).dohDial`: it builds its own direct dialer with `NewDirectDialer(BindIface: true)` once, inside a `sync.Once`, on the first DoH dial, and keeps it for the life of the prober. The checker's analyzer resolves the control domains through DoH using that dialer, so after the uplink changes every DoH lookup is still bound to the old (down) interface and calibration reports "no control domain reachable".

Wanted: `dohDial` must follow the uplink like the checker's dialer does (re-resolve after a TTL, after a failed dial, or through an injected dialer factory), keeping the TUN-bypass property. Audit the other places that build a direct dialer once (grep `NewDirectDialer`, `sync.Once`, `BindIface`): `internal/blockanalyzer/main.go` is one-shot and fine; check `internal/lib/checker/netwatch` and anything else long-lived. No public signature may change. Add a unit test with a fake uplink resolver.
