# AI review: internal/pc

Checks an AI agent can do on its own: reading code, comparing it with contracts, running tests. Nothing here needs admin rights or changes the network (AGENTS.md hard rules apply).

Related: `REVIEW-DECIDE.md` (agent prepares, human decides), `REVIEW-MANUAL.md` (human only, VM or spare PC).

How to review a module: read its `API.md`, check the code against it, then read the test names (`go test -v ./<pkg> | grep -- "--- "`) to see what is promised. If a test name does not match what the test does, that is a finding.

Output: `docs/REVIEW-NOTES.md`. One line per finding: file:line, what looks wrong, severity (high / medium / low). Do not fix anything in the same pass.

---

## Session 1: automated tests (done)
- [x] `go vet ./... && go test ./...`
- [x] `go test -tags lab ./...`
- [x] `go test -tags integration ./internal/pc/...` (needs `make fetch` done: the pinned sing-box is in `third_party/bin/`)
- [x] `go test -count=20 ./internal/pc/ ./internal/pc/procman/` (tests with timers and processes: must never flake)
- [x] `make cross` (or the four `GOOS=... go build ./...` lines from the Makefile)
- [x] Skim the output for `SKIP` lines: `go test -v ./... | grep SKIP`. Know what is skipped and why.

## Session 3: generated configs
- [x] Open `internal/pc/singbox/testdata/mixed.golden.json`. Walk the rules top to bottom and compare with `singbox/API.md` "Route mapping": own processes, QUIC, block, proxy, tunnel, desync, direct, final.
- [x] DNS section: direct/desync domains go to `dns-direct`, A/AAAA go to FakeIP, HTTPS/SVCB are rejected. List which query types of a tunnel domain can reach the direct DoH server (input for `REVIEW-DECIDE.md` D1).
- [x] Open `desync/winws/testdata/args.golden` and `desync/nfqws/testdata/nft_setup.golden`. Check the `ifIdx`, the excludes (VPS IP present, IPv4 and IPv6) and the ports.

## Session 4: `lib/clashapi` + `monitor`
Files: `lib/clashapi/client.go`, `monitor/detect.go`, `monitor/monitor.go`.
- [x] Does any error or log line contain the secret? (`scrub`, every `fmt.Errorf` in `client.go`)
- [x] Is the default HTTP client really proxy-free and time-limited?
- [x] `monitor/detect.go`: list normal traffic patterns that would trigger `TLS_RST`, `TCP_TIMEOUT`, `STALL_16_20` (input for `REVIEW-DECIDE.md` D2).
- [x] `monitor.poll`: a failed poll must not "close" every connection (test `TestAPIErrorDoesNotCloseEverything`).
- [x] Run `go test -v ./internal/pc/monitor | grep -- "---"` and check the test names match the tests.

## Session 5: `singbox` generator
Files: `singbox/singbox.go` (the whole `Generate` function), `singbox/check.go`.
- [x] Order of rules in code equals the contract.
- [x] `tagged()` forces the tag but does not log or print the outbound.
- [x] `checkLoopback` and `parseDoH` reject what they should (see `TestErrors`).
- [x] Output is deterministic: maps are marshalled with sorted keys, lists come from sorted sources (`TestDeterministic`).

## Session 6: `desync`, `winws`, `nfqws`
Files: `desync/desync.go`, `winws/filter.go`, `winws/winws.go`, `nfqws/nft.go`, `nfqws/nfqws.go`.
- [x] `desync.Build` checks in the documented order: no exclude IP, engine, empty list, virtual uplink. The TUN is never an uplink.
- [x] `winws/filter.go`: the address range maths in `lastAddr` (golden `/30`, `/32`, `/8`, IPv6 cases in `winws_test.go`).
- [x] `nfqws/nft.go`: the `return` rules come before the queue rule; only table `inet splitter` is ever touched (`TestNFTShape`).
- [x] Read the zapret2 docs and README online: `--wf-raw-filter` semantics (ANDed with the interception filter? `ifIdx` valid for outbound packets?) and the nftables example, compared line by line with `nft.go`. Record the result as "documented, still unverified on a real system"; the real check is in `REVIEW-MANUAL.md`.

## Session 7: `procman` (riskiest module)
Files: `procman/procman.go` (whole file), `attr_windows.go`, `attr_linux.go`.
- [x] Walk the life of one process: `Start` → `supervise` → crash → backoff → restart → `Stop`. Where can a goroutine leak or a lock be held while blocking?
- [x] `Stop` during a backoff sleep (test `TestStopWhileBackingOff`).
- [x] `CleanupStale`: it must never kill a same-named process outside `BinDir`, an unreadable path, or itself (test `TestCleanupStale`).
- [x] `attr_windows.go`: the structure layouts for `JobObjectExtendedLimitInformation` on amd64. 32-bit Windows is not supported.
- [x] `attr_linux.go`: the locked OS thread; compare with the contract (Pdeathsig fires when the thread exits).
- [x] Run `go test -count=10 -v ./internal/pc/procman` and make sure it never hangs.

## Session 8: `status` server
Files: `status/status.go`, `status/handlers.go`, `status/assets/app.js`.
- [x] `hostAllowed` (the DNS-rebinding guard) and `checkLoopbackHost`.
- [x] Token compare is constant time; the token is never logged.
- [x] `app.js` writes values with `textContent` only (no `innerHTML`).
- [x] Serve the page on loopback from a throwaway test harness (scratch dir, not committed) and screenshot the layout.

## Session 9: `pc` root, part 1: config and CLI
Files: `config.go`, `secrets.go`, `cli.go`, `logfile.go`, `testdata/*.yaml`.
- [x] `Validate`: is anything missing that would let a bad config start the app?
- [x] `excludeIPs`: the tunnel server is auto-excluded only if it is an IP literal.
- [x] `loadSecrets`: errors never contain file content (`TestLoadSecrets`).
- [x] `checkConfig` in `cli.go`: it never prints secrets and cleans its temp dir.

## Session 10: `pc` root, part 2: lifecycle
Files: `app.go`, `reload.go`, `adapters.go`, then `app_test.go`.
- [x] `Run`: pre-flight → checker → connect → status → loops → wait → shutdown. Compare with `pc/API.md` "Startup order".
- [x] `connectLocked`: the desync engine starts before sing-box; any failure leaves nothing running (`TestConnectFailureRollsBack`).
- [x] `reload`: what restarts when, the 30 s minimum gap, the failed-check path (old file and process stay).
- [x] Locking: `a.op` (serializes connect, disconnect, reload) versus `a.mu` (state). Find any place that holds `a.mu` while calling a child.
- [x] Shutdown order: status, monitor, sing-box, engine, checker (`TestReverseShutdownOrder`).
- [x] List scenarios missing from `app_test.go` in the notes.
