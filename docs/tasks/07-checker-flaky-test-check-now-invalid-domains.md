# Flaky test: TestCheckNowIgnoresInvalidDomains

Task card (found 2026-10-14, not started). Module: `internal/lib/checker`.

## Problem

`go test ./internal/lib/checker/ -run TestCheckNowIgnoresInvalidDomains -count=300` failed once in 300 runs (it passed 20 of 20 and 3 of 3 in other runs, and the whole package passed 40 times in a row). The failure:

```
checker_test.go:562: unexpected probe run: [{Domain:a.com Port:0 Path: BulkPath: IP:invalid IP} {Domain:b.com Port:0 Path: BulkPath: IP:invalid IP}]
```

The test expects `CheckNow` with invalid domains to be ignored, but the runner was called for `a.com` and `b.com`. The same failure also showed up once in a full `go test ./...` run earlier in the session.

## Suspects

Either the test is racy with the scheduler's startup or timer trigger (a legitimate check running at the same time as `CheckNow`, so the test sees "unexpected" targets), or `CheckNow` really lets a trigger through when it races with another one. `internal/lib/checker/DESIGN.md` says manual checks and targeted rechecks are exempt from `MinGap`, and triggers arriving during a run are merged.

## Wanted

- Find out which it is. Read `checker_test.go` around line 562, `loop.go` and `schedule/` for how a manual trigger merges with the startup trigger.
- If the test is racy, make it deterministic with the fake clock and the fake runner (no sleeps). If the checker is wrong, fix it and keep the contract in `internal/lib/checker/API.md` (`CheckNow`: "schedules an immediate check of the given domains (all candidates if none)").
- Run the test with `-count=1000` before and after, and report both results.

## Rules

Read root `AGENTS.md` first: one module per task, `API.md` is the contract. The race detector needs cgo, which is not available on the Windows machine; it can run in WSL (`gcc` is installed there) if a human runs it. Done means `go vet ./...`, `go test ./...`, `go test -tags lab ./...` and the cross builds pass. Do not commit.
