# Module: router

| Field | Value |
|---|---|
| Path | internal/router |
| Kind | app |
| Binary | splitter-router |
| Purpose | runs the checker on OpenWrt and keeps podkop's domain lists up to date |
| Consumers | cmd/splitter-router |
| Children | internal/router/podkop |
| Uses libs | internal/lib/checker, internal/lib/analyzer, internal/lib/route, internal/lib/paths, internal/lib/clock, internal/lib/version |
| Build tags | – |
| Status | planned |

## Responsibility

Composition root for the router. On the router, podkop owns routing, capture, DNS and ByeDPI integration. This app only decides lists. Small memory footprint (target ≤ 20 MB RSS).

## Public API

```go
package router

func Main(args []string) int

type Config struct {
    Paths    paths.Paths
    Analyzer analyzer.Config
    Checker  checker.Config
    Podkop   podkop.Config
    Log      LogConfig
}

func LoadConfig(path string) (Config, error) // YAML; UCI integration later via packaging

type Service struct{ /* ... */ }

func New(cfg Config, p paths.Paths) (*Service, error)
func (s *Service) Run(ctx context.Context) error
```

CLI: `splitter-router run`, `splitter-router check-config`, `splitter-router --version`.

## Behavior / contract

- Start the checker → on every `Table` → `podkop.Apply`.
- Probe sockets must bypass podkop's tproxy. This uses the analyzer's direct dialer with `Mark` = `cfg.BypassMark`, and the matching nft rule is provided by packaging and documented (never applied by the app).
- Store flush is coalesced (flash wear).
- Runs as a procd service. Logs go to `/tmp` via paths.

## Config

As above, plus `BypassMark uint32` passed into the analyzer's direct dialer.

## Errors

Exit codes as in `internal/pc`.

## Testing

Wiring test with a fake checker table stream and a fake podkop applier.

## Out of scope

Running sing-box or ByeDPI (podkop does), a web UI (LuCI integration later).

## Bootstrap (M0)

`doc.go`, plus `Main` supporting only `--version` (prints `version.String("splitter-router")`) and `--help`; anything else prints "not implemented yet" and returns 1.
