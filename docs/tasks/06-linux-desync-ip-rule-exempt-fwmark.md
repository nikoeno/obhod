# Exempt nfqws2's fwmark from the TUN's policy routing (Linux desync)

Task card (proposed 2026-10-14, not started). Modules: `internal/pc/desync/nfqws`, `internal/pc/desync`, `internal/pc` (and `internal/pc/singbox` if the fix is on the sing-box side).

## Problem

On Linux, with the sing-box TUN up, the desync route never works. nfqws2 splits the ClientHello correctly, but the split pieces never reach the physical interface.

Evidence from a root run in WSL namespaces (lab test `TestRouting`, `lab/routing_test.go`, finding F5 in `lab/PLAN.md`):

- nfqws2's debug log shows `LUA: multisplit: resolved split pos: 160`, `sending part 1 ... len=160`, `sending part 2 ... len=357` and `rawsend_dissect ... ifout=eth0 fwmark=40000000`, then `packet: id=... drop` for the original. No errors.
- Captures show the client's `eth0`, the ISP side and the server side see only the TCP handshake of that flow. The two pieces appear on the TUN device `splitter`, and TTL-64 resets come back from the TUN stack, which does not know that connection.
- `ip rule show` in the client namespace:

```
9000: from all to 198.19.0.0/30 lookup 2022
9001: from all lookup 2022 suppress_prefixlength 0
9002: not from all dport 53 lookup main suppress_prefixlength 0
9002: from all iif splitter goto 9010
9003: not from all iif lo lookup 2022
9003: from 0.0.0.0 iif lo lookup 2022
```

Table 2022 holds the routes that go through `198.19.0.2 dev splitter`.

## Cause

sing-box's `auto_route` sends every locally generated packet that is not bound to a device into the TUN (rule 9003 `not from all iif lo lookup 2022`). sing-box's own sockets are bound to the physical interface, so they work. nfqws2 injects its pieces through raw sockets with fwmark `0x40000000` (its default, `--fwmark`) and no device binding, so they match rule 9003 and go into the TUN.

The app prints only nft rules (the `inet splitter` table) and never an `ip rule`, so nothing exempts that mark. The app is not allowed to apply routes or firewall rules itself (hard rule in root `AGENTS.md`); it prints the exact commands for the human, as it does for nft.

## Wanted

Make the Linux desync plan include the routing exemption, printed like the nft commands and checked before the engine starts. For example:

- Setup: `ip rule add fwmark 0x40000000/0x40000000 lookup main priority 8999`, with the priority below sing-box's 9000 and the same for `ip -6 rule` if IPv6 is used. Teardown: the matching `ip rule del`.
- The check before starting the engine (today `checkNFT` in `internal/pc/app.go` reads `nft list table inet splitter`) also reads `ip rule show` and refuses with the commands if the rule is missing.
- The mark must stay in sync with nfqws2's `--fwmark`. Today the app never passes `--fwmark` (the default 0x40000000 is used), so use the same constant in one place.
- Check that the main table has the default route via the physical uplink when the rule is applied. Marked packets then leave through it.
- Alternatives to weigh: a sing-box-side exclusion, or binding nfqws2's raw sockets to the uplink if nfqws2 supports it. If you choose another approach, say why.

Update `internal/pc/desync/nfqws/API.md`, `internal/pc/desync/API.md` and `internal/pc/API.md` in the same change. Add golden tests for the new Setup/Teardown text (update goldens only with `-update`) and unit tests for the check with fakes (no root, no network). Do not change public signatures without asking.

## Rules

Read root `AGENTS.md` first: one module per task, `API.md` is the contract, never run routes/firewall commands, print them for the human. Done means `go vet ./...`, `go test ./...`, `go test -tags lab ./...` and the cross builds pass. Do not commit.

## How it is verified

`TestRouting` in WSL, run by the main session. The lab currently applies the `ip rule` itself in the client namespace as a workaround and keeps a failing assertion F5 that checks the app's printed Setup commands contain it.
