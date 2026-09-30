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
| Status | implemented |

## Responsibility

Apps never hardcode paths. Runtime data (decisions, logs, secrets) lives outside the repo, in OS-appropriate places with restrictive permissions.

## Public API

```go
package paths

var (
    ErrUnsupported = errors.New("paths: unsupported on this OS")
    ErrACL         = errors.New("paths: could not restrict directory permissions")
)

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
// If Windows ACL restriction fails the directories still exist and the error wraps ErrACL
// (errors.Is(err, paths.ErrACL)); apps may downgrade it to a warning.
func (p Paths) Ensure() error
```

## Behavior / contract

| OS | Config | Data | Logs | Secrets | Run |
|---|---|---|---|---|---|
| Windows | `%ProgramData%\<app>\` | `…\data` | `…\logs` | `…\secrets` | `…\run` |
| Linux | `/etc/<app>/` | `/var/lib/<app>/` | `/var/log/<app>/` | `/etc/<app>/secrets` | `/run/<app>/` |
| OpenWrt (detected via `/etc/openwrt_release`) — **planned, not implemented: `Default` returns `ErrUnsupported`** | `/etc/<app>/` | `/etc/<app>/data` (flash: write rarely) | `/tmp/<app>/logs` | `/etc/<app>/secrets` | `/tmp/<app>/run` |

- OpenWrt is deferred on purpose: the Linux layout would put data and secrets on RAM-backed `/var` and `/run`, so `Default` fails loudly instead. Adding it later means one platform constant, one `detect` rule and one layout function in `resolve.go`.
- The OpenWrt layout is needed before `internal/router` can call `Default`; build it first when the router app starts.
- Windows directories get an ACL for SYSTEM + Administrators only. A failure does not abort `Ensure`: all directories are created and the returned error wraps `ErrACL`, which the app may log as a warning.
- Linux directories get mode 0700 (Secrets) / 0750 (others).

## Config

Apps may override with a `paths.root` config value → `FromRoot`.

## Errors

`Default` fails if `app` is not a plain name (empty, `.`, `..`, or containing `/`, backslash or NUL), if the base location cannot be determined (e.g. `%ProgramData%` unset), or with `ErrUnsupported` on an OS without a layout. `Ensure` returns an error wrapping `ErrACL` when directories were created but the Windows ACL could not be restricted (non-fatal for the app).

## Testing

`FromRoot` layout; `Default` per OS via an injectable env/filesystem probe (Windows, Linux, OpenWrt → `ErrUnsupported`, unknown OS); app name validation; `Ensure` in a temp dir with a fake ACL function (directories, modes, idempotence, `ErrACL` case). The real Windows ACL call is an opt-in test (`OBHOD_TEST_ACL_DIR`) because it locks the directory.

Not verified: directory modes on a real Linux system, the ACL on a `%ProgramData%` path (tried on a scratch directory only), and `chmod` on pre-existing directories owned by another user (returns a plain error).

## Out of scope

Log rotation. Migrating data between versions.

## Bootstrap (M0)

Implemented fully: `Default`, `FromRoot`, `Ensure` and the errors above. No module imports it yet.
