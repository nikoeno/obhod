# Release checklist

For a human, before tagging a release. The lab (Level 3 tests) runs by hand under WSL2 as a smoke test, never in CI and never unattended. Plan and scenarios: [lab/PLAN.md](../lab/PLAN.md), contract: [lab/API.md](../lab/API.md).

## 1. One-time WSL2 setup

In the WSL2 distro (Ubuntu):

```bash
sudo apt-get update
sudo apt-get install nftables curl iproute2 make    # gcc is optional
```

- Go matching the `go` line in `go.mod` (install from go.dev; the distro package is usually too old).
- Kernel features: `nf_tables`, `nft_queue` / `nfnetlink_queue`, NAT, veth and TUN. The stock WSL2 kernel has them as modules.
- Clone the repo into the WSL filesystem (`~/obhod`). On `/mnt/c` it works but builds and tests are much slower.

`lab.Preflight` runs at the start of every lab test and reports in one line what is missing (root, `/dev/net/tun`, `ip`, `nft`, `curl`, the two kernel modules, the pinned `sing-box`) together with the command that fixes each item. The tests skip with that message instead of failing.

The NFQUEUE modules are not persistent: WSL2 forgets them after an idle shutdown or a restart. Run this right before `make e2e`:

```bash
for m in nfnetlink_queue nft_queue tun; do sudo modprobe $m; done
```

## 2. Steps

1. `make fetch`: downloads and verifies the pinned third-party binaries, see [HUMAN-third-party.md](HUMAN-third-party.md). Run it in WSL2 so `third_party/bin/linux-amd64/` is filled.
2. `make build`: `make e2e` also runs it, so the lab tests the binaries in `bin/`.
3. In WSL2: `for m in nfnetlink_queue nft_queue tun; do sudo modprobe $m; done`, then `sudo -E env "PATH=$PATH" make e2e`. It takes about 10 minutes. `make e2e` refuses to run off Linux, and prints this command when it is not root (it never calls sudo itself).
4. On the Windows host: the "Windows" part of [internal/pc/SMOKE.md](../internal/pc/SMOKE.md) (WinDivert and the real adapter are not in the lab).
5. Attach `dist/e2e/<version>-<date>/report.md` to the release. `make e2e` prints its path at the end. Keep the whole folder: it has the process logs.

## 3. Reading the report

`dist/e2e/<version>-<date>/report.md`:

- Header: version, start and finish time, kernel, sing-box version, Go version, and the result line (`N passed, M failed`).
- Scenario table: test, scenario, PASS or FAIL, a note. Notes mark known gaps ("known gap ...") and cases that could not be exercised ("NOT EXERCISED", "SKIPPED"). A release is fine with those notes only if they match what `lab/PLAN.md` lists as known.
- Next to it, one folder per test (`TestRouting-obl3894`, ...) with the logs of every process, also on success: `splitter-N.log` and `.out`, `vps-sing-box.log`.
- `nfqws2-debug.log`: the debug output of the desync engine (routing test).
- `sniff-<host>*.txt`, `desync-sniff.txt`: packet captures on `eth0` (towards the ISP) and on the TUN, per scenario domain.
- On a failure also `*-diag-<host>.txt` (routing diagnostics, ISP state) and the diagnostic dump in the failing test's output.
- `kill9-leftovers.txt`: what a `kill -9` of the app leaves behind (see [RECOVERY.md](RECOVERY.md), Linux).

## 4. What the lab does not cover

- Windows: WinDivert, the Wintun adapter, the elevated start. Covered by SMOKE.md only.
- A real TSPU: the fake DPI reproduces the rules we know (SNI reset, IP block, stalls, poisoned DNS), not the real one.
- IPv6 (the lab is IPv4-only) and real sites.

Safety: the lab touches only its own namespaces named `obl????-*`, `/etc/netns/obl????-*` and temp files. It never changes the routes, DNS or firewall of the WSL root namespace.

## 5. Crash recovery

If a run was killed, `ip netns list` shows `obl????-*` namespaces. Remove them:

```bash
sudo -E env "PATH=$PATH" make lab-clean
```

The next `make e2e` also removes leftovers of crashed runs at its start. For the app itself on a real Linux host see [RECOVERY.md](RECOVERY.md).

## 6. Troubleshooting

| Symptom | Look at |
|---|---|
| Tests skip with `lab preflight: missing ...` | Run the `fix:` commands from the message. After a WSL restart: `for m in nfnetlink_queue nft_queue tun; do sudo modprobe $m; done`. |
| `nfqws2` exits with status 1 | `nfqws2-debug.log` and `splitter-N.log` in the test folder: usually a wrong queue number or nft rule, or the NFQUEUE modules are gone. |
| Desync cases stall or time out | `ip rule show` in the client namespace must have `8999: from all fwmark 0x40000000/0x40000000 lookup main`; without it the engine's packets loop into the TUN. |
| The checker never identifies the network | The lab's ASN echo and DoH servers: see `TestHarness` (echo, DoH, Cymru TXT) and the DoH lines in `splitter-N.log`. |
| Builds and tests take minutes | The repo is on `/mnt/c`. Clone into the WSL filesystem. |
| Leftover namespaces or "File exists" | `make lab-clean`, then run again. Run only one lab process at a time. |
| `make e2e` says it needs root | Use the printed `sudo -E env "PATH=$PATH" make e2e` (plain `sudo` loses `go` from PATH). |
