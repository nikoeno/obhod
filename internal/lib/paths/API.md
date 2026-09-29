# Module: paths

| Field | Value |
|---|---|
| Path | internal/lib/paths |
| Kind | lib |
| Binary | – |
| Purpose | resolves per-OS runtime locations for config, data, logs and secrets |
| Consumers | internal/blockanalyzer, internal/pc, internal/router |
| Children | – |
| Uses libs | – |
| Build tags | – |
| Status | planned |

## Responsibility

Apps never hardcode paths. Runtime data (decisions, logs, secrets) lives outside the repo, in OS-appropriate places with restrictive permissions.

## Public API

```go
package paths

type Paths struct {
    Config  string // app.yaml
    Data    string // decisions, caches
    Logs    string
    Secrets string // tunnel credentials; never logged
    Run     string // generated configs, pid files (may be tmpfs)
}

// Default returns the standard locations for app (e.g. "splitter").
func Default(app string) (Paths, error)

// FromRoot puts everything under one directory (portable mode, tests).
func FromRoot(root string) Paths

// Ensure creates missing directories with owner-only permissions.
func (p Paths) Ensure() error
```

## Behavior / contract

| OS | Config | Data | Logs | Secrets | Run |
|---|---|---|---|---|---|
| Windows | `%ProgramData%\<app>\` | `…\data` | `…\logs` | `…\secrets` | `…\run` |
| Linux | `/etc/<app>/` | `/var/lib/<app>/` | `/var/log/<app>/` | `/etc/<app>/secrets` | `/run/<app>/` |
| OpenWrt (detected via `/etc/openwrt_release`) | `/etc/<app>/` | `/etc/<app>/data` (flash: write rarely) | `/tmp/<app>/logs` | `/etc/<app>/secrets` | `/tmp/<app>/run` |

- Windows directories get an ACL for SYSTEM + Administrators only (best effort; failure → warning, not error).
- Linux directories get mode 0700 (Secrets) / 0750 (others).

## Config

Apps may override with a `paths.root` config value → `FromRoot`.

## Errors

`Default` fails only if the base location can't be determined (e.g. `%ProgramData%` unset).

## Testing

`FromRoot` layout; `Default` per OS via an injectable env/filesystem probe; `Ensure` in a temp dir.

## Out of scope

Log rotation. Migrating data between versions.

## Bootstrap (M0)

`doc.go` only.
