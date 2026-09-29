# Module: status

| Field | Value |
|---|---|
| Path | internal/pc/status |
| Kind | child |
| Binary | – |
| Purpose | serves a local read-only status page with routes, network and VPS savings |
| Consumers | internal/pc |
| Children | – |
| Uses libs | internal/pc/lib/clashapi, internal/lib/route |
| Build tags | – |
| Status | planned |

## Responsibility

Shows the user what the app is doing. Localhost only.

## Public API

```go
package status

type Snapshot struct {
    Version    string
    Network    string // label (ASN name), never the public IP
    LastCheck  time.Time
    NextCheck  time.Time
    Routes     map[route.Route]int   // domain counts
    Traffic    map[route.Route]int64 // bytes
    Savings    float64               // share of traffic NOT via tunnel
    Processes  []Process
    Warnings   []string
    Recent     []Change              // last route changes
}

type Process struct{ Name, State string; Restarts int }
type Change struct{ Domain string; From, To route.Route; At time.Time }

// Source is implemented by an adapter in the pc app.
type Source interface{ Snapshot() Snapshot }

type Config struct {
    Listen string // default "127.0.0.1:9091"; non-loopback addresses are rejected
    Token  string // optional; if set, required as ?token=
}

type Server struct{ /* ... */ }

func New(cfg Config, src Source, live clashapi.Client) (*Server, error)
func (s *Server) Run(ctx context.Context) error
```

Endpoints: `GET /` (HTML, embedded assets), `GET /api/status` (JSON Snapshot), `GET /api/traffic` (proxied Clash traffic numbers).

## Behavior / contract

- Read-only: no endpoint changes state.
- Binds to loopback only. The listen address is validated.
- No external assets (no CDNs); everything is embedded.

## Config

See above.

## Errors

`New` rejects non-loopback addresses.

## Testing

`httptest` with a fake Source; golden JSON; loopback validation.

## Out of scope

Controls (pause, force route): maybe later, with CSRF protection.

## Bootstrap (M0)

`doc.go` only.
