# Smoke checklist — splitter (internal/pc)

Manual checks that need a real machine and administrator rights. The automated tests cover the logic with fakes; none of them starts a real TUN or intercepts real packets. Run these before a release and after changing `procman`, `singbox`, `desync` or the lifecycle in `app.go`.

Automated part (no admin rights): `go test -tags integration ./internal/pc/...` generates configs and runs the pinned `sing-box check` on them (`internal/pc/singbox`, `internal/pc` `TestCheckConfigWithPinnedSingBox`). `winws2.exe` asks for elevation even for `--help`, so the renderer test only checks that every emitted flag name occurs in the binary.

## Prepare

1. `make fetch` (pinned binaries into `third_party/bin/<os>-<arch>/`), then `make build`.
2. Put the tunnel outbound (a sing-box outbound as JSON, with `"server"` set to the VPS IP address) into the secrets directory as `tunnel.json`, and write `app.yaml` (candidate list, `desync.strategy`). Paths: `paths.Default("splitter")`, or set `paths.root`.
3. `splitter check-config` must print `config ok` and `sing-box check: ok`.

## Windows (elevated terminal)

1. `splitter run`: the TUN interface appears, `winws2.exe` and `sing-box.exe` run, the log shows `state=connected`.
2. Open the status page (`http://127.0.0.1:9091/`): routes, processes and the network label are shown; `Savings` changes while you browse.
3. Open a domain that is on the `desync` route and one on `tunnel`; check in the status page (or the Clash API) that connections carry the `desync` / `tunnel` tag.
4. End the `splitter.exe` process from Task Manager (kill, not Ctrl+C): `sing-box.exe` and `winws2.exe` must be gone within a few seconds (Job Object).
5. Start again: no `already running` error (stale processes under `third_party/bin` are cleaned up at start).
6. Switch Wi-Fi to Ethernet (or the other way): after about 3 s the log shows `network change`, and `winws2.exe` is restarted with the new interface index.
7. `splitter run --ui --parent-pid <pid of a notepad>`: starts disconnected; closing that notepad shuts splitter down.
8. A non-elevated `splitter run` exits with code 2 and prints how to start elevated.

## Linux (root)

1. `splitter run`: if the `inet splitter` table is missing it exits with code 2 and prints the `nft` commands. Apply them as printed, run again.
2. `nft list table inet splitter` shows the queue rule for the uplink; `ps` shows `nfqws2` and `sing-box`.
3. Kill `splitter` with `kill -9`: both children must die (Pdeathsig).
4. Remove the rules with the printed teardown command (`nft delete table inet splitter`); nothing else in `nft list ruleset` changed.
5. Run `go test ./internal/pc/procman` as a normal user: the Linux-only tests (`TestStopTimeoutKills`, kill-on-parent-exit) have only been compile-checked on Windows so far.
