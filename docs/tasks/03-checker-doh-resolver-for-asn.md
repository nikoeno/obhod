# Pass a DoH resolver to the checker for ASN lookup

Task card `task_f280f989` (proposed 2026-10-11, not started). Module: `internal/pc`.

## Problem

`internal/pc/deps.go` builds the checker with `checker.Deps{Runner, Dialers, DataDir}` and no `Resolver`. So netwatch (`internal/lib/checker/netwatch`) never looks up the public IP's ASN (Team Cymru TXT through DoH). Every network gets a `gw:<hash>` ID, and the status page label is "unknown network" instead of `asn:<n>` with the AS name.

The contract (`internal/lib/checker/API.md`): `Resolver analyzer.Resolver` is the "DoH resolver for network identification; nil means gateway IDs only". The ID rules are in `internal/lib/checker/netwatch/API.md` (`asn:<n>`, fallback `gw:`). The echo endpoints (`checker.netwatch.echoendpoints`) are required for the ASN path, public-IP lookups must go direct (never through the tunnel or TUN), and the public IP is never logged.

Found by the lab tests `TestChecker/1_network_identified_as_AS64500` and `6_status_names_the_network_by_ASN` (`lab/checker_test.go`, finding 1 in `lab/PLAN.md` under L7).

## Wanted

- Build a DoH resolver from the app's analyzer config (`analyzer.Config.DoH`) through the analyzer's public API, using the same direct dialer as the probes so the lookup bypasses the TUN. Pass it as `checker.Deps.Resolver`.
- Make sure the status snapshot's network label comes from the checker's `Network.ASName` and ID as intended.
- Do not hardcode any server addresses (hard rule). The echo endpoints stay user config.
- Add unit tests with fakes (no network, no root).

## Rules

Read root `AGENTS.md` first: one module per task, `API.md` is the contract. Ask before changing public signatures. Done means `go vet ./...`, `go test ./...`, `go test -tags lab ./...` and the cross builds pass. Do not commit.

Card 02 (stale dialer binding) also touches `internal/pc/deps.go`. Keep this change small and focused, and rebase if needed.

## How it is verified

The two lab tests above are re-run in WSL by the main session.
