# DESIGN.md — analyzer

Written for the direct-only scope. `AGENTS.md` overrides this file where they differ. Section numbers are referenced from `API.md`, `AGENTS.md` and the children's `API.md`.

## 1. Goal

For a target domain and a dialer, produce a typed verdict plus evidence: is it blocked, and how. Facts only; routes are suggested, never decided.

## 4. Staged probe

Each attempt opens a fresh TCP connection and walks the stages in order, stopping at the first failure:

1. **resolve** — A lookup through DoH (never the system resolver). `Target.IP` skips this stage. The system resolver is queried only for the poisoning comparison (dnscheck).
2. **tcp** — `Dialer.DialContext` to `ip:port`.
3. **tls** — uTLS handshake with the configured browser fingerprint and SNI = domain. Certificate verification is *deferred*: the handshake runs with `InsecureSkipVerify` so a spoofed certificate is observed rather than failing the handshake.
4. **verify** — the peer chain is verified against the system roots (or the roots given in the prober's test hook) afterwards (`Evidence.CertError`). A verification failure is `TLS_MITM`.
5. **http** — raw HTTP/1.1 `HEAD` over the TLS connection. A 3xx whose `Location` host is a known stub host → `HTTP_STUB`; other redirects → `OK_REDIRECT`.
6. **bulk** — keep-alive test that pushes or pulls 16–20 KB and beyond (see §4.1). Only when `StageSet` has `Bulk`.

Plain HTTP on port 80 is used only to learn ISP stubs (dnscheck).

`Repeats` attempts run per probe (default 2); the verdict is `classify.Majority` of them. `Evidence` comes from the first attempt that produced the winning verdict.

### 4.1 Bulk test

TSPU-style blocks freeze a flow after ~16–20 KB. Two modes:

- **download**: `GET` of `BulkPath` (or `Path` when unset) on the same keep-alive connection; read up to `Bulk.Size` bytes (default 64 KiB). A stall is no byte progress until the RTT-adaptive timeout `max(Timeouts.Bulk/4, 6×RTT)`, capped by `Timeouts.Bulk`.
- **upload**: `POST` of `Bulk.Size` bytes of body; wait for the status line.

`classify` maps a stall whose cumulative byte count (RX+TX) lies inside `[StallWin[0], StallWin[1]]` (default 12–36 KiB) to `STALL_16_20`; outside it → `READ_TIMEOUT`.

## 5. Verdict taxonomy (frozen)

| Verdict | Stage | Meaning | Default blocking |
|---|---|---|---|
| `OK` | any | everything worked | none |
| `OK_REDIRECT` | http | normal redirect | none |
| `DNS_POISONED` | resolve | system answer is a stub/bogon/NXDOMAIN while DoH resolves | likely |
| `DNS_FAIL` | resolve | DoH itself failed | unknown |
| `TCP_TIMEOUT` | tcp | SYN unanswered | likely |
| `TCP_REFUSED` | tcp | RST to SYN | likely |
| `TLS_RST` | tls | RST/abort after ClientHello (even when reported as EOF) | likely |
| `TLS_TIMEOUT` | tls | ClientHello unanswered | likely |
| `TLS_ALERT` | tls | fatal alert instead of ServerHello | likely |
| `TLS_SPOOF` | tls | non-TLS bytes / wrong version / record overflow / decode error | likely |
| `TLS_MITM` | verify | certificate does not verify | confirmed |
| `TLS_EOF` | tls | clean close during handshake with nothing sent | likely |
| `HTTP_STUB` | http | redirect or page of a known ISP stub | confirmed |
| `HTTP_RST` | http | RST/abort after the request | likely |
| `STALL_16_20` | bulk | flow freezes inside the stall window | confirmed |
| `READ_TIMEOUT` | http/bulk | stall outside the window / slow server | unknown |
| `CONN_LIMITED` | tls | handshakes rejected only under concurrency | unknown |
| `LOCAL_ERROR` | any | our own failure (`*verdict.LocalErr`) | local_error |
| `UNKNOWN` | any | not classifiable | unknown |

`Confirm` upgrades `Blocking` to `confirmed` when the verdict is a TLS-level block (`TLS_RST`, `TLS_TIMEOUT`, `TLS_EOF`, `TLS_ALERT`) and the SNI-swap attempt to the same IP succeeded (`OK`/`OK_REDIRECT`): the IP is reachable, only the SNI is blocked.

Classification order (first match wins): `*LocalErr` in the chain → `LOCAL_ERROR`; then by stage:

- resolve: any error → `DNS_FAIL` (poisoning is decided by dnscheck.Compare, not by errors).
- tcp: refused or reset → `TCP_REFUSED`; timeout/deadline → `TCP_TIMEOUT`; else `UNKNOWN`.
- tls: reset (`ECONNRESET`, `WSAECONNRESET`, "connection reset", "forcibly closed", "connection aborted") → `TLS_RST`; EOF/unexpected EOF → `TLS_RST` when the ClientHello was sent (`TxBytes > 0`; many stacks report an RST as EOF), else `TLS_EOF`; timeout → `TLS_TIMEOUT`; remote alert → `TLS_ALERT`; "wrong version number", "record overflow", "decode error", "first record does not look like a TLS handshake" → `TLS_SPOOF`.
- verify: `CertErr != nil` → `TLS_MITM`.
- http: reset/EOF → `HTTP_RST`; timeout → `READ_TIMEOUT`; stub redirect → `HTTP_STUB`; 3xx → `OK_REDIRECT`; otherwise `OK` (any status: the site answered).
- bulk: timeout with RX+TX inside the window → `STALL_16_20`; outside → `READ_TIMEOUT`; reset → `HTTP_RST`.

## 6. SNI swap, calibration, majority

- **SNI swap** (`Options.SNISwap`): when the TLS stage fails with a TLS-level verdict, repeat the handshake to the same IP with a neutral SNI from `Config.NeutralSNI`. Result stored in `Evidence.SNISwap`.
- **Calibration** (Runner only): probe the `ControlDomains` (never blocked) — all fail → `Internet=failed`. Probe `CanaryDomains` (known blocked) — any blocking verdict → `DPIPresent=yes`, all `OK` → `no`, no canaries → `unknown`. A foreign bypass tool found by process scan adds a warning (M8). If calibration failed, route suggestions are none.
- **Majority**: most frequent non-LOCAL verdict wins; ties prefer the more blocked one (ordered by `Blocking`: confirmed > likely > unknown > none); all LOCAL → `LOCAL_ERROR`.

## 7. Suggested route

See AGENTS.md "Suggested starting route". `SuggestRoute` returns `(route, true)` or `("", false)`; the report carries an optional `suggestion_note` (e.g. "needs DoH").

## 8. JSON report (schema 1, frozen)

```json
{
  "schema": 1,
  "version": "dev",
  "started_at": "2026-01-01T00:00:00Z",
  "network": {"public_ip": "203.0.113.5", "asn": {"number": 64500, "name": "...", "country": "RU", "prefix": "203.0.113.0/24"}},
  "calibration": {"internet": "ok", "dpi_present": "yes", "warnings": []},
  "results": [{
    "target": {"domain": "example.com", "port": 443, "path": "/"},
    "route": "direct", "ip": "93.184.216.34",
    "verdict": "TLS_RST", "blocking": "confirmed", "stage": "tls",
    "evidence": {"err": "...", "sni_swap": {"sni": "www.example.org", "verdict": "OK"}},
    "suggestion": "desync", "suggestion_note": "",
    "started_at": "...", "duration": 123456789
  }],
  "warnings": []
}
```

Rules: durations are nanosecond integers; empty optional fields are omitted; `results` keep the order of `Batch.Targets`; `EncodeReport` writes indented JSON with a trailing newline; `DecodeReport` rejects `schema != 1` and unknown verdict/blocking values. `CheckState` values: `ok`, `failed`, `yes`, `no`, `unknown`.

## 9. Concurrency (Runner)

A worker pool of `Concurrency` goroutines; a per-IP semaphore of `PerIPConcurrency`; before each probe a jitter sleep drawn uniformly from `Jitter` via `clock.Clock`. Calibration runs before the batch.
