# Module: desktop

| Field | Value |
|---|---|
| Path | api/desktop |
| Kind | app |
| Binary | splitter |
| Purpose | the splitter app: command line in front of the PC composition module (gateway for the desktop UI, ADR 0004) |
| Consumers | cmd/splitter |
| Children | – |
| Uses libs | – |
| Uses modules | internal/pc, internal/pc/status, internal/lib/route, internal/lib/paths, internal/lib/version, internal/lib/netif (tests only) |
| Build tags | – |
| Status | implemented: CLI and control API (ADR 0004 milestone 3, `internal/pc/PLAN.md` M12; contract below, approved by the human 2026-10-31) |

## Responsibility

Entry point of the `splitter` binary. It parses the command line, loads the config through `internal/pc`, builds the logger, runs `pc.App` and maps its errors to exit codes. With `run --ui` it serves the control API for the desktop UI (ADR 0004 milestone 3; contract in "Control API (M12)" below). It maps and validates; it holds no business logic: lifecycle, startup order and config rules live in `internal/pc`. The read-only status page is still served by `internal/pc/status`.

## Public API

```go
package desktop

// Main parses args, runs the app, and returns the exit code.
func Main(args []string) int
```

CLI:

```
splitter run [--config PATH] [--parent-pid PID]
splitter run --ui --token-file PATH [--listen ADDR] [--addr-file PATH] [--config PATH] [--parent-pid PID]
splitter check-config [--config PATH]
splitter --version | --help
```

`run` connects at once. `run --ui` (used by the desktop app) starts disconnected, with the checker and the status page running, serves the control API (below) and waits for a `connect` request. `--parent-pid` makes the process shut down cleanly when that process exits. `check-config` loads the config and the secrets, renders the sing-box config and the desync plan once, and runs `sing-box check` when the binary exists; it never prints secrets (`pc.CheckConfig`). The default config is `<Config dir>/app.yaml`; `paths.root` in that file moves the other directories. See `internal/pc/API.md` for what the commands do.

## Behavior / contract

Exit codes: 0 ok, 1 config or usage error (including a bad `--listen`, a bad token file and a busy listen port), 2 needs administrator rights or a manual setup step (the message says which), 3 runtime failure. The code of a failed run comes from `pc.ExitCode`. `SIGINT` and `SIGTERM` end `run` and `check-config` through the context. `Main` writes help and `--version` to stdout and errors and usage to stderr; the log goes to stderr and to the rotated file in `Paths.Logs`.

## Config

None of its own: the config is `pc.Config` (`app.yaml`).

## Errors

See exit codes above.

## Testing

CLI tests with fakes for the app, the engine, the paths, the signal context and the check environment (`pc.CheckEnv`); the control API tests are listed under "Testing" in the Control API section. `smoke_integration_test.go` (`integration` tag) runs `check-config` with the pinned sing-box from `third_party/bin`.

## Out of scope

Business logic. Server-management endpoints (milestone 4, sketched under FUTURE below).

## Bootstrap

Done: ADR 0004 milestone 2 (the app role moved here from `internal/pc`) and milestone 3 (the control API, M12).

# Control API (M12)

Status: **implemented**; approved by the human on 2026-10-31, all nine open questions decided as recommended (see "Decisions" at the end). Sections marked FUTURE are not part of milestone 3. "Implementation notes" at the end of the section lists the few choices the contract left open.

The only client is the Rust side of the Tauri app (ADR 0003); the web frontend never calls it. Payload shapes follow `ui/README.md` (the mock) and the Go field names of `status.Snapshot`; deviations from the mock are listed at the end of "Endpoints".

## Command line additions

```
splitter run --ui --token-file PATH [--listen ADDR] [--addr-file PATH] [--config PATH] [--parent-pid PID]
```

- The control API is served only with `--ui`. `--ui` without `--token-file`, and `--token-file`/`--listen`/`--addr-file` without `--ui`, are usage errors (exit 1). `splitter run` from a terminal has no control API.
- `--listen ADDR`: default `127.0.0.1:0` (the OS picks the port). The host must be a loopback IP literal (`127.0.0.0/8` or `::1`); `localhost`, a host name, `0.0.0.0`, `::`, an empty host or a missing port are rejected with exit 1 before anything else starts. Bind failure (port busy) is exit 1.
- Ready line: after the listener is bound **and** the app is ready (pre-flight passed, checker started, state `disconnected`), `splitter` prints exactly one line to stdout, flushes it, and starts serving:

  ```
  OBHOD-API v1 127.0.0.1:54321
  ```

  Format: `OBHOD-API`, one space, `v1`, one space, `host:port` as in `net.Addr.String()` (`[::1]:54321` for IPv6), then `\n`. The UI matches `^OBHOD-API v1 (\S+)$`. It is the only line on stdout in `--ui` mode (logs go to stderr and the log file). If start fails, no line is printed and the exit code says why. Connections made between bind and the line wait in the listen backlog.
- `--addr-file PATH` (proposed, Q3): also write the same `host:port` (no prefix) to PATH, atomically, mode 0600, when the ready line is printed. A Windows `runas` launch cannot capture stdout.
- `--token-file PATH`: see Auth.

## Listener and transport

- Loopback only (see `--listen`). One `http.Server`, HTTP/1.1, no TLS (loopback, token-protected), no HTTP/2.
- `Host` header must be a loopback IP literal or `localhost` with the port the server is bound to, else 403 (DNS-rebinding guard; same rule as `internal/pc/status`, re-implemented here: about 30 lines with their own tests).
- No CORS: no `Access-Control-*` header on any response; `OPTIONS` gets 405. Every response (errors too) carries `Content-Security-Policy: default-src 'self'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Cache-Control: no-store`.
- Timeouts: `ReadHeaderTimeout` 5 s, `ReadTimeout` 10 s, `WriteTimeout` 15 s, `IdleTimeout` 60 s, `MaxHeaderBytes` 16 KiB. The SSE handler clears its own write deadline (`http.ResponseController`).
- Request body: at most 64 KiB (`http.MaxBytesReader`), else 413 `too_large`. A body is optional on POSTs; if present it must be JSON (`Content-Type: application/json`, else 415 `bad_request`) with no unknown fields (400 `bad_request`). GETs take no body.
- Check order: Host (403) → Bearer token (401) → route and method (404, 405 with `Allow`) → body → handler. An unknown path without a token is 401, so paths cannot be probed.
- At most 4 open event streams (the 5th gets 429 `busy`).
- All paths are under `/api/v1/`. JSON is UTF-8, `Content-Type: application/json; charset=utf-8`.

## Auth

- Every request needs `Authorization: Bearer <token>`. No query-string token, no cookies.
- Token handoff (G18): the UI writes a random token (at least 32 bytes of entropy, printable ASCII without spaces, 32 to 256 characters, e.g. base64url) to a file in its own per-user directory and passes only the file path as `--token-file`. `splitter` reads it once at start, trims a trailing `\r\n`, keeps it in memory and never logs, prints or echoes it (not in errors either; it is not on the command line, so not in the process list). It does not delete or modify the file. On Linux a file readable by group or others is refused (Q2).
- Compare with `crypto/subtle.ConstantTimeCompare` on the SHA-256 digests of both sides, so length does not leak.
- Missing, malformed or wrong token: `401`, header `WWW-Authenticate: Bearer`, **empty body**.
- Token file missing, unreadable, empty, too short or long, with forbidden characters, or (Linux) too open: exit **1** (config or usage error) with a message that names the path and the rule but never the content; nothing is bound and no ready line is printed.

## Errors

Error body (except 401): `{ "error": "<sentence for the user>", "code": "<code>" }`, plus `"commands": [..]` for `needs_setup`. `error` never contains secrets, tunnel addresses, file contents or request bodies.

| Code | HTTP | When |
|---|---|---|
| `bad_request` | 400, 415 | malformed JSON, unknown field, wrong type, bad `name`, bad domain |
| `forbidden` | 403 | wrong `Host` |
| `not_found` | 404 | unknown path |
| `unknown_domain` | 404 | `check-now`: none of the domains is in the table |
| `method_not_allowed` | 405 | wrong method (`Allow` is set) |
| `busy` | 409, 429 | 409: `connect` while disconnecting; 429: too many event streams |
| `too_large` | 413 | body over 64 KiB |
| `engine_unreachable` | 503 | the app is shutting down (the listener is still up) |
| `internal` | 500 | unexpected; details only in the log |

These codes are not HTTP errors: they appear in `state.error` (and the `state` event) after a failed or interrupted transition.

| Code | Meaning | Source in `internal/pc` |
|---|---|---|
| `permission_denied` | the app lacks the rights to change routing | `procman.ErrNeedsAdmin`, exit 2 without commands |
| `needs_setup` | one-time manual setup needed; `commands` lists them (never run by the app) | Linux `inet splitter` table / `ip rule` check, exit 2 with commands |
| `tunnel_missing` | tunnel server file missing, invalid or unresolved | secrets / resolve errors, exit 1 |
| `crashed` | sing-box gave up while connected; the app is now disconnected | sing-box lost path |
| `connect_failed` | any other connect failure | other errors |

The Rust side also produces `permission_denied` (UAC or pkexec cancelled) and `engine_unreachable` (process gone) itself. The user sentences live in `docs/UI.md` section 10, not here.

## Endpoints

| Method | Path | Request | Success | Errors |
|---|---|---|---|---|
| GET | `/api/v1/state` | – | 200 State | 401 403 405 503 |
| GET | `/api/v1/routes` | – | 200 `[Route]` | same |
| GET | `/api/v1/processes` | – | 200 `[Process]` | same |
| GET | `/api/v1/logs?name=` | `name` = `splitter` \| `sing-box` \| `desync` | 200 `{name, lines}` | 400 `bad_request` |
| GET | `/api/v1/version` | – | 200 Version | same as state |
| GET | `/api/v1/events` | – | 200 `text/event-stream` | 429 `busy` |
| POST | `/api/v1/connect` | none | 202 `{ok, state:"connecting"}`; 200 `{ok, state}` if already connecting or connected | 409 `busy` while disconnecting |
| POST | `/api/v1/disconnect` | none | 202 `{ok, state:"disconnecting"}`; 200 `{ok, state}` if already disconnecting or disconnected | – |
| POST | `/api/v1/check-now` | optional `{"domains": ["example.org"]}` | 202 `{ok:true, checking:true}` (also when a check already runs) | 400, 404 `unknown_domain` |
| POST | `/api/v1/shutdown` | none | 200 `{ok:true}`, then the app shuts down | – |

**State** (`GET /state`, and the `state` event payload):

```json
{
  "state": "disconnected | connecting | connected | disconnecting",
  "error": null,
  "checking": false,
  "snapshot": { "...status.Snapshot, Go field names..." },
  "networks": [ { "label": "string", "since": "RFC3339" } ]
}
```

- `error` is `null` or `{ "error": string, "code": string, "commands": [string] }` (`commands` only for `needs_setup`). It is set when a transition fails or the connection drops, and cleared when the next `connect` starts.
- `checking` is the checker's `Status.Running`.
- `snapshot` is `status.Snapshot` with its Go names: `Version` (string), `State` (same as `state`), `Network` (label, never the public IP; `unknown network` until identified), `LastCheck`, `NextCheck` (RFC3339; the zero time `0001-01-01T00:00:00Z` when unknown), `Routes` (`{route: domainCount}`), `Traffic` (`{route: bytes}`), `Savings` (float, 0 to 1), `Processes` (`[{Name, State, Restarts}]`), `Warnings` (`[string]`), `Recent` (`[{Domain, From, To, At}]`, the last 20 route changes). Nil slices and maps are encoded as `[]` and `{}`, never `null`. Routes are `direct | desync | tunnel | proxy | block`.
- `networks`: networks seen since the app started (newest last, at most 10), `label` as in `Snapshot.Network`.

**Route** (`GET /routes`): `{ "domain", "route", "source", "since", "lastCheck", "history": [{ "From", "To", "At" }] }`, sorted by domain. `source` is `route.Source` as a string. `since` is when the entry got its current route (the first table of this run for entries that never changed). `lastCheck` is the time of the last finished check cycle (global, not per domain). `history`: at most 10 route changes of this run, oldest first. The list is empty while the table is empty ("still learning").

**Process** (`GET /processes`): `[{ "Name", "State", "Restarts" }]` for `splitter` (always `running`), `sing-box` and `desync` (the `winws2`/`nfqws2` engine). `State` is the procman state (`starting | running | stopping | stopped | failed`); a process that was never started is `stopped`.

**Logs** (`GET /logs?name=`): `{ "name": "sing-box", "lines": ["..."] }`, the last 200 lines. `sing-box` and `desync` come from the procman ring buffers; `splitter` from a ring of the app's own log lines (the same text as the log file). The gateway adds and filters nothing, so the producers' rule applies (full hostnames only when debug is on).

**Version** (`GET /version`): `{ "splitter": "splitter 0.12.0", "singBox": "", "desync": "", "api": "v1" }`. `splitter` is `version.String("splitter")`. `singBox` and `desync` are `""` when unknown (Q5). The UI adds its own version.

**check-now body**: `domains` (optional) is a list of at most 100 names, each normalized with `route.Normalize` (an invalid name is 400). Names not in the table are ignored; if none is in the table: 404 `unknown_domain`. Without `domains` all candidates are checked. The call returns at once; progress is on the event stream.

**connect/disconnect** are asynchronous: the handler starts the operation on the app's run context (not the request's) and returns. The result arrives as `state` events: `connecting`, then `connected`, or back to `disconnected` with `error`. `disconnect` while `connecting` cancels the connect and ends in `disconnected` without an error.

**shutdown**: answers 200, then cancels the run context. Open event streams get a final `shutdown` event and are closed; the listener closes after the response is flushed; `splitter` exits with 0 (or the code `pc.ExitCode` gives). A repeated call before the listener closes answers 200 again.

Deviations from the mock (`ui/README.md`) that the UI must follow:

1. No `trafficToday` in a Route: traffic is counted per route, never per domain (monitor rule). The UI shows per-route totals from `Snapshot.Traffic` (Q6).
2. `lastCheck` of a Route is global, not per domain (Q8).
3. Added: Route `source`. Version has no `ui` (the UI knows its own).
4. `shutdown` really shuts down and closes the streams.
5. The mock-only endpoints (`/mock/*`) do not exist.

## Events (SSE)

`GET /api/v1/events`: `Content-Type: text/event-stream; charset=utf-8`, `Cache-Control: no-store`, no `Content-Length`.

- First bytes: `retry: 2000\n\n`, then one `state` event with the current State, so a client needs no separate GET to start. Each event is `id: N\nevent: <name>\ndata: <one-line JSON>\n\n` with `N` increasing per connection. There is no replay (`Last-Event-ID` is ignored, Q9): after a reconnect the client takes the first `state` event as the truth and refetches `/routes` if it needs the table.
- Keep-alive: a comment `: ping\n\n` every 15 s.
- Slow clients: a buffer of 64 events per stream; when it is full the stream is closed (the client reconnects and resyncs). A slow client never blocks the app.
- The server ends the stream on `shutdown`, when the client disconnects, or when the listener closes.

| Event | When | Payload |
|---|---|---|
| `state` | first event; every state transition; error set or cleared; end of a check | State (as `GET /state`) |
| `routes` | the route table changed (`route.Diff`) | `{ "changes": [ { "Domain", "From", "To", "At" } ] }`; `From` or `To` is `""` for an added or removed entry |
| `check` | a check starts and ends | `{ "running": bool, "domains": [string] or null, "changed": int }` (`changed` only when `running` is false; `domains` echoes what the UI sent) |
| `traffic` | every 2 s while connected | `{ "uploadTotal": int, "downloadTotal": int, "connections": int, "delta": { "<route>": int }, "Traffic": { "<route>": int }, "Savings": float, "At": "RFC3339" }` |
| `shutdown` | last event before the stream closes | `{}` |

Hostname rule (same as the monitor): hostnames appear only where the payload is the route table itself (`routes`, `Recent`, `/routes`: entries of the curated lists, which the Sites page shows anyway). `traffic` is aggregated per route and carries no host or address. `check.domains` only repeats what the client sent. Nothing observed from the user's traffic is exposed through the API unless the app runs with debug logging, and then only through `logs` (Q4).

## What the gateway needs from `internal/pc` (new exports, implemented)

These were the API changes of `internal/pc` approved with this contract; they are documented in `internal/pc/API.md` (where `TrafficTick` has three more fields than sketched here: `Total`, `Savings`, `At`). Already exported and reused: `App.Connect`, `App.Disconnect`, `App.State`, `App.Run`, `Fail` / `ExitCode`, `NewLogger`.

```go
package pc

// Ready is closed when pre-flight passed and the checker runs (state Disconnected or later).
func (a *App) Ready() <-chan struct{}

// Snapshot is what the status page shows (the existing statusSource, exported). Includes State.
func (a *App) Snapshot() status.Snapshot

// Routes is the current table with the per-entry data of the Route payload.
func (a *App) Routes() []RouteInfo
type RouteInfo struct {
    Domain    string
    Route     route.Route
    Source    route.Source
    Since     time.Time
    LastCheck time.Time
    History   []status.Change // at most 10, oldest first
}

func (a *App) Networks() []NetworkInfo // seen this run, newest last, at most 10
type NetworkInfo struct{ Label string; Since time.Time }

func (a *App) Checking() bool                   // checker.Status().Running
func (a *App) CheckNow(domains ...string) error // route.Normalize; ErrUnknownDomain if none is in the table; then checker.CheckNow
var ErrUnknownDomain error

// LastError is set when a transition fails or the connection drops, and cleared by Connect.
func (a *App) LastError() *OpError
type OpError struct{ Code, Message string; Commands []string }
func Classify(err error) *OpError // maps a Connect error to the codes above

// Logs: "splitter" | "sing-box" | "desync"; ErrUnknownLog otherwise.
func (a *App) Logs(name string) ([]string, error)
var ErrUnknownLog error
type LogRing struct{ /* ... */ } // io.Writer keeping the last N lines
func NewLogRing(lines int) *LogRing
func WithLogRing(r *LogRing) Option // api/desktop passes io.MultiWriter(os.Stderr, ring) to NewLogger

// Subscribe delivers events to the gateway; the func unsubscribes. Never blocks the app.
func (a *App) Subscribe() (<-chan Event, func())
type EventKind string // "state", "routes", "check", "traffic"
type Event struct {
    Kind    EventKind
    Changes []status.Change // routes
    Check   CheckEvent      // check: Running bool, Domains []string, Changed int
    Traffic TrafficTick     // traffic: Upload, Download int64; Connections int; Delta map[route.Route]int64
}
```

Notes: a `state` event carries no data of its own; the gateway builds the State payload from `State`, `LastError`, `Checking`, `Snapshot` and `Networks` when it receives one. The 2 s traffic ticker (on the app's `Clock`) lives in `pc` and runs while connected. procman names map to payload names in the gateway (`winws2`/`nfqws2` to `desync`). `shutdown` needs no export: the gateway owns the context it passes to `Run` and cancels it.

Reused types: `status.Snapshot`, `status.Process`, `status.Change`, `route.Route`, `route.Normalize`. The gateway re-implements the Host and loopback checks instead of importing `status` internals (they are unexported, and a lib shared by `api/` and `internal/` is not worth it).

`Uses modules` (the header above): `internal/pc`, `internal/pc/status`, `internal/lib/route`, `internal/lib/paths`, `internal/lib/version`, `internal/lib/netif` (tests only). It does not import `checker`, `procman` or `monitor`; `pc` hides them.

The read-only status page (`internal/pc/status`, `status.listen`) is **kept alongside for now** and removed after the UI ships (Q1). The control server uses neither its port nor its `?token=`.

## Gateway rules (ADR 0004)

- Only maps and validates: JSON to `pc` calls, argument checks, status codes. No decisions, no retries, no state beyond the SSE client list.
- Never logs request or response bodies, the `Authorization` header or the token. It logs one line per request at debug (method, path, status, duration), and warnings for a rejected Host, failed auth (a count, no header values) and start and stop.
- Errors to clients are fixed sentences and codes; internal errors (paths, addresses, wrapped errors) go to the log only.
- Imports only what `Uses modules` lists.

## Testing

`httptest` against a handler built on a small `engine` interface defined in `api/desktop` (consumer-defined, satisfied by `*pc.App`; the fake lives next to it in a `_test.go` file), with a fake clock for the ticks:

1. Token: every endpoint and an unknown path, without or with a wrong token, is 401 with an empty body; the right token passes; token file cases (missing, empty, too short, Linux mode) exit 1 and the message never contains the content.
2. Host: a foreign `Host` is 403 before auth; a loopback IP, `localhost` and the right port pass; a wrong port is 403.
3. Listen: `0.0.0.0:0`, `[::]:0`, `localhost:0`, a host name and a missing port are rejected (exit 1); `127.0.0.1:0` works; the ready line has the exact format and appears only after `Ready`; `--addr-file` content and mode.
4. No CORS: no `Access-Control-*` header on any response; `OPTIONS` is 405; the four security headers are on every response, errors included.
5. Limits: body over 64 KiB is 413; unknown JSON fields are 400; wrong Content-Type is 415; the 5th event stream is 429; the server timeouts are set.
6. Actions call the right methods: `connect`, `disconnect`, `check-now` (domains normalized, unknown domain 404, invalid name 400), `shutdown` (cancels the run context); repeats answer 200; `connect` while disconnecting is 409; the handler returns before `Connect` finishes.
7. State on the event stream: the first event is `state`; `connect` shows `connecting`, `connected`; failures show `disconnected` with `error` for each code (`permission_denied`, `needs_setup` with `commands`, `tunnel_missing`, `crashed`); `routes`, `check` and `traffic` payloads; `: ping` after 15 s (fake clock); a slow client is dropped without blocking others; a reconnect gets a full `state` first.
8. `shutdown` ends every stream with a final `shutdown` event, and the server returns after the response is flushed.
9. Golden JSON in `testdata/` (updated only with `-update`) for each GET: `state` (several states), `routes` (empty and filled), `processes`, `logs` (all three names), `version`; nil slices appear as `[]`.
10. Error bodies: each code in the error tables has a golden body; none contains a secret marker planted in the fake's errors.

## Implementation notes

Choices the contract left open (none changes a decision):

- Files: `server.go` (handler, auth, Host check, payloads, SSE), `engine.go` (the `engine` interface), `token.go` and `tokenperm_{linux,other}.go` (token file), `listen.go` (`--listen` check, `--addr-file`), `cli.go` (flags, `runUI`).
- `--addr-file` is written without a trailing newline, before the ready line; a write error is exit 1 and no line is printed. The file is left in place when the process exits.
- The log ring (`pc.LogRing`, 200 lines) sits first in `io.MultiWriter(ring, stderr)` so a broken stderr cannot starve it.
- `snapshot.Processes` in `GET /state` and the `state` event uses the same three entries as `GET /processes` (`splitter`, `sing-box`, `desync`, with `winws2`/`nfqws2` renamed), so the UI sees one naming. All times are encoded in UTC.
- The event buffer of 64 is applied twice: `pc` drops (and closes the channel of) a subscriber that is 64 behind, and the stream keeps its own buffer of 64 between the engine and the socket, so a client that does not read is dropped even if the engine is fast. Either way the stream just ends.
- `pc` announces a check from `CheckNow` at once and ends it when the checker stops reporting `Running` (or, for a check that never showed as running, after about 4 s); scheduled checks are noticed by polling the checker every 500 ms.
- The keep-alive interval is a field of the server (tests set it to a few milliseconds), because `api/desktop` may not import `internal/lib/clock`.
- Shutdown order: the handler answers and flushes, then cancels the run context; streams get the `shutdown` event as soon as the context ends (also for a signal or `--parent-pid`), every other request is then 503 `engine_unreachable`, and the listener closes after `pc.App.Run` has returned and the streams are gone (bounded waits of 3 s and 5 s).
- A `connect` that is already in flight and a `disconnect` in flight are not started twice. A cancelled connect (disconnect while connecting, shutdown) records no error.
- The gateway logs internal errors only as "see the engine log"; `pc` logs the details itself.

## FUTURE: server management (ADR 0004 milestone 4, not part of milestone 3)

For the tunnel and proxy outbounds. Same transport, auth, limits and error format. **No endpoint ever returns a secret** (keys, UUIDs, passwords, full config text): a server is shown by an id, a name and its non-secret address parts. Request bodies that carry secrets are never logged. This is a sketch; the contract is completed and approved separately.

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/v1/servers` | – | 200 `[{ "id", "name", "kind", "host", "port", "active", "addedAt" }]` |
| POST | `/api/v1/servers` | `{ "name": string, "file": string }` (the imported server file text, at most 64 KiB) | 201 the same object (no secrets); 400 `invalid_server_file` |
| POST | `/api/v1/servers/{id}/activate` | none | 202; takes effect on the next connect or reload; 404 `not_found`; 409 `busy` |
| DELETE | `/api/v1/servers/{id}` | – | 200 `{ok:true}`; 409 `busy` for the active server while connected |
| POST | `/api/v1/servers/test` | `{ "id": string }` or `{ "file": string }` | 200 `{ "ok": bool, "latencyMs": int, "code": string }`; never the remote side's failure details |

Needs `internal/pc` exports for a secrets store (add, list without secrets, activate, delete, test). It also answers `docs/UI.md` section 12, question 1.

## Decisions

Approved by the human on 2026-10-31: every question below is decided as recommended. Changing one later needs the human and an update here.

1. **Keep `internal/pc/status` (page and `status.listen`)?** Decided: keep it for now (milestone 3 only adds the gateway); remove it, its port and `?token=` once the UI ships, as a separate change with `docs/architecture.md` and ADR updates.
2. **Require `--token-file` with `--ui`, and refuse a token file readable by others on Linux?** Decided: yes to both (the API is the only control path in UI mode, and the token the only authentication).
3. **Add `--addr-file`?** A UAC `runas` launch cannot capture stdout, so the ready line may never reach the UI. Decided: yes (the same address, written atomically to a 0600 file in the UI's directory); the UI reads the file or the line, whichever it can.
4. **Hostnames in `/routes`, `Recent` and `routes` events.** Read the monitor rule as: curated-list entries are fine, hostnames observed in traffic are not. Decided: keep as specified; the API then cannot reveal which sites the user actually visited.
5. **`Version.singBox` and `desync`.** Decided: empty strings in v1 (no process is run to ask); fill from `third_party/manifest.yaml` data baked in at build time later.
6. **Per-domain `trafficToday`.** Decided: drop it (it needs per-host accounting, against the monitor rule); the UI shows per-route totals. Say if the Sites page really needs it.
7. **`connect` as 202 plus events** (as the mock does) instead of a blocking call. Decided: keep 202; a blocking call would hold one request for up to 45 s (`startTimeout`).
8. **Per-domain `lastCheck`** (the mock has it). Decided: global only in v1.
9. **Event replay with `Last-Event-ID`.** Decided: no; a reconnect starts with a full `state` event.
