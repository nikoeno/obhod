# Manual checks: internal/pc

Checks only a human can do: they need admin/root, change the network of the machine, or need a real VPS. **Sessions 11-13 change the network: do them in a VM or on a spare PC** (recovery steps: `docs/RECOVERY.md`).

Related: `REVIEW-AI.md` (agent only), `REVIEW-DECIDE.md` (agent prepares, human decides).

Write findings into `docs/REVIEW-NOTES.md`, same format as the AI pass.

---

## Session 2: the CLI as a user (done)
- [x] `make build`, then `bin\splitter --help` and `--version`.
- [x] Create a test directory with `app.yaml` (`paths: {root: <test dir>}`, `desync: {strategy: ["--filter-l7=tls"]}`) and a fake `tunnel.json` (copy the sample from `internal/pc/singbox/check_integration_test.go`).
- [x] `bin\splitter check-config --config <test dir>\app.yaml`: expect `sing-box check: ok` and `config ok`.
- [x] Break it on purpose, one at a time, and confirm the exit code is 1 with a clear message: unknown key, `status.listen: 0.0.0.0:9091`, `checker.unknowndefault: direct`, a missing `tunnel.json`, `"server": "vpn.example.org"` without `desync.exclude_ips`.
- [x] `bin\splitter run` from a **non-admin** terminal: expect exit code 2 and the "run as administrator" message. Nothing may start.
- [x] Confirm no secret appears in any output (`tunnel.json` uuid, public key).

## nft syntax check (Linux VM or WSL)
- [ ] Paste the commands from `desync/nfqws/testdata/nft_setup.golden` into `nft -c -f` (check mode, applies nothing; may need root). Strip the `nft '...'` wrapper first.

## Own read of the riskiest code
The code was AI-written; the AI pass is a second opinion, not a replacement for understanding it.
- [ ] `procman/procman.go`, especially `CleanupStale` (session 7 in `REVIEW-AI.md`).
- [ ] `app.go` + `reload.go` lifecycle and locking (session 10 in `REVIEW-AI.md`). Draw the startup order on paper.

---

## Part C: run it for real (VM or spare PC only)

Take a VM snapshot first. Have `docs/RECOVERY.md` open on another device.

### Session 11: first real run on Windows (60 min)
Follow `internal/pc/SMOKE.md`, "Windows".
- [ ] Use a real `tunnel.json` for your VPS, a candidate list with 2-3 domains, and an empty `desync.strategy` first (no packet mangling yet).
- [ ] `splitter run` from an elevated terminal: TUN adapter appears, `sing-box.exe` runs, the status page opens at `http://127.0.0.1:9091/`.
- [ ] Browse: a tunnel domain works, a direct domain works, the status page counters move.
- [ ] Kill `splitter.exe` from Task Manager: `sing-box.exe` and `winws2.exe` disappear within seconds; internet works again without a reboot (this tests the cleanup claims in `RECOVERY.md`).
- [ ] Start again: no "already running" or leftover-adapter errors.

### Session 12: desync and network changes (60 min)
- [ ] Add a real `desync.strategy` from the zapret2 docs and one domain that your ISP blocks by SNI, put in the `desync` route (via the decision store or a test candidate).
- [ ] Check connections show the `desync` tag (Clash API: `curl -H "Authorization: Bearer <secret from run\sing-box.json>" http://127.0.0.1:9090/connections`).
- [ ] Confirm `--wf-raw-filter` behaves as the AI pass read it in the docs (session 6 in `REVIEW-AI.md`): only uplink traffic is intercepted, the VPS IP is not.
- [ ] Switch Wi-Fi to Ethernet (or toggle a VPN adapter): the log shows `network change`, the engine restarts with the new interface index, no manual action needed.
- [ ] `splitter run --ui --parent-pid <notepad pid>`, then close Notepad: splitter shuts down by itself.
- [ ] Write down everything surprising. This is the list for the next fixing round.

### Session 13 (optional): Linux (60 min)
Only if you will use Linux. `SMOKE.md`, "Linux": the tests for Pdeathsig, SIGTERM and the nft checks have never run there. Start with `go test ./internal/pc/...` on the Linux box. Verify `--fwmark` of nfqws2 and hostlist re-read (G13) here.
