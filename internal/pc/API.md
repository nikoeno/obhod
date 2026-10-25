# Module: pc

| Field | Value |
|---|---|
| Path | internal/pc |
| Kind | app |
| Binary | splitter |
| Purpose | runs the desktop app: checker, sing-box, desync engine, monitor and status page |
| Consumers | cmd/splitter |
| Children | internal/pc/singbox, internal/pc/desync, internal/pc/procman, internal/pc/monitor, internal/pc/status, internal/pc/lib/clashapi |
| Uses libs | internal/lib/checker, internal/lib/analyzer, internal/lib/route, internal/lib/paths, internal/lib/clock, internal/lib/version, internal/lib/netif, internal/lib/proc |
| Build tags | – |
| Status | implemented (control API for the UI: open, see PLAN.md M12) |

## Responsibility

Composition root for the PC. It loads config, wires modules together, owns the lifecycle, and reacts to `RouteTable` changes by regenerating configs and reloading processes. It contains no domain logic of its own.

## Public API

```go
package pc

// Main parses args, runs the app, and returns the exit code.
func Main(args []string) int

type Config struct {
    Paths    PathsConfig     // paths.root: put every directory under one folder
    Secrets  SecretsConfig   // file names inside Paths.Secrets: tunnel (required), proxy (optional)
    Analyzer analyzer.Config
    Checker  checker.Config
    SingBox  singbox.Config
    Desync   desync.Config
    Procman  procman.Config
    Monitor  monitor.Config
    Status   status.Config
    Log      LogConfig
}

type LogConfig struct {
    Level     string // debug | info | warn | error; default info
    File      string // file name inside Paths.Logs; default splitter.log
    MaxSizeMB int    // rotate at this size; default 10
    MaxFiles  int    // rotated files to keep; default 3
}

func DefaultConfig() Config
func LoadConfig(path string) (Config, error) // YAML over DefaultConfig, strict (unknown keys → error)
func ParseConfig(r io.Reader) (Config, error)
func (c Config) Validate() error

type App struct{ /* ... */ }

type Option func(*App)
func WithLogger(l *slog.Logger) Option

func New(cfg Config, p paths.Paths, opts ...Option) (*App, error)
func (a *App) Run(ctx context.Context) error // blocks; clean shutdown on ctx cancel
func (a *App) Connect(ctx context.Context) error
func (a *App) Disconnect(ctx context.Context) error
func (a *App) State() State // disconnected | connecting | connected | disconnecting
```

CLI:

```
splitter run [--config PATH] [--ui] [--parent-pid PID]
splitter check-config [--config PATH]
splitter --version | --help
```

`run` connects at once. `run --ui` (used by the desktop app) starts disconnected, with the checker and the status page running, and waits for `Connect`. `--parent-pid` makes the process shut down cleanly when that process exits (an elevated process cannot join the UI's job object). `check-config` loads the config and the secrets, renders the sing-box config and the desync plan once for a table with one domain per route, and runs `sing-box check` when the binary exists; it never prints secrets. The default config is `<Config dir>/app.yaml`; `paths.root` in that file moves the other directories.

## Behavior / contract

Startup order:

1. Pre-flight, in this order: admin rights (exit 2 with the way to start elevated), the owner-only directories, the single-instance lock (`Paths.Run/splitter.lock`; a second app exits with code 3 instead of killing the first one's children), the secrets (the tunnel outbound from `Paths.Secrets`), `procman.CleanupStale` for `sing-box`, `winws2`, `nfqws2` (a failure is only a warning).
2. Start the checker, subscribe to its tables, then take its first `Table` (from the store or defaults, without waiting for probes). The subscription is taken before the first `Table` and handed to the reload loop, so a table published while `Connect` runs (typically the stored decisions, about 1 s after start, once the network is identified) is not lost: it arms the first reload, 3 s after the loop starts. Bind the status port (`status.Server.Listen`): a busy port ends the start with exit code 1 before any tunnel process runs.
3. A host-name tunnel server is resolved with the system resolver (before the TUN exists) and its addresses join the desync excludes; a failed lookup is a warning, and keeps the addresses of an earlier connect. With desync domains and no resolved address the desync plan is refused (exit code 1): `desync.exclude_ips` alone does not count as protection for a host name. Then, when the table has desync domains: `netif.Uplink` (an uplink named like our TUN counts as virtual), the desync plan, the hostlist into `Paths.Run`, then the desync engine (skipped when the plan is empty). On Linux the `inet splitter` table must exist and name the current uplink and excluded addresses (see Errors). This check (exit code 2 with the commands to apply) only fires at start when the **first** table already has desync domains. The first table normally has none (the network is not identified yet), so a missing or outdated `inet splitter` table usually shows up later, as a logged reload error with the same commands (`reload: desync engine not (re)started`); the tunnel keeps running and the engine starts on the first reload after the table is fixed.
4. Generate the sing-box config, write it atomically into `Paths.Run`, run `sing-box check`, then start sing-box (ready when the Clash API answers). The desync engine starts **before** the TUN; this is required.
5. Start the monitor (feeds `checker.ReportFailure`), then the status page.

Connected and disconnected: `Disconnect` stops the monitor, sing-box and the desync engine (in that order) and keeps the checker and the status page. `Connect` runs steps 3-5 again. `Connect`, `Disconnect` and reload are serialized; waiting for that lock honours the caller's context, and a `Connect` in progress is cancelled when `Run` ends, so shutdown is never held up by a UI connect.

sing-box failure: when procman gives up on sing-box while connected, or a reload cannot start sing-box with either the new or the previous config, the app disconnects (the state and the status page say so) and logs the last lines sing-box wrote. In terminal mode `Run` then ends with exit code 3; with the UI it stays disconnected until the next `Connect`. When procman gives up on the desync engine, the app forgets it and the next reload starts it again. The output of every process that fails is copied to the app log.

Reload: on a new table or an uplink change the loop waits 3 s (trailing debounce, on the app's clock), then regenerates both outputs. The desync engine is stopped, started or restarted when its arguments or hostlist changed (any change restarts it: whether the engine re-reads its hostlist is unverified). sing-box is restarted when the bytes of its config changed, at most once per 30 s (a restart drops every connection); a postponed reload is retried after the gap. A config that fails `sing-box check` is dropped before the engine is touched: the old file and process stay and the error is logged. A checked config replaces the old file, which is kept as `sing-box.json.old` until sing-box is ready with the new one; if sing-box does not start with the new config, the old one is put back and sing-box restarted with it. A desync plan that cannot be built (no uplink, the uplink is our TUN) leaves the engine as it is and is logged; sing-box is still updated. On Linux a plan whose nft rules changed (new uplink or tunnel address) restarts the engine only after the table matches, and the routing exemption (`ip rule`, see startup step 3) is present; until then the commands to re-apply are logged. While disconnected only the sing-box file is kept up to date; no process is touched. A change that moves a domain to or from `desync` changes both outputs, so both processes restart (the engine first); a change among `direct`, `tunnel`, `proxy` and `block` restarts sing-box only.

Network identification: the checker gets a DoH resolver (`analyzer.NewDoHResolver(Config.Analyzer, <the same uplink-following direct dialer>)`, endpoints from `analyzer.doh`), so the public IP's ASN is looked up (Team Cymru TXT) from the uplink, never through the TUN, and networks get `asn:<n>` IDs (needs `checker.netwatch.echoendpoints`). The status page names the network by its AS name, else `AS<number>` when only the ASN is known, else `unknown network`; the public IP is never shown or logged.
Checker probes: the checker's direct dialer is pinned to the uplink (`SO_BINDTODEVICE`, so probes never enter the TUN). The uplink is re-resolved when the binding is older than 2 s, on an uplink event, and after a failed dial (retried once when the uplink differs), so an uplink switch is picked up without waiting for another event.

Shutdown runs in reverse order: status, monitor, sing-box, desync engine, checker; each step is bounded by a timeout.

Adapters live here:

- monitor failures → `checker.Failure`;
- `monitor.RouteOf`: the last element of a connection's chain is the outbound tag, which is the route name; anything else counts as direct;
- the monitor's Clash API client logs when the API stops and starts answering; while it does not answer the status page shows a warning;
- status `Source` ← app state + checker status + monitor stats + procman status; `Savings` is the share of traffic that did not use the tunnel; `Recent` holds the last 20 route changes seen by the app.

The tunnel server is added to `desync.ExcludeIPs` automatically when the tunnel file names it by IP address, or, for a host name, with the addresses it resolved to at connect time, so a correct tunnel file protects itself. A host name that did not resolve blocks the desync engine even when `desync.exclude_ips` is set (the system resolver may also answer differently from the DoH server sing-box uses; an IP address in the tunnel file avoids both). When none of that nor `desync.exclude_ips` gives an address and the table has desync domains, `run` fails with exit code 1 (`check-config` always renders a desync domain, so it needs an address too). Without desync domains no address is needed.

`Config.Validate` (run when the config is loaded, and by `check-config`) also checks the children's fields that would otherwise fail only at connect time: the sing-box fields through a dry `singbox.Generate` (TUN address, FakeIP range, Clash API address and port, DoH server, which defaults to `analyzer.doh[0]` and must then have an IP host), `desync.Config.Check` (strategy options the app sets itself, ports, ranges) and `checker.Config.Validate`. A relative `singbox.binary`/`desync.binary` is made absolute against the current directory.

### nfqws2 identity

The app keeps `nfqws2` as root: its renderer always passes `--uid=0:0`, so the engine does not drop privileges (it needs root for NFQUEUE and raw sockets anyway, sing-box runs as root too, and the nft table confines what it sees). A user strategy cannot override it (`desync.Config.Check` rejects `--uid` and `--user`). The hostlist stays `Paths.Run/hostlist.txt`, owner-only 0600, and `--hostlist=hostlist.txt` is relative to the engine's working directory (`Paths.Run`, which stays owner-only).

Evidence (lab, real kernel): started as root with the default drop, nfqws2 logged `Running as UID=2147483647 GID=2147483647`, then `file_open_test: Permission denied` and `cannot access hostlist file '<absolute path of run/hostlist.txt>'`, exit 1 (crash loop). nfqws2 resolves `--hostlist` to an absolute path and, after the drop, needs search (x) on every ancestor directory of it (and re-checks the file at runtime). A readable hostlist would therefore need every ancestor of `Paths.Run` world-searchable, including directories the app does not own (a home directory when `paths.root` points there), which is rejected. A hostlist subdirectory with 0755/0644 was tried and does not help.

## Config

`app.yaml` is decoded over `DefaultConfig` (`KnownFields`: an unknown key is an error); durations are strings such as `5s`. The sections use the snake_case keys from the children's struct tags; the checker's sections have no tags, so their keys are the lowercase Go field names (`checker.schedule.failthreshold`). Differences from the first draft of this contract: `Paths` is `PathsConfig{Root}`, and `Secrets` is new.

Rules checked by `Validate`: the secret and log file names are plain names (no path), `checker.netwatch.salt` must not be set (the app generates it into `Paths.Data/netwatch.salt`), `checker.unknowndefault` must be empty or `tunnel` (the sing-box final route is `tunnel`, see G6), `status.listen` must be loopback, the log level and sizes are valid. `singbox.clash_api` must be loopback (checked when the config is generated). Relative list paths in `checker.lists` are resolved against the directory of `app.yaml`. A clash secret cannot be configured. Secrets are never in `app.yaml`; they're referenced by file name inside `Paths.Secrets`.

The Clash API secret is internal: the app generates a random one (`crypto/rand`) at every start, puts it into the generated sing-box config (0600, in `Paths.Run`) and hands it to its own Clash API client. It is not configurable and never logged.

## Errors

Exit codes: 0 clean shutdown, 1 config/usage error, 2 needs admin or a manual setup step (printed with instructions), 3 runtime failure.

A `procman.ErrNeedsAdmin` from a child also exits 2.

Linux with desync domains: `run` lists the `inet splitter` nftables table read-only and checks that it exists and names the plan's uplink (`oifname`) and every excluded address and port range (whole addresses and ranges compared as tokens of the listing, the uplink as its quoted match; not a full proof). If it is missing (or unreadable without root) or was written for another plan, `run` refuses to start, prints the desync plan's `Setup` commands (and `Teardown` for later) and exits 2. The same check runs `ip rule show` (read-only, no root) and requires the routing exemption from the plan's `Setup` (`ip rule add fwmark 0x40000000/0x40000000 lookup main priority 8999`, the engine's mark sent through the main table before sing-box's `auto_route` rules at priority 9000 and above, which would otherwise take nfqws2's raw packets into the TUN). A listing line counts only if its priority, fwmark value and mask (compared as numbers), and table (`main` or `254`) are all equal and it has no other selector or action (`from all` is the only filler; `iif`, `not`, `suppress_prefixlength`, `goto` and the like make it not match); spacing does not matter. If the rule is missing, `run` exits 2 in the same way, and prints only the commands still needed (the nft commands when the table is missing or stale, the `ip rule add` when the rule is missing, so a rule is never added twice), then `Teardown`. If `ip` cannot be run the check fails with exit 2 (on other systems it reports unsupported and is skipped). The app never applies firewall rules or routes itself. IPv4 only; IPv6 policy routing is not handled.

## Testing

Wiring test with fakes for all children and libs; config loading golden tests.

## Out of scope

Any logic that belongs in a child or lib.

## Bootstrap

Done: M0 (skeleton), M9 (config and CLI), M10 (App wiring and lifecycle). Open: M12 (control API for the desktop UI). Smoke checklist: `SMOKE.md`.
