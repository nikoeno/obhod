# Decisions: internal/pc

The agent prepares the facts (options, code locations, consequences); the human decides. Write the decision under each item, then the agent implements it in a separate task.

Related: `REVIEW-AI.md` (agent only), `REVIEW-MANUAL.md` (human only).

---

## D1: DNS leak of tunnel domains (from session 3)
Direct/desync domains go to `dns-direct`, A/AAAA go to FakeIP, HTTPS/SVCB are rejected. Other query types (TXT, MX, ...) of a tunnel domain can still reach the direct DoH server.
- Agent prepares: the exact query types that leak, where the rule lives in `singbox.go`, the cost of rejecting or tunnelling them.
- [x] Decision (2026-10-15): reject other types by default; configurable with `dns.allow_other_query_types` (true = old behaviour, leaks the name). Done in `singbox.go`.
- [x] Decision (2026-10-15): the notification and logging of rejected queries is planned for later, not now. Recorded as G19 in `internal/pc/PLAN.md`. Until then a reject is visible only by setting `singbox.log_level: debug` and reading the sing-box output.

## D2: monitor heuristics (from session 4)
`monitor/detect.go` flags `TLS_RST`, `TCP_TIMEOUT`, `STALL_16_20`. Known false positive: idle keep-alive stopping at 12-36 KB.
- Agent prepares: list of normal patterns that would trigger each heuristic, and what a flag causes downstream.
- [ ] Decision: keep as is / tighten (how) / drop one.

## D3: `final: tunnel` hard-coded (from session 5, open item G6 in `PLAN.md`)
- Agent prepares: where it is set, what a configurable `final` would change in config and `API.md`.
- [ ] Decision: keep hard-coded / make configurable.

## D4: checker config keys (from session 9)
Checker keys are lowercase Go field names, not chosen names.
- Agent prepares: `testdata/full.yaml` with the current keys and a version with proposed yaml tags.
- [ ] Decision: keep / add tags (which names).

## D5: config keys feel right for a user (from session 9)
- Agent prepares: a list of the user-facing keys in `testdata/full.yaml` with one line each on meaning and default.
- [ ] Decision: rename anything?

## Other open items (known before the review)
- [ ] M12: control API placement.
- [ ] G13: hostlist re-read behaviour of nfqws2/winws2 (unverified).
- [ ] `--fwmark` flag of nfqws2 (unverified).
