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

1. Pre-flight: admin rights (exit 2 with the way to start elevated), `procman.CleanupStale` for `sing-box`, `winws2`, `nfqws2`, the owner-only directories, the secrets (the tunnel outbound from `Paths.Secrets`).
2. Start the checker and take its first `Table` (from the store or defaults, without waiting for probes).
3. `netif.Uplink`, the desync plan, the hostlist into `Paths.Run`, then the desync engine (skipped when the plan is empty). On Linux the `inet splitter` table must exist (see Errors).
4. Generate the sing-box config, write it atomically into `Paths.Run`, run `sing-box check`, then start sing-box (ready when the Clash API answers). The desync engine starts **before** the TUN; this is required.
5. Start the monitor (feeds `checker.ReportFailure`), then the status page.

Connected and disconnected: `Disconnect` stops the monitor, sing-box and the desync engine (in that order) and keeps the checker and the status page. `Connect` runs steps 3-5 again.

Reload: on a new table or an uplink change the loop waits 3 s (trailing debounce, on the app's clock), then regenerates both outputs. The desync engine is stopped, started or restarted when its arguments or hostlist changed (any change restarts it: whether the engine re-reads its hostlist is unverified). sing-box is restarted when the bytes of its config changed, at most once per 30 s (a restart drops every connection); a postponed reload is retried after the gap. A config that fails `sing-box check` is dropped: the old file and process stay and the error is logged. While disconnected only the sing-box file is kept up to date; no process is touched. A change that moves a domain to or from `desync` changes both outputs, so both processes restart (the engine first); a change among `direct`, `tunnel`, `proxy` and `block` restarts sing-box only.

Shutdown runs in reverse order: status, monitor, sing-box, desync engine, checker; each step is bounded by a timeout.

Adapters live here:

- monitor failures → `checker.Failure`;
- `monitor.RouteOf`: the last element of a connection's chain is the outbound tag, which is the route name; anything else counts as direct;
- status `Source` ← checker status + monitor stats + procman status; `Savings` is the share of traffic that did not use the tunnel; `Recent` holds the last 20 route changes seen by the app.

The tunnel server is added to `desync.ExcludeIPs` automatically when the tunnel file names it by IP address, so a correct tunnel file protects itself. When neither that nor `desync.exclude_ips` gives an address, `run` and `check-config` fail with exit code 1.

## Config

`app.yaml` is decoded over `DefaultConfig` (`KnownFields`: an unknown key is an error); durations are strings such as `5s`. The sections use the snake_case keys from the children's struct tags; the checker's sections have no tags, so their keys are the lowercase Go field names (`checker.schedule.failthreshold`). Differences from the first draft of this contract: `Paths` is `PathsConfig{Root}`, and `Secrets` is new.

Rules checked by `Validate`: the secret and log file names are plain names (no path), `checker.netwatch.salt` must not be set (the app generates it into `Paths.Data/netwatch.salt`), `checker.unknowndefault` must be empty or `tunnel` (the sing-box final route is `tunnel`, see G6), `status.listen` must be loopback, the log level and sizes are valid. `singbox.clash_api` must be loopback (checked when the config is generated). Relative list paths in `checker.lists` are resolved against the directory of `app.yaml`. A clash secret cannot be configured. Secrets are never in `app.yaml`; they're referenced by file name inside `Paths.Secrets`.

The Clash API secret is internal: the app generates a random one (`crypto/rand`) at every start, puts it into the generated sing-box config (0600, in `Paths.Run`) and hands it to its own Clash API client. It is not configurable and never logged.

## Errors

Exit codes: 0 clean shutdown, 1 config/usage error, 2 needs admin or a manual setup step (printed with instructions), 3 runtime failure.

A `procman.ErrNeedsAdmin` from a child also exits 2.

Linux with desync domains: `run` checks read-only whether the `inet splitter` nftables table exists. If it is missing, `run` refuses to start, prints the desync plan's `Setup` commands (and `Teardown` for later) and exits 2. The app never applies firewall rules itself.

## Testing

Wiring test with fakes for all children and libs; config loading golden tests.

## Out of scope

Any logic that belongs in a child or lib.

## Bootstrap

Done: M0 (skeleton), M9 (config and CLI), M10 (App wiring and lifecycle). Open: M12 (control API for the desktop UI). Smoke checklist: `SMOKE.md`.
