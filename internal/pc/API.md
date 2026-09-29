# Module: pc

| Field | Value |
|---|---|
| Path | internal/pc |
| Kind | app |
| Binary | splitter |
| Purpose | runs the desktop app: checker, sing-box, desync engine, monitor and status page |
| Consumers | cmd/splitter |
| Children | internal/pc/singbox, internal/pc/desync, internal/pc/procman, internal/pc/monitor, internal/pc/status, internal/pc/lib/clashapi |
| Uses libs | internal/lib/checker, internal/lib/analyzer, internal/lib/route, internal/lib/paths, internal/lib/clock, internal/lib/version |
| Build tags | – |
| Status | planned |

## Responsibility

Composition root for the PC. It loads config, wires modules together, owns the lifecycle, and reacts to `RouteTable` changes by regenerating configs and reloading processes. It contains no domain logic of its own.

## Public API

```go
package pc

// Main parses args, runs the app, and returns the exit code.
func Main(args []string) int

type Config struct {
    Paths    paths.Paths      // optional override (paths.root)
    Analyzer analyzer.Config
    Checker  checker.Config
    SingBox  singbox.Config
    Desync   desync.Config
    Procman  procman.Config
    Monitor  monitor.Config
    Status   status.Config
    Log      LogConfig
}

func LoadConfig(path string) (Config, error) // YAML, strict (unknown keys → error)

type App struct{ /* ... */ }

func New(cfg Config, p paths.Paths) (*App, error)
func (a *App) Run(ctx context.Context) error // blocks; clean shutdown on ctx cancel
```

CLI: `splitter run [--config path]`, `splitter check-config`, `splitter --version`.

## Behavior / contract

Startup order:

1. Load config and secrets (the tunnel outbound from `Paths.Secrets`).
2. Start the checker and wait for the first `Table` (from the store or defaults, without waiting for probes).
3. Generate the desync plan, then start the desync engine.
4. Generate the sing-box config, run `sing-box check`, then start sing-box. The desync engine starts **before** the TUN; this is required.
5. Start the monitor (feeds `checker.ReportFailure`) and the status page.

On a table change, regenerate. If a generated file differs, reload the affected process. A failed `sing-box check` keeps the old config running and logs an error. Shutdown runs in reverse order: status, monitor, sing-box, desync, checker.

Adapters live here:

- monitor failures → `checker.Failure`;
- status `Source` ← checker status + monitor stats + procman status.

## Config

As above. Secrets are never in `app.yaml`; they're referenced by file name inside `Paths.Secrets`.

## Errors

Exit codes: 0 clean shutdown, 1 config/usage error, 2 needs admin (printed with instructions), 3 runtime failure.

## Testing

Wiring test with fakes for all children and libs; config loading golden tests.

## Out of scope

Any logic that belongs in a child or lib.

## Bootstrap (M0)

`doc.go`, plus `Main` supporting only `--version` (prints `version.String("splitter")`) and `--help`; anything else prints "not implemented yet" and returns 1.
