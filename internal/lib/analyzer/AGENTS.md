# AGENTS.md — analyzer (internal/lib/analyzer)

Instructions for AI coding agents working on this module. Read the root `AGENTS.md` first (global and architecture rules, hard rules, commands), then this file, `API.md` and `DESIGN.md` in this folder.

## What this module is

The analyzer checks the current network from the user's own machine and reports, per domain, **whether it is blocked and how** (DNS poisoning, IP block, SNI reset, TLS spoofing, MITM, 16–20 KB stall, ISP stub page…).

It is module #1 of a larger app (Traffic Splitter) that routes traffic into `direct / desync / tunnel / proxy / block`. This module only produces **facts**. It never decides routes, stores decisions, or starts other programs.

## Source of truth

- Contract: `API.md` in this folder (public signatures, header, consumers). Children have their own `API.md`.
- Design: `DESIGN.md` in this folder.
- **This file overrides DESIGN.md where they differ.** In particular:
  - **Scope is direct-only.** Implement only the direct path. Do NOT implement desync/tunnel/proxy dialers, SOCKS clients, port-range allocators, or route calibration (DESIGN §3.4, route parts of §6). These are deferred.
  - Keep the dialer as a parameter anyway: `Probe(ctx, target, dialer, opts)` with one implementation (`dialer.NewDirect`) plus `dialer.NewFake` for tests.
  - Names: the module is `analyzer`, its CLI app is `internal/blockanalyzer` with binary `blockanalyzer` (DESIGN says `dpicheck` / `dpiprobe`; ignore that).
- If DESIGN.md, API.md and this file disagree in a way not covered here, **stop and ask**.

## How to work

1. Implement **only the milestone named in the prompt**. Do not start the next one, even if it looks easy.
2. Before coding, state a short plan: files you'll create/change, and how you'll test.
3. Keep changes small and focused. No drive-by refactors outside the milestone.
4. Finish with: `go build ./...`, `go vet ./...`, `go test ./...` all passing, plus the cross-builds (below).
5. Report back: what was done, what was tested, what is left or uncertain, any design questions.

Never mark a milestone done with failing or skipped tests.

## Repo layout

Folders analyzer milestones may touch:

```
internal/lib/analyzer/            root: Prober, Runner, Report, re-exported types
  classify/                       error + stage → Verdict (pure)
  dnscheck/                       DoH resolver, poisoning / FakeIP / stubs, ASN
  dialer/                         Dialer interface, direct + fake
  lib/verdict/                    Verdict, Stage, Blocking, Evidence, LocalErr
internal/lib/faultserver/         test-only fault servers (//go:build lab)
internal/lib/testca/              test-only CA (//go:build lab)
internal/blockanalyzer/           CLI: flags, table + JSON output
cmd/blockanalyzer/                one line
```

Read-only use (don't change their APIs without asking): `internal/lib/route`, `internal/lib/netif`, `internal/lib/proc`, `internal/lib/clock`, `internal/lib/paths`, `internal/lib/version`. If a milestone needs something new from them, propose the API change first.

Remember the fractal rules: children can't import the analyzer root or each other, so shared types go in `lib/verdict` and the root re-exports them.

## Tech stack (pinned)

- Go: the version in `go.mod`. Do not change it.
- Allowed dependencies:
  - `github.com/refraction-networking/utls` (browser TLS fingerprints)
  - `github.com/miekg/dns` (DNS wire format)
  - `gopkg.in/yaml.v3` (config)
  - the Go standard library
- **Any other dependency: ask first.** No CLI frameworks, no logging frameworks, no HTTP client libraries.
- Do not upgrade pinned versions unless asked; uTLS upgrades change fingerprints.

## Commands
See root `AGENTS.md`. Fault-server and TLS tests need `go test -tags lab ./...`.

## Code rules

- No global mutable state. Config is passed in; clock, dialer and resolver are behind interfaces.
- Everything network-facing takes a `context.Context` and respects cancellation and timeouts.
- **Verdicts are a typed enum, never strings with formatting/colors.** Presentation lives only in `internal/blockanalyzer`.
- Every probe records its `Stage` (resolve / tcp / tls / verify / http / bulk). Classification uses the stage.
- Keep the raw error chain in `Evidence`. Never discard error information.
- Errors from our own side (dialer misconfig, no interface, local bug) → `LOCAL_ERROR`, never a block verdict.
- Real runs use uTLS with a browser fingerprint (default `chrome`). Go's `crypto/tls` fingerprint only in tests.
- Fresh TCP connection per probe, except inside the bulk (16–20 KB) test, which needs keep-alive.
- Always resolve through DoH in probes; the system resolver is only used for the poisoning comparison (it may return FakeIP 198.18.0.0/15 when a TUN client runs).
- Exported identifiers get doc comments. Comments and identifiers in English.

## Frozen contracts (do not change without asking)

- The `Verdict` enum and `Blocking` levels (DESIGN §5).
- The JSON report schema (DESIGN §8), including `"schema": 1`. Any change means a schema bump and updated golden files.

## Testing rules

- **Every verdict must be reproduced by a localhost fault server** in `internal/lib/faultserver` and covered by a table test. A verdict without a fault-server test is not done.
- Unit tests must not touch the real internet. Use fake dialers, fake resolvers, a fake clock.
- Tests needing real network access go behind `//go:build integration` and are never required for "done".
- Fault-server tests are `//go:build lab` files. Fault servers bind to `127.0.0.1` on random free ports and shut down in `t.Cleanup`.
- CLI and report output: golden files in `testdata/`, updated only with an explicit `-update` flag.
- Tests must be deterministic: no sleeps for synchronization, no dependence on timing beyond generous timeouts.

## Hard rules (never break these)
See root `AGENTS.md`. They all apply here.

## Milestones (direct-only scope)

| # | Milestone | Done when |
|---|---|---|
| M1 | Skeleton: types (`Stage`, `Verdict`, `Blocking`, `Target`, `ProbeResult`, `Evidence`), config loading, CLI stub with `--version` | builds + cross-builds pass; config round-trip test |
| M2 | `DirectDialer` + staged probe: TCP → uTLS handshake → cert verify (recorded) → raw HTTP/1.1 `HEAD`; error classifier | classifier table tests on error fixtures |
| M3 | Fault server suite: RST after ClientHello, drop ClientHello, TLS alert, garbage/wrong version, wrong cert, HTTP stub redirect, RST after request | each server → expected verdict, table test green |
| M4 | Bulk 16–20 KB test (upload + download modes, RTT-adaptive timeout, 12–36 KB window) | stall at 16 KB → `STALL_16_20`; stall at 50 KB → `READ_TIMEOUT` |
| M5 | DNS: DoH resolver, system-vs-DoH comparison, FakeIP detection, ISP stub IP learning (plain HTTP), ASN lookup | fake resolver tests for each DNS verdict |
| M6 | SNI-swap differential, calibration (control + canary domains), repeats with majority vote | fake "SNI block" server → `TLS_RST` + `confirmed` |
| M7 | Batch runner (concurrency, per-IP limit, jitter), JSON report, CLI table, **suggested starting route** per domain | golden JSON + golden table tests |
| M8 | Foreign bypass-tool warning (process scan, list from config) | test with fake process list |

**Deferred (do not implement):** desync/tunnel/proxy dialers, source-port ranges, route calibration, burst ("siberian") probe, QUIC checks, IPv6 probing (keep the types IPv6-ready).

### Suggested starting route (M7)

A hint only, clearly labeled as a suggestion in the report; the real decision logic lives in another module.

| Direct result | Suggested route |
|---|---|
| `OK`, `OK_REDIRECT` | `direct` |
| `TLS_RST` / `TLS_TIMEOUT` / `TLS_SPOOF` / `TLS_ALERT` with SNI-swap success | `desync` |
| `TCP_TIMEOUT`, `TCP_REFUSED` (with calibration OK) | `tunnel` |
| `STALL_16_20` | `tunnel` |
| `DNS_POISONED` | `direct` + note "needs DoH" |
| `TLS_MITM`, `HTTP_STUB` | `tunnel` |
| `LOCAL_ERROR`, `UNKNOWN`, `DNS_FAIL`, calibration failed | none (`unknown`) |

## When to stop and ask

- The design doc and this file conflict in a way not covered above.
- A new dependency seems necessary.
- A frozen contract (verdict enum, JSON schema) would need to change.
- Something requires admin rights or system changes.
- A test can only pass with real network access.
- You're unsure whether a behavior is in the current milestone.
