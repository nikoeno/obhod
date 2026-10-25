# Fix missed table update between Connect and reloadLoop

Task card `task_84c3a917` (proposed 2026-10-11, not started). Module: `internal/pc`.

## Problem

`App.Run` (`internal/pc/app.go`, around lines 255-300) starts the checker and takes `a.setTable(a.d.checker.Table())`. At that moment the table is the default one, because the network is not identified yet. Then `Run` calls `Connect`, which takes 1-2 s (sing-box start). Only after that does `reloadLoop` (`internal/pc/reload.go`) call `checker.Subscribe()`.

`Subscribe` delivers only later changes ("latest-wins, buffered 1" in `internal/lib/checker/API.md`). A table published while `Connect` runs is never seen. Typically that is the stored decisions restored once netwatch identifies the network, about 1 s after start.

Observed in a real-kernel run:

- `/api/status` shows the seeded routes (desync 2, tunnel 2, ...) with `Recent` empty.
- The app log has no reload line for 90 s.
- The running sing-box config and the desync engine are still the defaults.

So the tunnel runs on default routing (everything through the tunnel, no desync engine) instead of the stored decisions until some unrelated later event triggers a reload.

A related consequence: the documented exit 2 at start for a missing nft table (`internal/pc/API.md`, startup step 3) cannot happen in practice, because the first table never has desync domains.

Found by the lab test `TestRouting` (`lab/routing_test.go`, finding F3, `lab/PLAN.md` under L8).

## Wanted

No missed updates. For example: subscribe before `Connect` and hand the channel to `reloadLoop`, and/or after subscribing compare `checker.Table()` with the table the running config was built from and arm the debounce if they differ.

Also decide and document what the startup contract should say. Either exit 2 only happens on a later reload, as a logged error with the commands, or the app waits briefly for network identification before the first table. Update `internal/pc/API.md` in the same change. Do not change public signatures without asking.

Add unit tests with the fakes already used in `internal/pc` (fake checker, fake clock; no root, no network).

## Rules

Read root `AGENTS.md` first: one module per task. Done means `go vet ./...`, `go test ./...`, `go test -tags lab ./...` and the cross builds pass. Do not commit.

Cards 02 and 03 touch `internal/pc/deps.go`. Keep this change focused on `app.go` and `reload.go`.

## How it is verified

The lab test above is re-run in WSL by the main session. It currently works around the bug by bouncing the client link to force a reload, and keeps a failing assertion F3 as the signal.
