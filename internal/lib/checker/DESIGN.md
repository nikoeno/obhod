# Design notes — checker

Status: M1–M7 implemented. `API.md` stays the contract; this file records the decisions taken while implementing it, the deviations from the original plan, and what is still open. The milestone plan itself lives in `PLAN.md` (only M8 remains).

## Decisions on the plan's open questions

| # | Question | Decision |
|---|---|---|
| Q1 | Resolver for netwatch | `Deps.Resolver analyzer.Resolver`; nil → gateway IDs only |
| Q2 | Store labels / LastSeen | `Put` sets `LastSeen`; new `Store.Touch(net, label)` |
| Q3 | Store warnings | `Store.Warnings() []string` (a method, `Open` keeps `(Store, error)`) |
| Q4 | Default for undecided candidates | `Config.UnknownDefault` (tunnel), source `default` |
| Q5 | 0600 on Windows | store does not import `paths`; `Chmod(0600)` on Linux only, elsewhere the file inherits the data dir ACL from `paths.Ensure` |
| Q6 | Unknown network | no probing until `Known`; the identification triggers the check; the default table is still published |
| Q7 | Prune | `Config.Retention` (default 90 days); `Run` prunes at start (networks unseen for that long, decisions of domains that are no longer candidates) |
| Q8 | Test fake for apps | exported `checker.FakeNetwork` (`NewFakeNetwork`, `Set`) |
| Q9 | Config re-exports | type aliases `ListsConfig`, `ScheduleConfig`, `DecidePolicy`, `StoreConfig`, `NetwatchConfig`; `Config` uses them |
| Q10 | NextCheck | `Scheduler.Next()` feeds `Status.NextCheck` |
| Q11 | Live failures vs. hysteresis | a failure never counts as a confirmation; it only triggers a recheck |

Other API additions: `model.Decision.Clone()`; `store.ErrClosed`.

## Deviations from the plan

- **`MinGap` exemption.** Manual checks (`CheckNow`) and targeted rechecks (`Domains != nil`) go out immediately. The plan exempted only network changes; delaying a user-initiated check by up to 5 minutes seemed wrong. Merged triggers use the merged kind, so a timer trigger merged with a manual one is also immediate.
- **Scheduler state.** A mutex plus a wake channel instead of an event channel, so `Failure` and friends never block and never drop events. A trigger counts as in flight from the moment it is offered on `Triggers()` and is withdrawn (merged back) if another event arrives before the checker receives it; this avoids a race between `Done` and the bookkeeping. Emitting a full check restarts the periodic countdown.
- **Netwatch keeps the ID on failure.** When the ASN lookup fails but the uplink (interface, gateway, gateway MAC) is unchanged, the previous ID is kept even if a gateway hash is available, so a flaky echo service cannot flip the network between `asn:` and `gw:`. The gateway hash needs a MAC or SSID; a bare gateway IP is not an identifier.
- **No default echo endpoints.** No server addresses are hardcoded. With `EchoEndpoints` empty and `Deps.PublicIP` nil, the ASN path is off. Echo requests bypass any system proxy and errors never contain the URL.
- **Store `LastSeen` persistence.** `LastSeen` alone is updated in memory and written with the next real write (saves flash); `Touch` writes only when the label changes or the network is new.
- **Expiry handling.** At start and on a network switch, expired decisions get one targeted recheck; afterwards a timer fires at the earliest upcoming expiry. An inconclusive recheck is not retried by this path (that would loop); the periodic check covers it.
- **Clock quirk.** `clock.Fake.Reset` after `Stop` registers the same timer twice and inflates `Waiters()`. The checker code uses `Reset` on Go 1.23+ timers without `Stop`/drain and creates a fresh timer instead of stop-then-reset where a fake clock is involved. `clock` itself was not touched.

## Not done / not verified

- **M8 lab scenario** (`//go:build lab`): blocked on `lab/fakedpi` and `lab/fakeasn`. May move into the lab's own plan.
- **`-race`** was not run: the race detector needs cgo and the development machine has no gcc. Run `go test -race ./internal/lib/checker/...` on a machine with a C toolchain (e.g. in CI).
- **No goroutine-leak check.** Tests cancel and wait for `Run` to return, but do not compare goroutine counts.
- **Untested paths:**
  - the default echo lookup with `netif.BindControl` (only `fetchIP` is tested, against `httptest`);
  - the root's netwatch adapter (`adapter.go`);
  - store write-failure paths, immediate and coalesced (the code exists);
  - store file permissions on Windows.
- **In-flight probes are not cancelled on a network change.** The results are dropped after the probe finishes (the plan only required the drop). Cancelling early would need a watcher goroutine.
- **Windows network identification** works only through the ASN path until `netif` reports a gateway MAC or SSID there.
