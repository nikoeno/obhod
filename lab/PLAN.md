# Implementation plan — lab (end-to-end tests)

Status: approved (2026-10-11); L0 done. Decided: the lab runs by hand under WSL2 on the developer's Windows machine, as a smoke test before each release (not in CI). `lab/API.md` and its children are `planned` with `doc.go` only. Nothing below is started. The contract changes in §2 are approved and written into the `API.md` files. Each module's `API.md` stays the source of truth; this file orders the work and lists the open decisions. Delete it (or fold the leftovers into `DESIGN.md`) when L10 is done.

## 0. Goal

Prove, with the real binaries, that the app routes every domain the way its table says, and that the checker decides correctly when a (fake) TSPU blocks something. The real `splitter`, `sing-box` and `nfqws2` run in a client network namespace. Fake ISPs, a fake TSPU, fake sites, a fake DoH/ASN service and a fake VPS run in other namespaces. The tests assert on what each fake endpoint observed, so routes are proven by facts, not by packet heuristics:

| Route | Proof |
|---|---|
| direct | the fake site logs a connection from the **client** address; fakedpi saw a clean ClientHello |
| tunnel | the fake site logs a connection from the **VPS** address; fakedpi never saw that SNI |
| proxy | the fake site logs a connection from the **proxy** address |
| desync | fakedpi has a rule that would block the domain, the request still succeeds, the site logs the client address |
| block | the fake site logs nothing; fakedpi saw nothing |
| DNS | the fake DoH server logs the names it was asked; no plain DNS for a tunnel domain crosses the ISP namespace |

Out of scope: Windows (WinDivert, Job Object, Windows adapter detection stay in `internal/pc/SMOKE.md`, run on the same machine in the same release session), real TSPU behaviour, real sites, performance.

## 0a. Where and when it runs

- **WSL2 on the developer's Windows machine, before a release**, by the human with `sudo` (the agent never runs root commands). It is a release gate, not a CI job: a run may take tens of minutes.
- **It tests the release artifacts**: the `bin/` binaries from `make build` of the release commit (the version is in the report), and the pinned `third_party/bin/linux-amd64/` binaries from `make fetch`. Building from source is the fallback for development.
- **The host stays untouched.** Everything happens inside namespaces created inside the WSL2 VM; the WSL root namespace and Windows keep their routes, DNS and firewall. This matters more on a real machine than in CI: teardown also runs after a crash or Ctrl+C (prefix cleanup on the next start), and `lab-clean` removes leftovers by hand.
- **The result is a report** kept with the release: `dist/e2e/<version>-<date>/report.md` (pass/fail per scenario, versions of splitter, sing-box and nfqws2, WSL kernel version) plus the logs of every process. Logs come from fake traffic only and contain no real secrets; the folder is git-ignored.
- On non-Linux systems, without root, or with a missing kernel feature, the lab tests skip, so `go test -tags lab ./...` stays green on Windows and in the existing CI job.

## 1. Topology

```
 [client]                     [isp-a]                       [inet]
  splitter, sing-box TUN       router + fakedpi (NFQUEUE)     fake sites (faultserver, one IP each)
  nfqws2, blockanalyzer  ─eth0─ plain DNS resolver      ──────  fake DoH (zone + Cymru TXT + query log)
  curl                   ─eth1─ [isp-b] other rules     ──────  fakeasn echo
                                                              [vps] sing-box server: VLESS+Reality, SOCKS (proxy route)
```

Addressing rules (each one comes from a real constraint in the code):

- Client ↔ ISP links in `192.168.<n>.0/24`. This range is excluded from the TUN, like a real home LAN, so the gateway stays reachable.
- Sites, DoH, echo, VPS in `203.0.113.0/24` and `198.51.100.0/24` (TEST-NET). They must **not** be in a TUN-excluded range (10/8, 169.254/16, 192.168/16), or traffic skips sing-box. They must not overlap the FakeIP range (198.18.0.0/16) or the TUN address (198.19.0.1/30) either.
- Client interfaces are renamed to `eth0`/`eth1` inside the namespace. `netif` adds a name heuristic (`veth`, ...) on Linux, so a `veth*` uplink would count as virtual and the desync plan would be refused.
- Each ISP has its own source NAT address, so `fakeasn` can map client source → ASN (`isp-a` = AS64500, `isp-b` = AS64501).
- `ip netns exec` bind-mounts `/etc/netns/<ns>/resolv.conf`, so the client's system resolver is the ISP's plain DNS resolver. Teardown removes that folder.

Processes in a namespace: the test either runs a binary with `ip netns exec`, or creates a socket inside the namespace on a locked OS thread (`netns.Do`). A socket keeps its namespace after it is created, so listeners and the NFQUEUE socket can then be served from any goroutine.

## 2. Contract changes (approved 2026-10-11, written in L0)

| # | Module | Change |
|---|---|---|
| C1 | `lab` | `Up` also builds the `vps` namespace with a sing-box server (VLESS+Reality inbound for `tunnel`, SOCKS inbound for `proxy`). New methods: `Site(name) SiteInfo` (address, connection log), `DoH() *fakedns.Server` (query log), `DPI(isp) *fakedpi.Engine`, `RunSplitter(ctx, cfg) *Splitter` (writes `app.yaml`, `tunnel.json` and the seeded store into a temp root, runs `splitter` in the client namespace, exposes its log, status API, PID, exit code), `HTTPGet(ns, url)` and `Curl(args...)` in the client namespace. |
| C2 | new child `lab/fakedns` | A DoH server (HTTPS, test CA) with a static zone (A/AAAA for lab sites), TXT answers supplied by a hook (the parent wires `fakeasn` into it, siblings may not import each other), and a query log. Also a plain UDP/TCP 53 mode for the ISP resolver, with optional poisoning (answer a stub IP for listed names). |
| C3 | `lab/fakeasn` | Drop its own DNS listener (the app only asks Cymru TXT through DoH, `dnscheck/asn.go`). Keep the echo server; add `TXT(name) []string` to answer Cymru-style queries for `fakedns`. |
| C4 | `internal/lib/faultserver` | `Server.Conns() []Conn` with `Remote netip.AddrPort`, `SNI string`, `At time.Time`, `Bytes int64`: a log of every accepted connection. Test-only module, no app impact. |
| C5 | `lab/fakedpi` | Add `DNSPoison` handled by `fakedns` (no change here) or drop it; add `Seen() []Flow` (5-tuple, SNI if parsed, verdict taken) so tests can assert "the TSPU never saw this SNI". |
| C6 | `docs/architecture.md` | New row for `lab/fakedns`; `lab` Uses libs adds `internal/lib/checker`'s store format only through files (no import). |

## 3. Open decisions

| # | Question | Proposal |
|---|---|---|
| L-D1 | NFQUEUE access for `fakedpi` | **Decided:** `github.com/florianl/go-nfqueue` (approved; listed in root `AGENTS.md`). The kernel side is Linux's own NFQUEUE; the library is only the Go client for it. |
| L-D2 | Where lab tests run | **Decided:** WSL2 on the developer's machine, by hand before each release (§0a). No CI job. |
| L-D3 | Tools in the WSL distro (**decided:** nothing to prepare by hand) | `curl` and `iproute2` (`ip`) ship with the default Ubuntu WSL image; the `nft` command may not (`sudo apt install nftables`). The preflight checks all three and names what is missing. `curl` generates traffic through the TUN like a normal app would. |
| L-D4 | sing-box restart gap (30 s, `pc/app.go` `singBoxMinGap`) and reload debounce (3 s) are constants | Accept: a pre-release run may be slow (each reload scenario takes ≥ 30 s), so no `pc` API change is needed. |
| L-D5 | Known product gap: the analyzer probes direct only, so the checker cannot see that desync **failed** (fakedpi `reassemble`). A domain with `TLS_RST` is moved to `desync` and stays there. | The lab scenario documents the current behaviour (asserts it, with a comment pointing here) instead of failing. Fixing it (a desync dialer, or monitor failures on `desync` escalating to `tunnel`) is a separate product decision. |
| L-D6 | How the lab reads the decision store | Seed and read `decisions.json` by its documented format (`store/API.md`, schema 1) instead of importing `checker/store` (a child, not importable). |

## 4. Milestones

One milestone per task (root `AGENTS.md`). Each ends with `go vet ./...`, `go test ./...`, `go test -tags lab ./...` and `make cross` passing (root-only tests skip there), plus a human run of the new root tests under WSL2 (`sudo -E go test -tags lab ./lab/...`), and a status update in the module's `API.md` header and `docs/architecture.md`.

```
L0 contracts ── L1 netns ─────────────────────────────┐
             ├─ L2 faultserver log ───────────────────┤
             ├─ L3 fakeasn + fakedns ─────────────────┼─ L5 lab harness ─┬─ L6 blockanalyzer
             └─ L4a fakedpi engine ── L4b fakedpi I/O ┘                  ├─ L7 checker (M8)
                                                                         ├─ L8 splitter routing
                                                                         └─ L9 failures + lifecycle ── L10 runbook + report
```

L1–L4a can start in any order after L0. L2, L3 and L4a are testable **without root**.

### L0 — contracts (done 2026-10-11)
Update `lab/API.md` "Responsibility": it currently says the lab runs "in CI (`sudo`) or a disposable VM, never on a dev machine unattended"; replace with the WSL2 pre-release run of §0a (still attended, never unattended). Wrote C1–C6 into the `API.md` files and `docs/architecture.md`; created `lab/fakedns` (`API.md`, `doc.go`). Also in this step, needed by §1: `lab/netns` gained `Do`, `Start`, interface naming, `AddAddr`, `SetLink`, `DelRoute`, `Forwarding`, `SetResolver`. `fakeasn`, `fakedns` and the `fakedpi` engine have no build tag (pure Go), so their tests run on Windows without root; only the NFQUEUE part of `fakedpi` is Linux-only. No code beyond `doc.go`.

### L1 — `lab/netns` (done 2026-10-11)
Files: `netns.go` (Topology, Add, Link, Route, Exec, Teardown), `do_linux.go` (`(*NS).Do(fn)`: `runtime.LockOSThread`, `setns` by raw syscall, restore; if restoring fails the thread is never unlocked so Go discards it), `netns_test.go`.
- Every object carries the run prefix (`obl<random>`); `Teardown` is idempotent and also removes leftovers of crashed runs (namespaces, `/etc/netns/<prefix>*`).
- Link renaming inside a namespace (`eth0`), addresses, default routes, `sysctl` forwarding inside ISP namespaces only.
- Never touches the root namespace's routes or firewall (a test lists `ip route` and `nft list ruleset` of the root namespace before and after).
Tests (root): two namespaces ping each other; `Do` opens a listener inside a namespace; teardown leaves nothing; a non-root run skips.

### L2 — faultserver connection log (C4) (done 2026-10-11)
`Conns()` on every behavior, including connections reset by the behavior. Tests run without root on loopback.

### L3 — `lab/fakeasn` + `lab/fakedns` (C2, C3) (done 2026-10-11)
- `fakeasn`: echo server returns the caller's source address; `TXT(name)` answers `origin.asn.cymru.com` / `AS<n>.asn.cymru.com` in Cymru format from the mappings.
- `fakedns`: DoH (RFC 8484 GET and POST) with `github.com/miekg/dns` (approved), static zone, TXT hook, query log; plain DNS mode with poisoning list.
Tests (no root, loopback): `analyzer.LookupASN` with the analyzer's DoH resolver against `fakedns`+`fakeasn` returns the mapped ASN; zone answers; the poisoned resolver answers the stub; the query log records names and types.

### L4a — `lab/fakedpi` engine (pure) (done 2026-10-11)
Packet parsing (IPv4/IPv6, TCP, UDP), TLS ClientHello SNI extraction (single record, and across segments for `Reassemble`), per-flow state keyed by 5-tuple with a short TTL, and the rules: `NaiveSNI`, `FirstHello`, `Reassemble`, `Stall`, `IPBlock`, `QUICBlock`. Output per packet: accept, drop, or drop + inject RSTs (both directions, correct sequence numbers). `Seen()` log.
Tests (no root): crafted packets, including the split and fake-packet patterns nfqws2 produces, against each rule; a clean ClientHello is blocked by `NaiveSNI`, a split one is not, `Reassemble` blocks both.

### L4b — `lab/fakedpi` queue and injection (L-D1) (done 2026-10-11)
NFQUEUE client, raw IP socket for RST injection, `Start`/`SetRules`/`Close`, nft `forward` chain rule in the ISP namespace that queues client traffic. Both sockets are created with `netns.Do`.
Tests (root), in the `lab` root (`fakedpi` may not import its sibling `netns`): two namespaces through an ISP namespace; `NaiveSNI` resets a TLS connection to a faultserver, an unlisted SNI passes, `SetRules` takes effect without restarting.

### L5 — lab harness (`lab` root) (done 2026-10-11)
**Preflight first** (`lab.Preflight`): checks root, `/dev/net/tun`, `ip`, `nft`, `curl`, the kernel features (netns, veth, nf_tables, `nft_queue`/`nfnetlink_queue`, NAT, IPv6) by trying them in a throwaway namespace, and the binaries. A missing feature skips the whole lab with one message naming it and the command to fix it (for the human; never run by the lab). Verify on the actual WSL2 kernel in this milestone: if NFQUEUE is missing there, see §5.
`Up` builds the topology in §1 from profiles (`lab/profiles/*.yaml`: ISP name, ASN, fakedpi rules, poisoned names): one test CA (`testca`, passed to processes with `SSL_CERT_FILE` and `curl --cacert`, never installed), sites (one faultserver per name, `OK` behaviour unless a scenario says otherwise), fakedns, fakeasn, ISP resolvers, VPS sing-box server (keys from `sing-box generate reality-keypair`, Reality handshake target = a lab site). `Binary(name)` uses `bin/<name>` when `LAB_BIN_DIR` is set (the release artifacts), else builds `./cmd/<name>` into a temp dir; third-party binaries come from `third_party/bin/linux-<arch>/` (skip when missing, `make fetch` installs them). `UseISP` switches the client's default route and link state. Every helper polls with a deadline; no fixed sleeps.
Tests (root): `curl` from the client reaches a site directly; with `NaiveSNI` on `isp-a` it gets a reset; after `UseISP("isp-b")` (no rule) it works; the site's log shows the client's NAT address of the ISP in use.

### L6 — blockanalyzer scenarios (done 2026-10-14; 10 of 10 pass)
**Finding (fixed by task card 01):** with every client link down, `blockanalyzer` exited 1 instead of 2 with a calibration-failed report. Fixed in `internal/blockanalyzer`; the case `calibration fails: client link down` passes.

Run the `blockanalyzer` binary in the client namespace with a config pointing at lab DoH, control/canary domains and neutral SNI. Table test: each fakedpi rule and each faultserver behaviour gives the expected verdict (`TLS_RST`, `TCP_TIMEOUT`, `STALL_16_20`, `DNS_POISONED` via the poisoning resolver, `TLS_MITM` via a wrong-cert site, ...) and the expected suggestion; SNI swap confirms an SNI-only block; calibration fails with the ISP link down.

### L7 — checker end to end (checker M8) (done 2026-10-14; 10 of 10 cases pass)
**Findings (fixed by task cards 02 and 03):** (1) the app never gave the checker a DoH resolver, so networks were never identified by ASN (card 03: `analyzer.NewDoHResolver`, wired in `internal/pc/deps.go`, status label `AS<n>` fallback); (2) after a network switch the checker's probes stayed bound to the old interface: two pinned dialers, `internal/pc/direct.go` (card 02, part 1) and the analyzer's DoH dialer `dialer.NewFollowing` (part 2, the real cause).

Run `splitter run --ui` (disconnected: checker and status page only) with short schedule settings. Read state through the status API (`HTTPGet` in the client namespace) and `decisions.json`.
- Unknown network → all candidates `tunnel` (default) until fakeasn identifies AS64500.
- `NaiveSNI` on a domain → `desync` after `Confirmations` (2) runs, not after one.
- `IPBlock` → `tunnel`.
- `SetRules` removes the block → back to `direct` after 2 runs.
- `UseISP("isp-b")` → network change, the table switches to isp-b's decisions, then back to isp-a's stored ones on return.
- `Reassemble` → documents L-D5 (stays `desync`).

### L8 — splitter routing (connected) (done 2026-10-14; all cases pass)
**Findings, all fixed by task cards and verified as root:** F3 startup race between `Connect` and the reload loop's subscription (card 04); F4 nfqws2 could not read its hostlist after dropping root, which needs search permission on every ancestor directory (card 05, final design: nfqws2 keeps the app identity, `--uid=0:0`); F5 sing-box's `auto_route` rule `9003: not from all iif lo lookup 2022` sent nfqws2's raw packets (fwmark 0x40000000) into the TUN (card 06: the app prints and checks `ip rule add fwmark 0x40000000/0x40000000 lookup main priority 8999`). Still not exercised: IPv6 (the lab is IPv4-only), `--fwmark` with a non-default mark, and hostlist re-read G13 (nfqws2 does re-check the hostlist file at runtime; a table change while connected is an L9 scenario).
Seed `decisions.json` for `asn:64500` with one domain per route plus nested overlaps (`tunnel example.test` + `direct cdn.example.test`, `desync` parent + `direct` child), with fakedpi rules that agree with the seed so probes do not change it. Static `proxy` and `block` lists.
- First run without the nft table: exit code 2 and the printed `Setup` commands; the test applies them **inside the client namespace** and runs again; the app reaches `connected`.
- `curl` each domain; assert the §0 proof table (site connection logs, fakedpi `Seen()`, DoH query log).
- Clash API `/connections` agrees (route tag per connection).
- QUIC (UDP 443) to a site never crosses the ISP namespace when `block_quic` is on.
- Findings from `docs/REVIEW-NOTES.md` turned into tests: tunnel-domain TXT/MX queries reaching the DoH server (D1), the LAN resolver bypassing the TUN (client `resolv.conf` points at `192.168.x`, an excluded range), an IPv6 site bypassing the TUN. These assert the **current** behaviour with a link to the finding until it is decided.
- Verify the unverified flags here: nfqws2 `--fwmark`, and whether nfqws2 re-reads its hostlist (G13).

### L9 — live failures and lifecycle (done 2026-10-14; all scenarios pass as root)
Results recorded from the real-kernel run: a stall rule added while connected moves a direct domain to tunnel through the monitor's failures and a recheck (stalled download 15069 B, inside the 12–36 KiB window); a domain moved to desync restarts nfqws2 (new pid, new hostlist) before sing-box (G13: the app restarts the engine on any hostlist change; nfqws2 re-reading the file on its own is not relied on); after an uplink change the nft rules mismatch, the app logs the commands (now only what is missing) and keeps the engine until they are applied; a second `splitter` on the same root exits 3 and the first keeps running; after `kill -9` of the app sing-box and nfqws2 are gone at once and the TUN disappears, while the nft table, the `ip rule` entries (8999 and sing-box's 9000-9003) and the files in `run/` are left behind (recorded in `kill9-leftovers.txt` of the run, for `docs/RECOVERY.md`). The Linux-only unit tests were run as the unprivileged user `nobody` (compiled as root, run with `setpriv`): all pass, including `procman` (`TestStopTimeoutKills` fixed in the test, card 08), `netif`, the instance lock.
- A `Stall` rule added while connected on a `direct` domain: the monitor flags it, a recheck runs, the route changes (with `curl` traffic generating the stalled flows).
- `kill -9` of `splitter`: `sing-box` and `nfqws2` exit (Pdeathsig), the TUN disappears, the client reaches a site directly again.
- A table change that moves a domain to `desync`: the engine restarts first, then sing-box, after the debounce and the 30 s gap (L-D4).
- `UseISP` while connected: the uplink changes, the nft rules no longer match, the app logs the commands and keeps the engine stopped; after the test applies them the engine restarts.
- A second `splitter` exits with code 3 (instance lock).
- Run the Linux-only unit tests that were only compile-checked (`procman` SIGTERM/Pdeathsig, `netif` sysfs, the `flock` lock) in the same job.

### L10 — runbook and report
- `make e2e` (in WSL2): checks it runs on Linux, then `sudo -E env "PATH=$PATH" LAB_BIN_DIR=$PWD/bin go test -tags lab -v -timeout 60m ./lab/...`. `make lab-clean` removes leftover namespaces and `/etc/netns/obl*` of a crashed run.
- The lab writes `dist/e2e/<version>-<date>/report.md` and keeps every process log there (also on success, so a release has evidence). `dist/` is git-ignored already or is added to `.gitignore`.
- `docs/RELEASE.md` (new): the pre-release checklist: one-time WSL2 setup (distro packages, Go, kernel check through `make e2e`'s preflight), then `make build` → `make fetch` → `make e2e` in WSL2 → `internal/pc/SMOKE.md` "Windows" on the host → attach the report. It links `docs/RECOVERY.md`.
- `internal/pc/SMOKE.md` "Linux": the items the lab now covers (nft table check, kill -9, teardown) point at the lab scenarios instead of being run by hand.

## 5. Risks and things to verify early

- **sing-box trusting the test CA** for its DoH server: `SSL_CERT_FILE` should work if sing-box uses the system pool; verify in L5. If not, the lab DoH needs another way in (would be a `singbox` config change: ask).
- **`setns` syscall numbers** differ per architecture and the package must still compile for linux/mipsle (`make cross`): keep the raw call in per-arch files or use a constant from `syscall` where it exists.
- **WSL2 kernel features**: the stock WSL2 kernel must provide `nf_tables`, `nft_queue`/`nfnetlink_queue`, NAT, veth and TUN. Check with the preflight in L5 before writing L4b. If NFQUEUE is missing, the fallback is a custom WSL2 kernel (`.wslconfig` `kernel=`, a host change the human makes) or a Linux VM; decide then.
- **WSL2 networking mode** (NAT or mirrored) must not matter, because the lab never uses the WSL root namespace's uplink; verify both if the developer switches modes.
- **Repo on `/mnt/c`**: builds and tests work but are slow; cloning into the WSL filesystem is faster. The runbook mentions both.
- **netif in a namespace**: virtual detection and gateway MAC through `/sys` and the ARP table of the client namespace; verify in L5 that `netif.Uplink` picks `eth0`.
- **Flaky timing**: every wait is a poll with a deadline; schedule intervals are shortened in the test config, never the code.
- **Hard rules**: the lab never touches the root namespace's routes, DNS or firewall; the test CA lives only in temp files; everything is torn down, also after a crash (prefix cleanup).
