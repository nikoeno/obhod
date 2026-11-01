# Module: pc

| Field | Value |
|---|---|
| Path | internal/pc |
| Kind | child |
| Binary | – |
| Purpose | composes the desktop engine: checker, sing-box, desync engine, monitor and status page |
| Consumers | api/desktop |
| Children | internal/pc/singbox, internal/pc/desync, internal/pc/autodesync, internal/pc/procman, internal/pc/monitor, internal/pc/status, internal/pc/servers, internal/pc/sites, internal/pc/lib/clashapi |
| Uses libs | internal/lib/checker, internal/lib/analyzer, internal/lib/route, internal/lib/paths, internal/lib/clock, internal/lib/version, internal/lib/netif, internal/lib/proc |
| Build tags | – |
| Status | implemented (the control API for the UI lives in api/desktop; the exports it needs are below, including server management, ADR 0004 milestone 4) |

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
    DesyncAuto autodesync.Config // desync_auto: tuning of the automatic preset search (2026-11-01)
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
func (a *App) Snapshot() status.Snapshot // what the status page shows; includes State and Tunnel (status.TunnelReady | status.TunnelNone)

// RouteInfo is one table entry with its per-entry data.
type RouteInfo struct {
    Domain    string
    Route     route.Route
    Source    route.Source
    Since     time.Time       // when the entry got its current route (first table of this run if it never changed)
    LastCheck time.Time       // last finished check cycle; global, not per domain
    History   []status.Change // real route changes of this run, at most 10, oldest first
    Degraded  bool            // Route is tunnel and no tunnel is available (Snapshot.Tunnel == "none"): served direct
    Desync    string          // with desync.preset auto (2026-11-01): DesyncPending | DesyncVerified for a desync site, DesyncFailed for a site no preset reached (Route is then tunnel); "" otherwise
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
    Code     string   // CodePermissionDenied | CodeNeedsSetup | CodeTunnelMissing (a server is in use but unusable) | CodeProgramMissing (sing-box, or the desync engine a site needs, is not found) | CodeCrashed | CodeConnectFailed
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
type EventKind string // EventState "state" | EventRoutes "routes" | EventCheck "check" | EventTraffic "traffic" | EventDesync "desync" (2026-11-01)
type Event struct {
    Kind    EventKind
    Changes []status.Change // routes
    Check   CheckEvent      // check
    Traffic TrafficTick     // traffic
    Desync  DesyncEvent     // desync
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

// Desync presets (2026-11-01, stage 2 of 3; the logic of one search is internal/pc/autodesync, the HTTP side api/desktop).
type DesyncMode string // DesyncModePreset "preset" | DesyncModeAuto "auto" | DesyncModeCustom "custom"
type DesyncStatus struct {
    Mode       DesyncMode
    Preset     string         // catalogue id in use; in auto the one stored for this network (the default preset until a search ends); "custom" in custom
    Searching  bool
    LastSearch *DesyncSearch  // nil until a search ended in this run (not persisted)
}
type DesyncSearch struct { At time.Time; Tried []DesyncTry; Chosen string; Outcome string /* chosen | none | inconclusive | error */ }
type DesyncTry struct { Preset string; Fixed, Total int }
type DesyncPreset struct{ ID, Name, Description string }
type DesyncEvent struct { // counts and catalogue ids only, never a site name
    Phase string // searching | trying | done
    Index, Total int; Preset string                      // trying
    Outcome, Chosen string; Fixed, Of int                // done; Outcome is chosen | none | inconclusive | error | canceled
}
const (DesyncPending = "pending"; DesyncVerified = "verified"; DesyncFailed = "failed")

func (a *App) Desync() DesyncStatus
func (a *App) DesyncPresets() []DesyncPreset // the catalogue in display order
// SetDesync chooses "auto", a catalogue id, or "custom" (only when app.yaml has a strategy). Stored in Paths.Data/desync.json; wins over app.yaml. Applied through the reload path.
func (a *App) SetDesync(ctx context.Context, preset string) error
// StartDesyncSearch starts a search now; returns at once, progress arrives as desync events.
func (a *App) StartDesyncSearch() error
var ErrDesyncPreset, ErrDesyncBusy, ErrDesyncNotAuto, ErrDesyncNoDomain error
var ErrProgramMissing error // SetDesync: a named preset's payload file is missing; Classify maps it to CodeProgramMissing

// Server management (ADR 0004 milestone 4; the logic is in internal/pc/servers, the HTTP side in api/desktop).
// The gateway reaches the servers types only through this alias.
type Server = servers.Server // ID, Name, Protocol, Host, Port, Engine, AddedAt, Active; never a secret

// Servers lists the stored servers, oldest first, never nil. Reads and TestServer do not wait for the lock below.
func (a *App) Servers() ([]Server, error)

// AddServer parses input (a share link or one raw sing-box outbound JSON object), runs the dry run
// (singbox.Generate + sing-box check; without a sing-box binary only the rendering) and stores the
// server. It is never activated, not even the first one. name is optional (1 to 60 printable characters).
func (a *App) AddServer(ctx context.Context, input, name string) (Server, error)

// ActivateServer copies the stored outbound over the tunnel secret file (atomic, 0600) and replaces the
// cached tunnel outbound. Disconnected, or when id is already active: reloaded=false, only the file and the
// cache change. Connected: reloaded=true is returned at once and the reload path applies it: the new
// sing-box config is checked first, sing-box restarts once (never inside the 30 s gap: the restart waits
// for it), and if it does not start with the new config the previous config is restarted and the
// previous tunnel file, cached outbound and active mark are put back; LastError is then connect_failed
// ("The new server could not be used...") and a state event is sent. A second activation while one is
// still waiting for the gap rolls back to the last confirmed server. ErrBusy while connecting or disconnecting.
func (a *App) ActivateServer(ctx context.Context, id string) (reloaded bool, err error)

// DeleteServer removes a server. The active one: allowed while disconnected (the tunnel file is removed and
// the next Connect runs in degraded mode), ErrBusy while connected. ErrBusy during a connect or disconnect.
func (a *App) DeleteServer(ctx context.Context, id string) error

// TestServer is offline: parse (input) or load (id) plus the same dry run, no connection. The protocol is
// returned whenever it is known, also with an error.
func (a *App) TestServer(ctx context.Context, id, input string) (protocol string, err error)

// Error kinds (errors.Is). The ones that come from the input carry a fixed user sentence (an UserMessage()
// method) that names the kind of problem and never a value; no error text, log line or file other than the
// tunnel secret and the stored server files contains a credential.
var (
    ErrInvalidInput        error // malformed or incomplete input, bad name, or rejected by the dry run
    ErrUnsupportedProtocol error // a protocol, transport, cipher or option this version cannot use (including Xray-only ones)
    ErrDuplicate           error // same protocol, host, port and credential already stored
    ErrLimit               error // 20 servers stored
    ErrServerNotFound      error
    ErrInputTooLarge       error // input over 32 KiB
    ErrBusy                error // connecting or disconnecting, or deleting the active server while connected
)

// Per-site rules (user-approved 2026-11-01; the logic is in internal/pc/sites, the HTTP side in api/desktop).
// A rule is a domain (normalized like list entries; it covers subdomains) and an action: direct | desync |
// tunnel | block. Persisted in Paths.Data/sites.json (0600, atomic). The rules reach the checker through
// checker.SetOverrides at start (before the first table) and after every change.
type SiteRule = sites.Rule // Domain, Action, AddedAt

// Sites lists the rules sorted by domain, never nil.
func (a *App) Sites() ([]SiteRule, error)

// SetSite adds or changes the rule of a domain (upsert) and applies it without a restart. created is true for
// a new rule. It does not take the connect/disconnect/reload lock, so it never waits behind a connect.
func (a *App) SetSite(domain, action string) (rule SiteRule, created bool, err error)

// DeleteSite removes the rule: the domain returns to automatic.
func (a *App) DeleteSite(domain string) error

var (
    ErrSiteDomain   error // not a plain domain name (URL, path, port, IP address, wildcard inside, empty)
    ErrSiteAction   error // not direct, desync, tunnel or block ("none" and "proxy" included)
    ErrSiteLimit    error // 500 rules stored
    ErrSiteNotFound error // DeleteSite: no rule for that domain
)

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

1. Pre-flight, in this order: admin rights (exit 2 with the way to start elevated), the owner-only directories, the single-instance lock (`Paths.Run/splitter.lock`; a second app exits with code 3 instead of killing the first one's children), the secrets (the tunnel outbound from `Paths.Secrets`; **in UI mode a tunnel file that does not exist is not an error**: the app starts disconnected with no tunnel outbound, and `Connect` runs in degraded mode (below) until `ActivateServer` has written one. A tunnel file that exists but is not a JSON object, and a missing tunnel file without the UI, still end the start with exit code 1), `procman.CleanupStale` for `sing-box`, `winws2`, `nfqws2` (a failure is only a warning).
2. Start the checker, subscribe to its tables, then take its first `Table` (from the store or defaults, without waiting for probes). The subscription is taken before the first `Table` and handed to the reload loop, so a table published while `Connect` runs (typically the stored decisions, about 1 s after start, once the network is identified) is not lost: it arms the first reload, 3 s after the loop starts. Bind the status port (`status.Server.Listen`): a busy port ends the start with exit code 1 before any tunnel process runs.
3. A host-name tunnel server is resolved with the system resolver (before the TUN exists) and its addresses join the desync excludes; a failed lookup is a warning, and keeps the addresses of an earlier connect. With desync domains and no resolved address the desync plan is refused (exit code 1): `desync.exclude_ips` alone does not count as protection for a host name. Then, when the table has desync domains: `netif.Uplink` (an uplink named like our TUN counts as virtual), the desync plan, the hostlist into `Paths.Run`, then the desync engine (skipped when the plan is empty). On Linux the `inet splitter` table must exist and name the current uplink and excluded addresses (see Errors). This check (exit code 2 with the commands to apply) only fires at start when the **first** table already has desync domains. The first table normally has none (the network is not identified yet), so a missing or outdated `inet splitter` table usually shows up later, as a logged reload error with the same commands (`reload: desync engine not (re)started`); the tunnel keeps running and the engine starts on the first reload after the table is fixed.
4. Generate the sing-box config, write it atomically into `Paths.Run`, run `sing-box check`, then start sing-box (ready when the Clash API answers). The desync engine starts **before** the TUN; this is required.
5. Start the monitor (feeds `checker.ReportFailure`), then the status page.

Degraded mode (user-approved 2026-11-01, "Everyday + Picky only"): `Connect` with **no tunnel outbound in use** (no tunnel file: first run, no server activated, or the active server deleted) no longer fails. `singbox.Generate` gets `Outbounds.TunnelDirect`, so sing-box runs with no `tunnel` outbound, `final` = `direct`, and every domain whose route is `tunnel` (checker default for unknown and blocked sites, user `tunnel` rules) is sent to the `direct` outbound, never anywhere else; `block` still blocks and `desync` works as usual. With desync domains and no tunnel server the engine's exclusion is `192.0.2.1/32` (a documentation address, never routed) because `desync` insists on an address and there is no tunnel to protect; an explicit `desync.exclude_ips` is kept. The route table and the checker are unchanged: a site that needs the detour keeps route `tunnel` and is flagged (`RouteInfo.Degraded`, `Snapshot.Tunnel == "none"`); its traffic counts under `direct`. `Snapshot.Tunnel` describes the running sing-box config while connected (a server activated during the 30 s restart gap shows `none` until the restart is done) and whether a server is in use otherwise. `ActivateServer` while connected in degraded mode switches to full mode through the normal reload path (check, restart after the gap, rollback to the degraded config with `connect_failed` "The new server could not be used. The app keeps running without a detour server."). On Linux the desync `nft` set-up differs between the two modes (the tunnel address is excluded in full mode), so the switch may log the commands to re-apply; the engine then keeps running its old plan. Unchanged: a tunnel file that exists but is invalid ends the start (exit 1); without the UI a missing tunnel file ends the start (exit 1); a server that is in use but whose host name did not resolve, with desync domains, is still `tunnel_missing`; an unstartable sing-box is still `connect_failed`. While disconnected with no server the reload loop does not render anything (the next connect does).

Connected and disconnected: `Disconnect` stops the monitor, sing-box and the desync engine (in that order) and keeps the checker and the status page. `Connect` runs steps 3-5 again. `Connect`, `Disconnect` and reload are serialized; waiting for that lock honours the caller's context, and a `Connect` in progress is cancelled when `Run` ends, so shutdown is never held up by a UI connect.

sing-box failure: when procman gives up on sing-box while connected, or a reload cannot start sing-box with either the new or the previous config, the app disconnects (the state and the status page say so) and logs the last lines sing-box wrote. In terminal mode `Run` then ends with exit code 3; with the UI it stays disconnected until the next `Connect`. When procman gives up on the desync engine, the app forgets it and the next reload starts it again. The output of every process that fails is copied to the app log.

Server management and the reload path: server calls that change state (`AddServer`, `ActivateServer`, `DeleteServer`) take the same lock as `Connect`, `Disconnect` and reload (waiting honours the caller's context); `Servers` and `TestServer` read only. The tunnel outbound is read once at pre-flight and cached; `ActivateServer` replaces the cache, forgets the resolved addresses of a host-name server (the lookup is repeated at the next connect; until then a plan with desync domains is refused and the engine keeps its state, an IP-literal server is excluded at once) and, while connected, asks the reload loop to run at once. With no tunnel outbound the reload keeps the table up to date while disconnected, and re-renders the degraded config while connected.

Reload: on a new table or an uplink change the loop waits 3 s (trailing debounce, on the app's clock), then regenerates both outputs. The desync engine is stopped, started or restarted when its arguments or hostlist changed (any change restarts it: whether the engine re-reads its hostlist is unverified). sing-box is restarted when the bytes of its config changed, at most once per 30 s (a restart drops every connection); a postponed reload is retried after the gap. A config that fails `sing-box check` is dropped before the engine is touched: the old file and process stay and the error is logged. A checked config replaces the old file, which is kept as `sing-box.json.old` until sing-box is ready with the new one; if sing-box does not start with the new config, the old one is put back and sing-box restarted with it. A desync plan that cannot be built (no uplink, the uplink is our TUN) leaves the engine as it is and is logged; sing-box is still updated. On Linux a plan whose nft rules changed (new uplink or tunnel address) restarts the engine only after the table matches, and the routing exemption (`ip rule`, see startup step 3) is present; until then the commands to re-apply are logged. While disconnected only the sing-box file is kept up to date; no process is touched. A change that moves a domain to or from `desync` changes both outputs, so both processes restart (the engine first); a change among `direct`, `tunnel`, `proxy` and `block` restarts sing-box only.

Per-site rules: at start (before the checker's first table) `pc` opens `Paths.Data/sites.json` and gives its rules to `checker.SetOverrides`; an unreadable file is moved to `sites.json.corrupt` and the rules start empty (a warning in the log), a file that cannot be read at all is a warning and the app starts without rules. `SetSite` and `DeleteSite` write the file first, then call `SetOverrides`; the checker publishes a new table, and the reload loop that already handles tables does the rest, so **a rule applies without restarting the engine, also while connected**: `setTable` runs at once (the `routes` event and `Routes()` follow within milliseconds; a change of source only, e.g. checked to user with the same route, has no `routes` event because `route.Diff` ignores it; the client refetches `GET /routes`), and after the 3 s debounce the same reload as for a checker decision regenerates the configs (a change among direct, tunnel, proxy and block restarts sing-box only, once per 30 s; a change to or from desync also restarts the engine). Disconnected: only the sing-box file is kept up to date. A `desync` rule needs the same things as any desync domain (resolved or IP tunnel server, uplink, nft setup on Linux); if the plan cannot be built the reload logs it and sing-box still gets its update. Precedence: see `checker/API.md` ("User overrides"). Rule changes do not take the `op` lock and are not refused during a connect. The log shows counts only, never a domain.

Network identification: the checker gets a DoH resolver (`analyzer.NewDoHResolver(Config.Analyzer, <the same uplink-following direct dialer>)`, endpoints from `analyzer.doh`), so the public IP's ASN is looked up (Team Cymru TXT) from the uplink, never through the TUN, and networks get `asn:<n>` IDs (needs `checker.netwatch.echoendpoints`). The status page names the network by its AS name, else `AS<number>` when only the ASN is known, `This connection` for a `local:` ID (offline fallback from the uplink adapter name + gateway IP + salt, used when neither an ASN nor a gateway MAC/SSID is available, e.g. Windows without echo endpoints; 2026-11-01), else `unknown network`; no address is ever shown or logged.
Checker probes: the checker's direct dialer is pinned to the uplink (`SO_BINDTODEVICE`, so probes never enter the TUN). The uplink is re-resolved when the binding is older than 2 s, on an uplink event, and after a failed dial (retried once when the uplink differs), so an uplink switch is picked up without waiting for another event.

Shutdown runs in reverse order: status, monitor, sing-box, desync engine, checker; each step is bounded by a timeout.

Control API support (see the Public API block): a `state` event carries no data of its own; the receiver reads `State`, `LastError`, `Checking`, `Snapshot` and `Networks` when it gets one. State events are sent on every state transition, when `LastError` is set or cleared (a failed `Connect` records its error before it rolls back, so the final `disconnected` event already carries it) and at the end of a check. `routes` events carry the `route.Diff` of every new table (the same changes as `Snapshot.Recent`). `check` events come from polling `checker.Status().Running` every 500 ms (plus an immediate start event from `CheckNow`, which also ends after about 4 s if the checker never showed the check as running). `traffic` events come every 2 s while connected: totals from the Clash API, per-route bytes from the monitor, aggregated only (no host, no address). The event tickers use `deps.evClk`, which is the app clock unless a test gives it its own. `Classify` (no tunnel outbound is no longer an error, so `tunnel_missing` is only the unresolved host name case) maps `procman.ErrNeedsAdmin` and exit-2 errors without commands to `permission_denied`, the exit-2 error of the Linux nft/ip-rule check (it carries the commands that are still needed) to `needs_setup`, an unresolved or missing tunnel server to `tunnel_missing`, a helper program that cannot be found (sing-box, or the desync engine when some site needs it; since 2026-11-01 also a desync preset's fake payload file) to `program_missing` (additive, 2026-11-01; the message is the fixed sentence "A required program is missing next to the app. Reinstall the app." and the log line `connect failed` carries the error text with the program name), and everything else to `connect_failed`; `crashed` is recorded when sing-box is lost while connected. A connect cancelled by its caller or by shutdown records no error. `Routes` hostnames are the table's entries only (curated lists), never observed traffic.

Adapters live here:

- monitor failures → `checker.Failure`;
- `monitor.RouteOf`: the last element of a connection's chain is the outbound tag, which is the route name; anything else counts as direct;
- the monitor's Clash API client logs when the API stops and starts answering; while it does not answer the status page shows a warning;
- status `Source` ← app state + checker status + monitor stats + procman status; `Savings` is the share of traffic that did not use the tunnel; `Recent` holds the last 20 route changes seen by the app.

The tunnel server is added to `desync.ExcludeIPs` automatically when the tunnel file names it by IP address, or, for a host name, with the addresses it resolved to at connect time, so a correct tunnel file protects itself. A host name that did not resolve blocks the desync engine even when `desync.exclude_ips` is set (the system resolver may also answer differently from the DoH server sing-box uses; an IP address in the tunnel file avoids both). When none of that nor `desync.exclude_ips` gives an address and the table has desync domains, `run` fails with exit code 1 (`check-config` always renders a desync domain, so it needs an address too). Without desync domains no address is needed.

`Config.Validate` (run when the config is loaded, and by `check-config`) also checks the children's fields that would otherwise fail only at connect time: the sing-box fields through a dry `singbox.Generate` (TUN address, FakeIP range, Clash API address and port, DoH server, which defaults to `analyzer.doh[0]` and must then have an IP host), `desync.Config.Check` (strategy options the app sets itself, ports, ranges) and `checker.Config.Validate`. A relative `singbox.binary`/`desync.binary` is made absolute against the current directory.

Desync presets (decided 2026-11-01, stage 1 of 3; see `desync/API.md` and `desync/PRESETS.md`): `desync.preset` selects a ready-made strategy by a stable id (21 of them, translated from the zapret1 Discord/YouTube pack), `custom` (use `desync.strategy`, the raw zapret2 arguments; also what an older config that sets only `strategy` gets) or `auto` (valid since 2026-11-01, see "Desync presets: automatic selection" below; `Validate` accepts it and refuses `strategy` and `ports` with it, as for a named preset). Left empty it means `alt2-multisplit` (the default preset) unless `desync.strategy` is set; the default `app.yaml` written on the first start (`api/desktop`) writes the setting out with comments. A named preset refuses `desync.strategy` and `desync.ports`. A preset names fake payload files that must sit in a `fake` folder next to the engine binary; `buildPlan` checks `Plan.Requires` with `deps.fileExists` (nil in tests: not checked) and a missing file ends the connect like a missing program (`errProgramMissing`, code `program_missing`, the log line names the file and the preset; the fixed UI sentence is unchanged). `check-config` only prints a line for a missing payload file. On Linux the nft check also compares the queue rules' destination ports (`tcp dport`, `udp dport`) with the listing, so a table written for the custom strategy (TCP 80, 443 only) is reported as stale when a preset needs more ports or the UDP rule.

### Desync presets: automatic selection

Decided 2026-11-01 (stage 2 of 3). The search itself (the loop, budget, scoring, the store) is `internal/pc/autodesync`; this module wires it to the engine, the table and the events.

**Modes and the choice.** Mode `preset` (a named id), `auto`, or `custom`. The user's choice through `SetDesync` is kept in `Paths.Data/desync.json` (`selected`) and wins over `desync.preset` of `app.yaml` until it is changed again (`custom` clears it, and only works when `app.yaml` has a strategy). A named preset never starts a search by itself; `StartDesyncSearch` with a named preset is `ErrDesyncNotAuto`. A named preset's payload files are checked when it is chosen (the same files `buildPlan` checks at connect): a missing one is `ErrProgramMissing`, nothing is stored.

**Candidates.** In mode auto, the sites the checker routes to desync with source `checked` or `default` (never a user rule or a list entry) are `pending` until a search has reached them with the stored preset (`verified`) or no preset reaches them (`failed`). The checker is untouched: its decision stays `desync`/`checked`, and `internal/pc` applies the states on top of its table (`effectiveTable`): a `failed` site is served on the **tunnel** route (so with no detour server it is `Degraded`, exactly as a site the checker decided to be tunnel), `pending` and `verified` sites stay `desync` (pending ones run with the default preset until the search ends, which costs nothing if it does not help). `RouteInfo.Desync` carries the state; `Snapshot.Routes` counts a failed site as tunnel. The `routes` events show the move to tunnel like any route change. A failed site is tried again after `desync_auto.failed_ttl` (24 h), on `StartDesyncSearch`, or when its network record is replaced.

**When a search runs.** In the background, never inside `Connect` (it returns as before): after a connect and after every reload, when connected, in mode auto, the network is identified, no search runs, the backoff is over and at least one candidate is `pending` (a new site, a new network, expired failures). `StartDesyncSearch` (the user) takes every desync site, also verified and failed ones, and ignores the backoff. Live failures: the monitor's failures on the desync route for a `verified` site are counted with the checker's own `schedule.failthreshold` / `failwindow`; when they fire, the site goes back to `pending`, the checker is told as before, and a search starts (the stored preset is tried first; at most one such search per `desync_auto.backoff`). A search covers at most `max_domains` sites, pending ones first.

**One search.** The presets are tried in the order of `presetPriority` (`desyncauto.go`; documented in `desync/PRESETS.md`), the network's stored winner first, at most `max_presets` (6). For each, under the op lock: the engine plan is rebuilt for that preset and a hostlist of **only the searched sites** (`trialState`; every reload during the search renders the same plan, so nothing disturbs it), applied with the normal path (`applyEngine`: a restart, the Linux nft check as for a reload), then unlocked; `settle` (2 s) on the app clock; the sites are probed through the real direct dialer by an analyzer runner with its own polite config (`trialAnalyzerConfig`: at most 2 at once, one per address, jitter >= 300-900 ms, no canaries, one attempt, no SNI swap, stages capped at `probe_timeout`). Skipped (payload missing): the preset is not counted. The search stops at the first preset that reaches everything, at the cap, or at the time `budget` (3 min, checked between presets), with a random `pause` (1-3 s) between presets. Progress arrives as `desync` events (`searching`, `trying` i of n, `done`), never with a site name.

**Result and failure handling.**

- *Some preset reaches some site:* the one that reached the most (earlier on a tie) is stored for the network (`autodesync.Record`: preset, time, verified sites, failed sites with the time), the sites it reaches are `verified`, the rest `failed`. Verified sites of an earlier search under the same preset that this search did not probe stay verified; under another preset they become `pending`. The reload path then restarts the engine with the normal plan (all desync sites, the stored preset).
- *Presets were tried and none reached a site:* the default preset stays in use, every searched site is `failed` (tunnel route, `Degraded` without a server), the record keeps the earlier preset. `lastSearch.outcome` is `none`.
- *No usable measurement* (the probes' internet check failed, the engine did not start, a probe error): `inconclusive` or `error`; nothing is marked failed; automatic searches wait `desync_auto.backoff` (10 min).
- *Cancelled* (a disconnect, a shutdown, a switch to a named preset): nothing is stored and `lastSearch` is unchanged; a `done` event with outcome `canceled` is sent.
- *The network changed* while it ran: the result is discarded (`inconclusive`).

**Reuse.** On a later connect on the same network the stored preset is the engine's preset from the first plan on; no search runs unless a candidate is `pending`. Then the search tries the stored preset first (usually one trial).

**Serialisation.** A search takes the op lock only per step (apply a preset), never while probing, so a reload or a disconnect is never blocked for the length of a measurement. `dsMu` guards the search state and is a leaf lock. `Disconnect` ends a running search before it takes the op lock (the search may be waiting for it); the shutdown path goes through `Disconnect`.

**Known limits.** No real engine was run (rules forbid it): the settle time, the hostlist restart and the probe through a working preset are unverified. The checker probes a desync site on `direct`, through the running engine, so it can see `OK` and move the site to `direct` by its hysteresis (checker `DESIGN.md`). The record is per network id; on a `local:` id (Windows, no echo endpoint) it is per adapter and gateway.

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

Linux with desync domains: `run` lists the `inet splitter` nftables table read-only and checks that it exists and names the plan's uplink (`oifname`) and every excluded address and port range (whole addresses and ranges compared as tokens of the listing, the uplink as its quoted match, and since 2026-11-01 the destination ports of the queue rules: the `tcp dport` / `udp dport` keyword and each port as a token; not a full proof). If it is missing (or unreadable without root) or was written for another plan, `run` refuses to start, prints the desync plan's `Setup` commands (and `Teardown` for later) and exits 2. The same check runs `ip rule show` (read-only, no root) and requires the routing exemption from the plan's `Setup` (`ip rule add fwmark 0x40000000/0x40000000 lookup main priority 8999`, the engine's mark sent through the main table before sing-box's `auto_route` rules at priority 9000 and above, which would otherwise take nfqws2's raw packets into the TUN). A listing line counts only if its priority, fwmark value and mask (compared as numbers), and table (`main` or `254`) are all equal and it has no other selector or action (`from all` is the only filler; `iif`, `not`, `suppress_prefixlength`, `goto` and the like make it not match); spacing does not matter. If the rule is missing, `run` exits 2 in the same way, and prints only the commands still needed (the nft commands when the table is missing or stale, the `ip rule add` when the rule is missing, so a rule is never added twice), then `Teardown`. If `ip` cannot be run the check fails with exit 2 (on other systems it reports unsupported and is skipped). The app never applies firewall rules or routes itself. IPv4 only; IPv6 policy routing is not handled.

## Testing

Wiring test with fakes for all children and libs; config loading golden tests. `desyncauto_test.go` (2026-11-01): the search against a fake engine (`fakeProcs`), a scripted probe and a fake clock whose `Sleep` advances time: first preset fixes nothing, second fixes everything (chosen, stored, reused on reconnect without a search, trial hostlist = the failing sites); nothing fixed (default kept, tunnel/degraded, no immediate re-search); failed site retried after its TTL; a named preset never searches; cancellation by disconnect and by shutdown (nothing stored, trial cleared); the preset cap and the settle and pause bounds on the fake clock; the probe config caps (concurrency <= 2); a reload in the middle of a trial keeps it; a skipped preset (missing payload) does not count; live failures re-search with the stored preset first; the explicit search preconditions; `SetDesync` (validation, storage, switch, missing files = program_missing); the effective table; the priority list. **Not covered:** a real `winws2`/`nfqws2`, a real probe through a mangled path, real timing.

## Out of scope

Any logic that belongs in a child or lib.

## Bootstrap

Done: M0 (skeleton), M9 (config and CLI), M10 (App wiring and lifecycle). M12 (control API for the desktop UI) and ADR 0004 milestone 4 (server management, `servers` child) are done. Smoke checklist: `SMOKE.md`.
