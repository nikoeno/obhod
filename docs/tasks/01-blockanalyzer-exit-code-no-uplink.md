# Fix blockanalyzer exit code when no uplink exists

Task card `task_cd773aee` (proposed 2026-10-11, not started). Module: `internal/blockanalyzer`.

## Problem

`internal/blockanalyzer/API.md` says the exit codes are: 0 report produced; 1 usage/config error; 2 calibration failed (network down).

With every network link down, `netif` finds no physical uplink. `newDialer()` (`internal/blockanalyzer/main.go`, around line 149) fails with `netif: no physical uplink found` (`ErrNoUplink`), and the CLI exits 1 with "cannot set up the direct dialer" and no report. A user whose network is completely down is told their config is broken.

Found by the lab test `TestBlockanalyzer/calibration_fails:_client_link_down` (`lab/blockanalyzer_test.go`), recorded in `lab/PLAN.md` under L6.

## Wanted

- A missing uplink (`ErrNoUplink`, and similar "network down" errors from the dialer setup) is treated as calibration failed: exit 2.
- When `--json` is given, write a schema-1 report with `calibration.internet = "failed"` and no suggestions (analyzer `DESIGN.md` sections 6 and 8).
- Real config errors (bad flags, bad config file, unknown fingerprint) stay exit 1.
- Add a unit test with a fake dialer factory that returns `ErrNoUplink`.

## Rules

Read root `AGENTS.md` first: one module per task, `API.md` is the contract, no admin rights. Do not change public signatures without asking. Done means `go vet ./...`, `go test ./...`, `go test -tags lab ./...` and the cross builds (`make cross`) pass. Do not commit.

## How it is verified

The lab test above is re-run in WSL by the main session. It should then pass.
