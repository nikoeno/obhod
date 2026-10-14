# Module: lab

| Field | Value |
|---|---|
| Path | lab |
| Kind | lab |
| Binary | – |
| Purpose | runs end-to-end scenarios in Linux network namespaces with a fake TSPU |
| Consumers | – (go test only) |
| Children | lab/netns, lab/fakedpi, lab/fakeasn, lab/fakedns |
| Uses libs | internal/lib/faultserver, internal/lib/testca, internal/lib/route, internal/lib/analyzer (scenarios: `DecodeReport` of blockanalyzer's JSON report) |
| Build tags | linux (harness and scenarios also `lab`); the address plan, the VPS config renderer, the splitter config and store writers, the nft Setup parser and the Clash wire parser (`addrs.go`, `vps.go`, `splitterconf.go`, `nftsetup.go`, `clashwire.go`) have no tag so their unit tests run on any OS |
| Status | in progress: harness, checker, blockanalyzer, routing and lifecycle scenarios done (L5-L9, 76 scenarios pass as root in WSL); runbook and release report L10 open (plan: PLAN.md) |

## Responsibility

Level 3 testing: a fake internet with two "ISPs", a fake TSPU in the path, a fake VPS, and our real binaries (`splitter`, `blockanalyzer`, `sing-box`, `nfqws2`) as the client, driven by Go tests. Each fake endpoint logs what it observed, so a scenario proves which route a request took instead of guessing it from packets.

It needs root. It is run by hand under WSL2 on the developer's machine as a smoke test before each release (attended, never unattended), and writes a report kept with the release. It is not a CI job.

## Public API

```go
//go:build linux

package lab

type Profile struct {        // loaded from lab/profiles/*.yaml
    Name     string
    ASN      uint32
    Rules    []fakedpi.Rule
    Poisoned map[string]netip.Addr // plain-DNS answers the ISP resolver fakes (name → stub IP)
}

// LoadProfiles reads dir/*.yaml in file name order (name, asn, rules: [{kind, domains, prefix,
// bytes}], poisoned: {name: stub}). Unknown keys or rule kinds and invalid names are errors.
func LoadProfiles(dir string) ([]Profile, error)

type Lab struct{ /* ... */ }

// Preflight reports what the host lacks (root, /dev/net/tun, ip, nft, curl, kernel features such as
// nf_tables, nft_queue, NAT, veth; the binaries). Up calls it and skips the test with its message.
func Preflight() error

// Up builds namespaces: client, one per profile (ISP with fakedpi and a plain DNS resolver),
// inet (fake sites, fake DoH, ASN echo) and vps (sing-box server: VLESS+Reality for `tunnel`,
// SOCKS for `proxy`). The first profile is the client's ISP; with no profiles, lab/profiles is
// loaded. Teardown is registered with t.Cleanup and always runs.
func Up(t *testing.T, profiles ...Profile) *Lab

func (l *Lab) UseISP(name string)                     // switch client uplink (network change)
func (l *Lab) SetRules(isp string, r ...fakedpi.Rule) // simulate a TSPU update
func (l *Lab) SetISPLink(isp string, up bool)         // the ISP's lan side down/up: a dead modem (client link stays up)
func (l *Lab) DPI(isp string) *fakedpi.Engine         // the ISP's fake TSPU (Seen, Stats)
func (l *Lab) DoH() *fakedns.Server                   // the fake DoH server (query log)

// AddSite starts a fake site in inet with its own address and adds its names to the DoH zone.
func (l *Lab) AddSite(name string, s faultserver.Spec) Site
func (l *Lab) Site(name string) Site

type Site struct {
    Addr   netip.AddrPort
    Server *faultserver.Server // Conns: who connected, with which SNI
}

// Addresses as a site sees them: the client's NAT address per ISP, and the VPS (which also serves the proxy route).
func (l *Lab) ClientAddr(isp string) netip.Addr
func (l *Lab) VPSAddr() netip.Addr

func (l *Lab) Exec(ctx context.Context, argv ...string) (stdout, stderr []byte, err error) // in the client ns
func (l *Lab) Curl(ctx context.Context, args ...string) (stdout []byte, err error)         // curl in the client ns, test CA trusted
func (l *Lab) HTTPGet(ctx context.Context, url string) ([]byte, error)                     // a Go request from the client ns

func (l *Lab) Binary(name string) string // LAB_BIN_DIR/<name> when set (release artifacts), else built from ./cmd/<name>

// RunSplitter writes app.yaml, tunnel.json (and proxy.json), the check lists, the netwatch salt
// and the seeded decisions into a fresh paths.root and starts `splitter <Args> --config <app.yaml>`
// in the client namespace (test CA in SSL_CERT_FILE; stdout/stderr to splitter-<n>.out in the run
// dir). Stopped in t.Cleanup (SIGTERM, then SIGKILL after 30 s); the app log is copied to the run dir.
func (l *Lab) RunSplitter(ctx context.Context, o SplitterOptions) *Splitter

type SplitterOptions struct {
    Args       []string               // e.g. "run" or "run", "--ui" (default "run"); --config is added
    AppYAML    string                 // deep-merged over the lab defaults (lab DoH, echo, binaries); must not move paths.root
    Candidates []string               // the checker's candidate list; the Decisions domains are added
    Decisions  map[string]route.Route // seeded into decisions.json for the current ISP's network
    Static     map[string]route.Route // proxy/block list entries

    // OtherDecisions seeds the stored decisions of other ISPs' networks (ISP name -> domain -> route),
    // for scenarios that switch the uplink; their domains join the candidates.
    OtherDecisions map[string]map[string]route.Route

    // ReuseRootOf starts a second process on the SAME paths.root as that splitter (single-instance
    // lock test). Nothing under the root is rewritten (lists, secrets, salt, decisions); the config goes
    // to app-<n>.yaml. Decisions, Candidates, Static and OtherDecisions must be empty; AppYAML is
    // merged over the first process's config and must not move paths.root.
    ReuseRootOf *Splitter
}

type Splitter struct{ /* ... */ }

func (s *Splitter) Wait(ctx context.Context) (exitCode int, err error)
func (s *Splitter) Log() string                          // app log so far
func (s *Splitter) Status(ctx context.Context) ([]byte, error) // GET /api/status, raw JSON
func (s *Splitter) Decisions() ([]byte, error)           // raw decisions.json
func (s *Splitter) PID() int
func (s *Splitter) Kill()                                // SIGKILL, for orphan tests
func (s *Splitter) Clash(ctx context.Context) ([]ClashConn, error) // live connections of the Clash API (secret read from <root>/run/sing-box.json)

// Report records one scenario result for the release report.
func (l *Lab) Report(scenario string, pass bool, note string)
```

## Behavior / contract

- Scenarios live as `*_test.go` in this package (`//go:build linux && lab`, because they use `testca`), for example:
  - blockanalyzer classifies each rule correctly;
  - checker network switch, hysteresis, re-decision after a rule change;
  - every route is taken as the table says (proven by site connection logs, the fakedpi log and the DoH query log);
  - nfqws desync defeats the naive-SNI rule but not the reassembling rule;
  - live failures, kill -9 without orphans, nft table checks.
- `TestRouting` (L8, `routing_test.go`): one `splitter run` throughout; finding F3 (the table published during Connect is lost, so the exit-2-at-start path is unreachable) is recorded as its own check and a link bounce forces the reload; the nft commands the app logs on that reload (applied only inside the client namespace, only text `parseNFTSetup` accepts: `nft add`/`flush` on `inet splitter`), ; one site per route plus nested overlaps; each is fetched with `curl --curves X25519` and checked against the site log, the fakedpi log and the Clash API (`Splitter.Clash`, `ClashConn`: host and last chain element); QUIC block, FakeIP/D1/LAN-resolver findings assert the current behaviour. The checker is held still (1 h schedule, 50 confirmations). The only fixed wait is the 2 s of the QUIC negative (nothing to poll for).
- `TestLifecycle` (L9, `lifecycle_test.go`): one `splitter run` (40 s check interval, bulk stage on, confirmations 2), in order: a `Stall` rule added on a direct domain while connected (the stalled flows are checked in the Clash API inside the monitor's window; the monitor logs nothing and the status shows no failure counts, so the failure-triggered recheck is recognised as a store write that changes that one decision while a periodic check rewrites all; route `direct` to `tunnel` after the confirmations; sing-box restarted, engine untouched; the new request exits at `VPSAddr`); a NaiveSNI rule moves a domain to `desync` (the app log shows the nfqws2 start before the sing-box restart, new pid, hostlist file, request succeeds through the `desync` outbound; answers G13: the app restarts the engine on any hostlist change); `UseISP("ispb")` (nft listing no longer names the uplink: `reload: desync engine not (re)started` with only the missing commands, the test applies them in the client namespace, a link bounce forces the reload, the engine restarts); a second instance on the same root (exit 3, the first stays `connected`, its children keep their pids); `Kill()` (sing-box and nfqws2 gone by Pdeathsig, TUN gone, the VPS sing-box untouched, curl works through the leftover `queue ... bypass` rule; the leftovers are written to `kill9-leftovers.txt` and the report). `pin-desync.test` (Reassemble rule on both ISPs) keeps a desync domain in every table, so the engine plan survives the checker's flapping of NaiveSNI-only domains.
- Tests skip (not fail) when not Linux, not root, or when `Preflight` finds a missing feature.
- No changes outside the namespaces it creates and `/etc/netns/<prefix>*` (per-namespace `resolv.conf`). Namespaces use a unique prefix per run and are removed in teardown; leftovers of a crashed run are removed at the next start.
- Address plan (constants in `addrs.go`): client `eth<i>` 192.168.<10(i+1)>.2/24 ↔ ISP `lan` .1 (also its plain DNS on :53); ISP `wan` 203.0.113.<4i+1>/30 (its SNAT address = `ClientAddr`) ↔ inet `isp<i>` .<4i+2>; VPS `eth0` 203.0.113.250/30 (= `VPSAddr`) ↔ inet `vps` .249. On inet's loopback: DoH 198.51.100.2:443, ASN echo 198.51.100.3:80, the VPS's plain resolver 198.51.100.4:53, sites from 198.51.100.10 (the first is `reality.test`, the Reality handshake target). Each ISP queues forwarded lan↔wan traffic to its fakedpi (`table inet obldpi`, NFQUEUE 10+i) and SNATs to its wan address (`table ip oblnat`).
- Each ISP serves its block page on every stub address of its `poisoned` list: the address on the ISP's loopback, plain HTTP on :80 answering `302` to `http://block.<isp>.test/` (faultserver `HTTPStub`), so a client that follows the poisoned answer meets an ISP stub as in a real network.
- The VPS runs a copy of the pinned sing-box from the lab's temp dir (never from `third_party/bin/...`: `splitter`'s pre-flight `procman.CleanupStale` kills every `sing-box`/`nfqws2`/`winws2` below the folder of its own sing-box, and the PID namespace is shared) with VLESS+Reality on :443 and SOCKS5 with a username/password on :1080; its config is validated with `sing-box check` before start and its log is kept in the run dir. The matching client parameters stay in the Lab (unexported) for `RunSplitter`.
- At `Up`, namespaces and `/etc/netns` folders named `obl<4 hex>-*` (crashed runs) are removed through `netns.Topology.Teardown`. This also removes a concurrently running lab: run one lab at a time.
- Addresses: client↔ISP links in `192.168.0.0/16` (excluded from the TUN, like a home LAN); sites, DoH, echo and VPS in `198.51.100.0/24` and `203.0.113.0/24` (inside the TUN, outside FakeIP and the TUN address). Client interfaces are named `eth0`, `eth1` so `netif` counts them as physical.
- The test CA lives in temp files only and is passed per process (`SSL_CERT_FILE`, `curl --cacert`); it is never installed.
- `RunSplitter` defaults (merged under `AppYAML`): `paths.root` = a fresh temp dir; `secrets` tunnel.json/proxy.json (the VPS's client parameters); `analyzer` with the lab DoH, controls `control-a.test`/`control-b.test`, canary `canary.test`, neutral SNI `neutral.test`, `fingerprint: firefox`, concurrency 2, short timeouts, no bypass processes; `checker` lists in `<root>/lists`, schedule interval 15 s, jitter 2 s, min gap 10 s, `decide.confirmations: 2`, `netwatch` debounce 1 s, refresh 5 s, echo `http://198.51.100.3/`; `singbox.binary` = the pinned sing-box, `singbox.dns.direct_doh` = the lab DoH; `desync.binary` = the pinned nfqws2 when present; `status.listen: 127.0.0.1:19091` (client namespace); `log.level: debug`. A scenario adds the sites the defaults name (`AddSite`).
- Seeded decisions (store schema 1, `source: checked`, 24 h TTL) are written under every ID the checker may give the current ISP: `asn:<n>` (`asn:<n>/gw:<hash>` with `includegateway`) and `gw:<hex(sha256(salt|gatewayMAC|))[:16]>`, from the salt `RunSplitter` writes into `<root>/data/netwatch.salt` and the ISP's `lan` MAC.
- Every wait is a poll with a deadline; no fixed sleeps (except waiting out a negative assertion such as "no datagram arrived").
- The run writes `dist/e2e/<version>-<date>/report.md` (`<version>` = `git describe --always --dirty` or `dev`, `<date>` = `YYYYMMDD-HHMMSS`; `TestMain` writes it after the tests, only when a lab test got past `Preflight`) (scenario results, versions of splitter, sing-box, nfqws2, the kernel version) and keeps every process log next to it.

## Config

Profiles in `lab/profiles/*.yaml`. Environment: `LAB_BIN_DIR` (prebuilt binaries), `LAB_REPORT_DIR` (default `dist/e2e`).

## Errors

Setup failures fail the test with the failing command.

## Testing

It *is* the tests.

## Out of scope

Windows (`internal/pc/SMOKE.md`, run in the same release session), real TSPU behavior.

## Bootstrap (M0)

`doc.go` only (with `//go:build linux`). Also create `lab/profiles/.gitkeep`.
