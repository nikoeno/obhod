# procman: TestStopTimeoutKills fails on Linux

Task card (found 2026-10-14, not started). Module: `internal/pc/procman`.

## Problem

The Linux-only tests of `procman` were only compile-checked on the Windows development machine (see `internal/pc/SMOKE.md`, Linux step 5). Run for the first time on a real Linux kernel (WSL2, Ubuntu 26.04, kernel 6.18), as the unprivileged user `nobody`, `TestStopTimeoutKills` fails:

```
=== RUN   TestStopTimeoutKills
    procman_test.go:294: clock: BlockUntil timed out
--- FAIL: TestStopTimeoutKills (10.00s)
```

The test (`internal/pc/procman/procman_test.go`, line 279) starts a helper process in "slow-stopper" mode (it should ignore SIGTERM), calls `Stop` in a goroutine with `StopTimeout = 5s`, and then waits with `fc.BlockUntil(ctx, 1)` for exactly one waiter on the fake clock (the stop timer). That waiter never appears.

Every other package that was run the same way passes (`netif`, `pc`, `proc`, `paths`, `checker/netwatch`, `checker/store`), and the other `procman` tests pass or skip.

## Suspects

- The test is wrong: the helper's SIGTERM handling, the way the helper is started (`TestHelperProcess` re-executing the test binary), or an assumption about the number of fake-clock waiters on Linux (for example the restart backoff timer or the readiness poll timer also counts).
- `procman` is wrong: on Linux `Stop` sends SIGTERM to the process group and then waits for `StopTimeout` on the injected clock. If `Stop` returns early because the helper exits at once, or the timer is created on a different path, the stop-timeout guarantee ("SIGTERM, then SIGKILL after `StopTimeout`", `procman/API.md`) is not what the tests believe.

## Wanted

- Find out which it is. Read `procman.go` (`Stop`, the Linux signal path, the locked OS thread) and the helper in `procman_test.go`. You cannot run WSL yourself; add temporary diagnostics (for example the number of clock waiters over time, and what the helper does on SIGTERM) to the test, so the main session can run it and show you the output. Remove the diagnostics at the end.
- Fix the test or `procman`. Keep the contract in `internal/pc/procman/API.md` (Linux: SIGTERM to the process group, SIGKILL after `StopTimeout`; Windows terminates at once). If a public signature must change, stop and report.
- The Pdeathsig / kill-on-parent-exit test also exists for Linux: check that it ran and passed in the same run.

## Rules

Read root `AGENTS.md` first: one module per task, `API.md` is the contract. Done means `go vet ./...`, `go test ./...`, `go test -tags lab ./...` and the cross builds pass. Do not commit.

## How it is verified

The main session compiles the test binary in WSL and runs it as `nobody` (`setpriv`): `go test -c ./internal/pc/procman`, then `setpriv --reuid=65534 --regid=65534 --clear-groups ./procman.test -test.v`. It runs the whole package, with `-test.count=20` to look for flakes.
