# Module: pc

| Field | Value |
|---|---|
| Path | internal/pc |
| Kind | child |
| Binary | – |
| Purpose | composes the desktop engine: checker, sing-box, desync engine, monitor and status page |
| Consumers | api/desktop |
| Children | internal/pc/singbox, internal/pc/desync, internal/pc/procman, internal/pc/monitor, internal/pc/status, internal/pc/lib/clashapi |
| Uses libs | internal/lib/checker, internal/lib/analyzer, internal/lib/route, internal/lib/paths, internal/lib/clock, internal/lib/version, internal/lib/netif, internal/lib/proc |
| Build tags | – |
| Status | implemented (the control API for the UI lives in api/desktop; the exports it needs are below) |

## Responsibility

Composition module for the PC, used by the `api/desktop` app. It loads config, wires modules together, owns the lifecycle, and reacts to `RouteTable` changes by regenerating configs and reloading processes. It contains no domain logic of its own.

## Public API

```go
package pc

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
func WithUI(ui bool) Option        // start disconnected and wait for Connect
func WithParentPID(pid int) Option // Run ends cleanly when that process exits
func WithLogRing(r *LogRing) Option // Logs("splitter") reads from r

func New(cfg Config, p paths.Paths, opts ...Option) (*App, error)
func (a *App) Run(ctx context.Context) error // blocks; clean shutdown on ctx cancel
func (a *App) Connect(ctx context.Context) error
func (a *App) Disconnect(ctx context.Context) error
func (a *App) State() State // disconnected | connecting | connected | disconnecting

// Control API support (used by api/desktop, ADR 0004). All methods are safe for concurrent use.

// Ready is closed when the pre-flight passed, the checker runs and the status port is bound
// (the state is Disconnected or later). If Run fails earlier it is never closed: watch Run's result too.
func (a *App) Ready() <-chan struct{}
func (a *App) Snapshot() status.Snapshot // what the status page shows; includes State

// RouteInfo is one table entry with its per-entry data.
type RouteInfo struct {
    Domain    string
    Route     route.Route
    Source    route.Source
    Since     time.Time       // when the entry got its current route (first table of this run if it never changed)
    LastCheck time.Time       // last finished check cycle; global, not per domain
    History   []status.Change // real route changes of this run, at most 10, oldest first
}
func (a *App) Routes() []RouteInfo // sorted by domain; empty while the table is empty

type NetworkInfo struct {
    Label string // as Snapshot.Network, never the public IP
    Since time.Time
}
func (a *App) Networks() []NetworkInfo // identified networks seen this run, newest last, at most 10

func (a *App) Checking() bool                   // the checker's Status().Running
func (a *App) CheckNow(domains ...string) error // returns at once; progress arrives as check events
var ErrUnknownDomain error                      // CheckNow: none of the domains is in the table

// OpError describes a failed or interrupted transition.
type OpError struct {
    Code     string   // CodePermissionDenied | CodeNeedsSetup | CodeTunnelMissing | CodeCrashed | CodeConnectFailed
    Message  string   // fixed sentence, no addresses, paths or secrets
    Commands []string // needs_setup only; the app never runs them
}
func (a *App) LastError() *OpError // set when a transition fails or the connection drops, cleared when Connect starts
func Classify(err error) *OpError  // maps an error of Connect to an OpError (never returns CodeCrashed)

// Logs: LogSplitter | LogSingBox | LogDesync ("splitter", "sing-box", "desync"); ErrUnknownLog otherwise.
func (a *App) Logs(name string) ([]string, error) // last 200 lines, never nil
var ErrUnknownLog error
type LogRing struct{ /* ... */ }                  // io.Writer keeping the last N lines (split at newlines)
func NewLogRing(lines int) *LogRing
func (r *LogRing) Lines() []string

// Events. The channel has a buffer of 64; a subscriber that falls behind is dropped and its channel closed.
func (a *App) Subscribe() (<-chan Event, func()) // the func unsubscribes (and closes the channel); never blocks the app
type EventKind string // EventState "state" | EventRoutes "routes" | EventCheck "check" | EventTraffic "traffic"
type Event struct {
    Kind    EventKind
    Changes []status.Change // routes
    Check   CheckEvent      // check
    Traffic TrafficTick     // traffic
}
type CheckEvent struct {
    Running bool
    Domains []string // what CheckNow was asked for (normalized); nil for all candidates or a scheduled check
    Changed int      // route changes published while the check ran; set when Running is false
}
type TrafficTick struct {
    Upload, Download int64                // sing-box totals (Clash API); 0 when it did not answer
    Connections      int
    Delta            map[route.Route]int64 // bytes per route since the previous tick
    Total            map[route.Route]int64 // bytes per route since the connect (Snapshot.Traffic)
    Savings          float64
    At               time.Time
}

// Exit codes of the splitter command.
const (
    ExitOK      = 0
    ExitConfig  = 1 // config or usage error
    ExitManual  = 2 // needs admin rights or a manual setup step
    ExitRuntime = 3 // runtime failure
)
func Fail(code int, format string, a ...any) error // an error that ExitCode maps to code
func ExitCode(err error) int                       // exit code for the error of Run or New

// Log setup: stderr plus a size-rotated file in Paths.Logs (Config.Log); the func closes the file.
func NewLogger(cfg LogConfig, p paths.Paths, stderr io.Writer) (*slog.Logger, func(), error)

// check-config: what it needs from the machine (tests replace the fields).
type CheckEnv struct {
    FindBinary func(name, configured string) (string, error)
    Uplink     func(ctx context.Context) (netif.Interface, error)
    SingCheck  func(ctx context.Context, binary, file string) error
}
func DefaultCheckEnv() CheckEnv // binaries next to the executable or in third_party/bin, netif.Uplink, singbox.Check
func CheckConfig(ctx context.Context, cfg Config, p paths.Paths, env CheckEnv, out io.Writer) error
```

The command line (`splitter run`, `check-config`, `--version`, `--help`, `main`) lives in `api/desktop` (ADR 0004); this module no longer parses arguments or owns a binary. In terms of the CLI, `run` connects at once. `run --ui` (used by the desktop app) starts disconnected, with the checker and the status page running, and waits for `Connect`. `--parent-pid` makes the process shut down cleanly when that process exits (an elevated process cannot join the UI's job object). `check-config` (`CheckConfig`) loads the secrets, renders the sing-box config and the desync plan once for a table with one domain per route, and runs `sing-box check` when the binary exists; it never prints secrets. The default config is `<Config dir>/app.yaml`; `paths.root` in that file moves the other directories.

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

Control API support (see the Public API block): a `state` event carries no data of its own; the receiver reads `State`, `LastError`, `Checking`, `Snapshot` and `Networks` when it gets one. State events are sent on every state transition, when `LastError` is set or cleared (a failed `Connect` records its error before it rolls back, so the final `disconnected` event already carries it) and at the end of a check. `routes` events carry the `route.Diff` of every new table (the same changes as `Snapshot.Recent`). `check` events come from polling `checker.Status().Running` every 500 ms (plus an immediate start event from `CheckNow`, which also ends after about 4 s if the checker never showed the check as running). `traffic` events come every 2 s while connected: totals from the Clash API, per-route bytes from the monitor, aggregated only (no host, no address). The event tickers use `deps.evClk`, which is the app clock unless a test gives it its own. `Classify` maps `procman.ErrNeedsAdmin` and exit-2 errors without commands to `permission_denied`, the exit-2 error of the Linux nft/ip-rule check (it carries the commands that are still needed) to `needs_setup`, an unresolved or missing tunnel server to `tunnel_missing`, and everything else to `connect_failed`; `crashed` is recorded when sing-box is lost while connected. A connect cancelled by its caller or by shutdown records no error. `Routes` hostnames are the table's entries only (curated lists), never observed traffic.

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

Exit codes (`ExitCode`; the process exit is done by `api/desktop`): 0 clean shutdown, 1 config/usage error, 2 needs admin or a manual setup step (printed with instructions), 3 runtime failure.

A `procman.ErrNeedsAdmin` from a child also exits 2.

Linux with desync domains: `run` lists the `inet splitter` nftables table read-only and checks that it exists and names the plan's uplink (`oifname`) and every excluded address and port range (whole addresses and ranges compared as tokens of the listing, the uplink as its quoted match; not a full proof). If it is missing (or unreadable without root) or was written for another plan, `run` refuses to start, prints the desync plan's `Setup` commands (and `Teardown` for later) and exits 2. The same check runs `ip rule show` (read-only, no root) and requires the routing exemption from the plan's `Setup` (`ip rule add fwmark 0x40000000/0x40000000 lookup main priority 8999`, the engine's mark sent through the main table before sing-box's `auto_route` rules at priority 9000 and above, which would otherwise take nfqws2's raw packets into the TUN). A listing line counts only if its priority, fwmark value and mask (compared as numbers), and table (`main` or `254`) are all equal and it has no other selector or action (`from all` is the only filler; `iif`, `not`, `suppress_prefixlength`, `goto` and the like make it not match); spacing does not matter. If the rule is missing, `run` exits 2 in the same way, and prints only the commands still needed (the nft commands when the table is missing or stale, the `ip rule add` when the rule is missing, so a rule is never added twice), then `Teardown`. If `ip` cannot be run the check fails with exit 2 (on other systems it reports unsupported and is skipped). The app never applies firewall rules or routes itself. IPv4 only; IPv6 policy routing is not handled.

## Testing

Wiring test with fakes for all children and libs; config loading golden tests.

## Out of scope

Any logic that belongs in a child or lib.

## Bootstrap

Done: M0 (skeleton), M9 (config and CLI), M10 (App wiring and lifecycle). Open: M12 (control API for the desktop UI). Smoke checklist: `SMOKE.md`.
