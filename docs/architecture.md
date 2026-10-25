# Architecture

## 1. Overview

Generated from the API.md headers. docs/project-notes.md was not present when this file was generated.

## 2. Rules

The architecture rules (module shape, placement, import rules a-e, API.md header) are in [BOOTSTRAP.md](../BOOTSTRAP.md) section 1 and condensed in [AGENTS.md](../AGENTS.md).

## 3. Module tree

  - `internal/blockanalyzer`: runs the analyzer from the command line and prints what is blocked and how
    - `internal/lib/analyzer`: probes domains from the local network and classifies whether and how they are blocked
      - `internal/lib/analyzer/classify`: maps a failed probe stage and error to a verdict (pure functions)
      - `internal/lib/analyzer/dialer`: opens probe connections through a given route without being captured by TUN or desync
      - `internal/lib/analyzer/dnscheck`: resolves via DoH, detects DNS poisoning, FakeIP and ISP stubs, and looks up ASNs
        - `internal/lib/analyzer/lib/verdict`: defines the verdict, stage, blocking and evidence types shared inside the analyzer
    - `internal/lib/checker`: runs the network-aware probe → decide → store cycle and publishes the current RouteTable
      - `internal/lib/checker/decide`: turns probe results and the current decision into a new decision, with hysteresis (pure)
        - `internal/lib/checker/lib/model`: defines the decision and network identity types shared inside the checker
      - `internal/lib/checker/lists`: loads and merges domain lists: candidates to check and static proxy/block entries
      - `internal/lib/checker/netwatch`: identifies the current network (networkId) and emits change events
      - `internal/lib/checker/schedule`: decides when to run checks: timer with jitter, network change, expired TTL, failure threshold
      - `internal/lib/checker/store`: persists decisions per network and domain with atomic writes
    - `internal/lib/clock`: provides an injectable clock with a real and a fake implementation
    - `internal/lib/faultserver`: runs localhost servers that misbehave in specific, DPI-like ways
    - `internal/lib/netif`: finds the physical uplink interface and binds sockets to it, bypassing TUN adapters
    - `internal/lib/paths`: resolves per-OS runtime locations for config, data, logs and secrets
    - `internal/lib/proc`: lists and finds running processes per OS
    - `internal/lib/route`: defines the route kinds, domain normalization and the RouteTable shared by all modules
    - `internal/lib/testca`: creates an in-memory test certificate authority for lab and fault-server tests
    - `internal/lib/version`: holds build version information injected at link time
  - `internal/pc`: runs the desktop app: checker, sing-box, desync engine, monitor and status page
    - `internal/pc/desync`: builds the local desync engine plan (zapret2: winws2 or nfqws2) for the desync domains
      - `internal/pc/desync/nfqws`: renders nfqws2 (zapret2) arguments, hostlist and nftables rules
      - `internal/pc/desync/winws`: renders winws2 (zapret2, WinDivert) arguments and hostlist
      - `internal/pc/lib/clashapi`: a minimal client for sing-box's Clash-compatible API
    - `internal/pc/monitor`: watches live connections via the Clash API, detects failures and counts traffic per route
    - `internal/pc/procman`: starts, stops, restarts and supervises child processes that die with the parent
    - `internal/pc/singbox`: generates and validates the sing-box config from a RouteTable
    - `internal/pc/status`: serves a local read-only status page with routes, network and VPS savings
  - `internal/router`: runs the checker on OpenWrt and keeps podkop's domain lists up to date
    - `internal/router/podkop`: renders podkop-format domain lists from a RouteTable and triggers a podkop reload
- `lab`: runs end-to-end scenarios in Linux network namespaces with a fake TSPU
  - `lab/fakeasn`: serves fake public-IP echo and Cymru-style ASN answers for the lab
  - `lab/fakedns`: serves the lab's DoH and plain DNS with a static zone, poisoning and a query log
  - `lab/fakedpi`: emulates TSPU-like blocking on packets via NFQUEUE inside a lab namespace
  - `lab/netns`: creates and tears down network namespaces, veth links and routes for the lab

## 4. Placement

| Path | Kind | Consumers | Children | Status |
|---|---|---|---|---|
| `internal/blockanalyzer` | app | cmd/blockanalyzer | – | design ready (see internal/lib/analyzer/AGENTS.md) |
| `internal/lib/analyzer` | lib | internal/blockanalyzer, internal/lib/checker, internal/lib/checker/decide, internal/lib/checker/lib/model, internal/lib/checker/netwatch, internal/pc, internal/pc/monitor, internal/router, lab, lab/fakeasn | internal/lib/analyzer/classify, internal/lib/analyzer/dnscheck, internal/lib/analyzer/dialer, internal/lib/analyzer/lib/verdict | design ready (see DESIGN.md, AGENTS.md) |
| `internal/lib/analyzer/classify` | child | internal/lib/analyzer | – | design ready |
| `internal/lib/analyzer/dialer` | child | internal/lib/analyzer | – | design ready (direct only) |
| `internal/lib/analyzer/dnscheck` | child | internal/lib/analyzer | – | design ready |
| `internal/lib/analyzer/lib/verdict` | lib | internal/lib/analyzer, internal/lib/analyzer/classify, internal/lib/analyzer/dialer, internal/lib/analyzer/dnscheck | – | design ready |
| `internal/lib/checker` | lib | internal/pc, internal/router | internal/lib/checker/decide, internal/lib/checker/store, internal/lib/checker/netwatch, internal/lib/checker/lists, internal/lib/checker/schedule, internal/lib/checker/lib/model | implemented |
| `internal/lib/checker/decide` | child | internal/lib/checker | – | implemented |
| `internal/lib/checker/lib/model` | lib | internal/lib/checker, internal/lib/checker/decide, internal/lib/checker/netwatch, internal/lib/checker/schedule, internal/lib/checker/store | – | implemented |
| `internal/lib/checker/lists` | child | internal/lib/checker | – | implemented |
| `internal/lib/checker/netwatch` | child | internal/lib/checker | – | implemented |
| `internal/lib/checker/schedule` | child | internal/lib/checker | – | implemented |
| `internal/lib/checker/store` | child | internal/lib/checker | – | implemented |
| `internal/lib/clock` | lib | internal/lib/analyzer, internal/lib/checker, internal/lib/checker/netwatch, internal/lib/checker/schedule, internal/lib/checker/store, internal/pc, internal/pc/monitor, internal/pc/procman, internal/router | – | planned |
| `internal/lib/faultserver` | test-only | internal/lib/analyzer, lab | – | implemented (M3, lab L2) |
| `internal/lib/netif` | lib | internal/lib/analyzer/dialer, internal/lib/checker/netwatch, internal/pc, internal/pc/desync | – | planned |
| `internal/lib/paths` | lib | internal/blockanalyzer, internal/pc, internal/router | – | implemented |
| `internal/lib/proc` | lib | internal/lib/analyzer, internal/pc, internal/pc/procman | – | planned |
| `internal/lib/route` | lib | internal/blockanalyzer, internal/lib/analyzer, internal/lib/analyzer/dialer, internal/lib/checker, internal/lib/checker/decide, internal/lib/checker/lib/model, internal/lib/checker/lists, internal/lib/checker/schedule, internal/pc, internal/pc/desync, internal/pc/monitor, internal/pc/singbox, internal/pc/status, internal/router, internal/router/podkop, lab | – | planned |
| `internal/lib/testca` | test-only | internal/lib/analyzer, internal/lib/faultserver, lab | – | planned |
| `internal/lib/version` | lib | internal/blockanalyzer, internal/pc, internal/router | – | implemented |
| `internal/pc` | app | cmd/splitter | internal/pc/singbox, internal/pc/desync, internal/pc/procman, internal/pc/monitor, internal/pc/status, internal/pc/lib/clashapi | implemented |
| `internal/pc/desync` | child | internal/pc | internal/pc/desync/winws, internal/pc/desync/nfqws | implemented |
| `internal/pc/desync/nfqws` | child | internal/pc/desync | – | implemented |
| `internal/pc/desync/winws` | child | internal/pc/desync | – | implemented |
| `internal/pc/lib/clashapi` | lib | internal/pc, internal/pc/monitor, internal/pc/status | – | implemented |
| `internal/pc/monitor` | child | internal/pc | – | implemented |
| `internal/pc/procman` | child | internal/pc | – | implemented |
| `internal/pc/singbox` | child | internal/pc | – | implemented |
| `internal/pc/status` | child | internal/pc | – | implemented |
| `internal/router` | app | cmd/splitter-router | internal/router/podkop | planned |
| `internal/router/podkop` | child | internal/router | – | planned |
| `lab` | lab | – (go test only) | lab/netns, lab/fakedpi, lab/fakeasn, lab/fakedns | implemented (lab/PLAN.md) |
| `lab/fakeasn` | lab | lab | – | implemented (lab L3) |
| `lab/fakedns` | lab | lab | – | implemented (lab L3) |
| `lab/fakedpi` | lab | lab | – | implemented (lab L4a, L4b) |
| `lab/netns` | lab | lab | – | implemented (lab L1) |

## 5. Binaries

| Binary | App module |
|---|---|
| `blockanalyzer` | `internal/blockanalyzer` |
| `splitter` | `internal/pc` |
| `splitter-router` | `internal/router` |
