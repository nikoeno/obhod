# Module: podkop

| Field | Value |
|---|---|
| Path | internal/router/podkop |
| Kind | child |
| Binary | – |
| Purpose | renders podkop-format domain lists from a RouteTable and triggers a podkop reload |
| Consumers | internal/router |
| Children | – |
| Uses libs | internal/lib/route |
| Build tags | – |
| Status | planned |

## Responsibility

The adapter between our `RouteTable` and podkop's user lists.

## Public API

```go
package podkop

type Config struct {
    ListDir   string                 // where list files are written, e.g. /etc/splitter/podkop
    Files     map[route.Route]string // route → file name referenced in podkop config
    ReloadCmd []string               // e.g. ["/etc/init.d/podkop", "reload"]
    MinReload time.Duration          // debounce reloads; default 60s
}

// Render returns file name → content for every configured route.
func Render(cfg Config, t route.Table) (map[string][]byte, error)

type Applier struct{ /* ... */ }

func NewApplier(cfg Config, run func(ctx context.Context, argv []string) error) *Applier
// Apply writes changed files atomically and reloads podkop if anything changed.
func (a *Applier) Apply(ctx context.Context, t route.Table) (changed bool, err error)
```

## Behavior / contract

- Typical mapping: `tunnel` → the main proxy section's list, `desync` → the ByeDPI section's list, `proxy` → the second section's list. `direct` needs no list. For `block`: if podkop can't express it, write a separate file and report it as unsupported.
- One domain per line in podkop's user-list format. The **exact format and reload command must be verified against the pinned podkop version**.
- Unchanged content → no write, no reload.

## Config

See above; the human configures podkop sections to read these files once.

## Errors

Write/reload failures wrap the file/command.

## Testing

Golden list files; a fake `run` records reload calls; debounce.

## Out of scope

Editing podkop's UCI config, installing podkop.

## Bootstrap (M0)

`doc.go` only.
