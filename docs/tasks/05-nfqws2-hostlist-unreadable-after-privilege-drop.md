# Fix nfqws2 crash loop: hostlist unreadable after privilege drop

Task card `task_cf17dcbc` (proposed 2026-10-11, not started). Modules: `internal/pc` and `internal/pc/desync/nfqws`.

## Problem

On a real Linux kernel, run as root, the desync engine `nfqws2` is started by the app after the nft table is applied. It exits at once with status 1, and procman restarts it with backoff forever. The app log shows `process name=nfqws2 state=starting err="exit status 1"` every few seconds. The desync route therefore never works.

Reproduced with `nfqws2 --dry-run` and the app's exact arguments:

```
--qnum=200 --lua-init=@.../zapret-lib.lua --lua-init=@.../zapret-antidpi.lua
--hostlist=hostlist.txt --filter-l7=tls --payload=tls_client_hello --lua-desync=multisplit:pos=midsld
```

Result: `Running as UID=2147483647 GID=2147483647`, then `file_open_test: Permission denied` and `cannot access hostlist file '.../hostlist.txt'`, exit 1. With a 0755 directory and a 0644 file the same command prints `command line parameters verified`, exit 0. So the strategy is valid; only the permissions fail.

## Cause

`nfqws2` drops root to uid 2147483647 by default. The flags are `--user=<name>` and `--uid=uid[:gid1,gid2,...]` (see `nfqws2 --help`). It then must read the hostlist. But the app keeps `Paths.Run`, where `hostlist.txt` is written (`internal/pc/app.go`, `writePlanFiles`), owner-only 0700 (see `paths.Ensure` and the owner-only directory rule in `internal/pc/API.md` and the hard rules in root `AGENTS.md`). The Lua files under the binary directory are fine.

Neither `--uid` nor `--user` is set or handled anywhere in `internal/pc/desync/nfqws`.

Found by the lab test `TestRouting` (`lab/routing_test.go`, finding F4, `lab/PLAN.md` under L8).

## Wanted

Make the Linux desync engine start under the app's hardening without weakening it more than needed. Options:

- **(a)** Pass `--uid=0:0` (or `--user=root`) from the nfqws renderer so nfqws2 keeps the app's uid. Simplest, but keeps a root process. The process is already root and confined by the nft rules.
- **(b)** Keep the privilege drop and put only the hostlist in a dedicated directory readable by the dropped uid (a 0755 directory containing just that file, no secrets), with the exact modes set explicitly (umask-proof). The `Run` directory itself stays unchanged.
- **(c)** Pass the `--uid` of a dedicated unprivileged user that the installer creates. Needs packaging, probably out of scope.

This is a security trade-off. Ask the human in the final report if one is needed.

Check `internal/pc/desync/nfqws/API.md` and `internal/pc/desync/API.md`. `Config.Check` forbids certain strategy options; `--uid` and `--user` are currently not forbidden and not set by the app. Do not change public signatures without asking. Update the module `API.md` files in the same change. Add unit and golden tests (no root, no network; goldens are updated only with `-update`).

## Rules

Read root `AGENTS.md` first: one module per task. Done means `go vet ./...`, `go test ./...`, `go test -tags lab ./...` and the cross builds pass. Do not commit.

## How it is verified

The lab test above is re-run in WSL by the main session. The lab currently works around the bug with `--uid=0:0` in the test strategy, and keeps a failing assertion F4 that checks the hostlist directory and file are readable by an unprivileged user.
