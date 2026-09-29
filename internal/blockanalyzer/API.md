# Module: blockanalyzer

| Field | Value |
|---|---|
| Path | internal/blockanalyzer |
| Kind | app |
| Binary | blockanalyzer |
| Purpose | runs the analyzer from the command line and prints what is blocked and how |
| Consumers | cmd/blockanalyzer |
| Children | – |
| Uses libs | internal/lib/analyzer, internal/lib/route, internal/lib/paths, internal/lib/version |
| Build tags | – |
| Status | design ready (see internal/lib/analyzer/AGENTS.md) |

## Responsibility

A human-facing diagnostic CLI for Stage A and later troubleshooting. All presentation lives here; all logic lives in the analyzer.

## Public API

```go
package blockanalyzer

func Main(args []string) int
```

CLI:

```
blockanalyzer [flags] [domain...]
  -f, --file PATH          domains file (one per line)
  --stages LIST            tcp,tls,http,bulk (default all)
  --fingerprint NAME       chrome|firefox|safari (default chrome)
  --repeats N              default 2
  --concurrency N          default 8
  --calibrate-only
  --json PATH              write the JSON report ("-" = stdout)
  --config PATH            YAML with an analyzer: section
  -v                       show evidence
  --version
```

Output: a table `DOMAIN | VERDICT | BLOCKING | STAGE | SUGGESTED` plus calibration warnings. `-v` adds evidence columns.

## Behavior / contract

- Exit codes: 0 report produced; 1 usage/config error; 2 calibration failed (network down).
- The table is plain text (no colors when stdout isn't a TTY). Column widths are stable for golden tests.
- Reports are written only where asked. Nothing is saved by default.

## Config

Optional YAML file with an `analyzer:` section (`analyzer.Config`).

## Errors

As exit codes above.

## Testing

Golden table/JSON output from a fake Runner; flag parsing.

## Out of scope

Anything stateful (no store, no scheduling).

## Bootstrap (M0)

`doc.go`, plus `Main` supporting only `--version` (prints `version.String("blockanalyzer")`) and `--help`; anything else prints "not implemented yet" and returns 1.
