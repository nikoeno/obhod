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
| Status | implemented |

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

- `GET /api/traffic` returns `{"uploadTotal","downloadTotal","connections"}` from the live Clash API: totals and a count, never a host or an address. If the Clash API fails it answers 502 with a fixed message (no details). With a nil `live` it answers zeros.
- `Listen` must be a loopback IP or `localhost` (resolved: every address must be loopback); a host name otherwise, `0.0.0.0`, `::`, an empty host and a missing port are rejected with `ErrNotLoopback` or a parse error. Port 0 is allowed (the OS picks one; tests use it).
- Every response (also errors) carries `Content-Security-Policy: default-src 'self'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Cache-Control: no-store`. There are no CORS headers; a preflight gets 405.
- The `Host` header must be a loopback IP or `localhost` on the server's port, otherwise 403 (DNS-rebinding guard). Only GET is allowed (everything else 405 with `Allow: GET`).
- With a token, `/`, `/api/status` and `/api/traffic` need `?token=` (constant-time compare, 401 otherwise). The embedded assets (`/assets/app.js`, `/assets/style.css`) need none: they hold no data and a `<script src>` cannot carry the token. The page's script reads the token from its own URL and adds it to its API calls.
- The page has no inline script or style and no external URL (a test greps the embedded files for `http`). Values are inserted with `textContent` only.
- `Run` returns nil after a clean shutdown on ctx cancel. `Snapshot` is encoded with its Go field names.
- Exported beyond the contract: `DefaultListen`, `ErrNotLoopback`.

## Config

See above.

## Errors

`New` rejects non-loopback addresses.

## Testing

`httptest` with a fake Source; golden JSON; loopback validation.

## Out of scope

Controls (pause, force route): maybe later, with CSRF protection.

## Bootstrap (M0)

`doc.go` only. (Implemented in M8.)
