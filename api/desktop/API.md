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
| Status | implemented: CLI and control API (ADR 0004 milestone 3, `internal/pc/PLAN.md` M12) and server-management endpoints (ADR 0004 milestone 4, `internal/pc/PLAN.md` M13); contracts below, approved by the human 2026-10-31 |

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

Business logic: parsing, validating and storing servers is `internal/pc/servers` behind `pc`.

## Bootstrap

Done: ADR 0004 milestone 2 (the app role moved here from `internal/pc`), milestone 3 (the control API, M12) and milestone 4 (server management, M13).

# Control API (M12)

Status: **implemented**; approved by the human on 2026-10-31, all nine open questions decided as recommended (see "Decisions" at the end). "Implementation notes" at the end of the section lists the few choices the contract left open. Server management (milestone 4) is the section after it.

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
- `--addr-file PATH` (Q3): also write the same `host:port` (no prefix) to PATH, atomically, mode 0600, when the ready line is printed. A Windows `runas` launch cannot capture stdout. A file that already exists at start is deleted before the port is bound (a dead address from a crashed run, decision 10), and the file is removed again when the process exits cleanly (normal end, `shutdown`, signal, `--parent-pid`). Only a crash or kill may leave it behind, so the UI treats a file whose address does not answer as stale.
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
| `bad_request` | 400, 415 | malformed JSON, unknown field, wrong type, bad `name`, bad domain, a missing `input`, `id` and `input` both or neither |
| `forbidden` | 403 | wrong `Host` |
| `not_found` | 404 | unknown path; unknown server id (an id that is not 16 lowercase hex characters is also 404) |
| `unknown_domain` | 404 | `check-now`: none of the domains is in the table |
| `method_not_allowed` | 405 | wrong method (`Allow` is set) |
| `busy` | 409, 429 | 409: `connect` while disconnecting, a server call during a connect or disconnect, deleting the active server while connected; 429: too many event streams |
| `too_large` | 413 | body over 64 KiB; a server link or JSON over 32 KiB |
| `invalid_input` | 422 | server calls: malformed or incomplete link or JSON, forbidden keys, or the sing-box dry run failed |
| `unsupported_protocol` | 422 | server calls: a protocol, transport, cipher or option this version cannot use (including Xray-only ones) |
| `duplicate` | 409 | `POST /servers`: same protocol, host, port and credential already stored |
| `limit_reached` | 409 | `POST /servers`: 20 servers stored |
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
3. Listen: `0.0.0.0:0`, `[::]:0`, `localhost:0`, a host name and a missing port are rejected (exit 1); `127.0.0.1:0` works; the ready line has the exact format and appears only after `Ready`; `--addr-file` content and mode; the file is gone after `shutdown` and after a signal stop; a stale file is deleted at start, before `Ready`.
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
- `--addr-file` is written without a trailing newline, before the ready line; a write error is exit 1 and no line is printed. The file is removed on exit (a `defer` once it is written, so every way out of `runUI` including a failed run) and a pre-existing one is removed first (decisions 10 and 11).
- The log ring (`pc.LogRing`, 200 lines) sits first in `io.MultiWriter(ring, stderr)` so a broken stderr cannot starve it.
- `snapshot.Processes` in `GET /state` and the `state` event uses the same three entries as `GET /processes` (`splitter`, `sing-box`, `desync`, with `winws2`/`nfqws2` renamed), so the UI sees one naming. All times are encoded in UTC.
- The event buffer of 64 is applied twice: `pc` drops (and closes the channel of) a subscriber that is 64 behind, and the stream keeps its own buffer of 64 between the engine and the socket, so a client that does not read is dropped even if the engine is fast. Either way the stream just ends.
- `pc` announces a check from `CheckNow` at once and ends it when the checker stops reporting `Running` (or, for a check that never showed as running, after about 4 s); scheduled checks are noticed by polling the checker every 500 ms.
- The keep-alive interval is a field of the server (tests set it to a few milliseconds), because `api/desktop` may not import `internal/lib/clock`.
- Shutdown order: the handler answers and flushes, then cancels the run context; streams get the `shutdown` event as soon as the context ends (also for a signal or `--parent-pid`), every other request is then 503 `engine_unreachable`, and the listener closes after `pc.App.Run` has returned and the streams are gone (bounded waits of 3 s and 5 s).
- A `connect` that is already in flight and a `disconnect` in flight are not started twice. A cancelled connect (disconnect while connecting, shutdown) records no error.
- `needs_setup` `commands` list only what is still missing, never teardown commands (decision 11); nothing in the code changed for this.
- The gateway logs internal errors only as "see the engine log"; `pc` logs the details itself.

## Server management (ADR 0004 milestone 4, implemented)

Status: **implemented**; contract approved by the human on 2026-10-31, all twelve open questions decided as recommended (see "Server management decisions" at the end of the file). Same transport, auth, limits and error format as above. The logic is not in this module: parsing, validation, storage and activation are the child `internal/pc/servers` (contract: `internal/pc/servers/API.md`), wired by `internal/pc` (exports and behaviour changes: `internal/pc/API.md`). The gateway reaches the types only through the `pc.Server` alias, so `Uses modules` did not change. The `engine` interface grew `Servers`, `AddServer`, `ActivateServer`, `DeleteServer` and `TestServer`, and maps the `pc` error kinds to the codes below.

### Scope in short

The UI adds a tunnel server without hand-editing files: paste a share link (vless incl. Reality, trojan, ss, hy2/hysteria2, vmess, tuic) or one raw sing-box outbound JSON object. One active server, no groups, no failover. A new server is never activated automatically, not even the first one. Subscription URLs, the Xray sidecar (L11) and the `proxy` outbound are not part of v1; anything that needs Xray is `unsupported_protocol`. The input exists only in the request body, in memory and in the stored file; it is never logged and never returned.

### Endpoints

All under `/api/v1/`. Bodies are JSON with the 64 KiB limit above; no response contains a secret.

**Server object** (never has keys, UUIDs, passwords, the link, the JSON or any query parameter):

```json
{ "id": "9f2c41aa07d3e8b1", "name": "Server 1", "protocol": "vless", "engine": "singbox",
  "host": "198.51.100.7", "port": 443, "active": true, "addedAt": "2026-10-31T10:00:00Z" }
```

`host` and `port` are returned in full (decision 17: loopback and token protected); the UI hides them behind a reveal by default. `engine` is `singbox` in v1.

| Method | Path | Request | Success | Errors |
|---|---|---|---|---|
| GET | `/servers` | – | 200 `[Server]`, oldest first, `[]` when none | 401 403 405 503 |
| POST | `/servers` | `{ "input": string, "name"?: string }` (`input` = link or raw JSON; `name` 1-60 printable characters, default from the link fragment or `Server N`) | 201 Server, not activated | 400 `bad_request`, 413 `too_large`, 422 `invalid_input`, 422 `unsupported_protocol`, 409 `duplicate`, 409 `limit_reached` |
| POST | `/servers/{id}/activate` | none | 200 `{ok:true, active:id, reloaded:false}` while disconnected or when already active; 202 `{ok:true, active:id, reloaded:true}` while connected (the reload runs, progress arrives as `state` events) | 404 `not_found`, 409 `busy` (connecting or disconnecting), 422 `invalid_input` (the dry run of the full config fails now) |
| DELETE | `/servers/{id}` | – | 200 `{ok:true}` | 404 `not_found`, 409 `busy` (the active server while connected, or any server while connecting or disconnecting) |
| POST | `/servers/test` | `{ "id": string }` or `{ "input": string }` (exactly one) | 200 `{ "ok": bool, "protocol": string, "code": string }`; `code` is `""`, `invalid_input` or `unsupported_protocol` | 400, 404, 413 |

- `test` is **offline** in v1: parse plus the same dry run, no connection to the server (decision 20; no `latencyMs`). `protocol` is filled whenever it is known, also with `ok:false`.
- The `error` texts of `invalid_input`, `unsupported_protocol`, `duplicate` and `limit_reached` are fixed sentences from `internal/pc/servers` ("The link has no port."), never a value from the input; the gateway passes on only sentences the engine marked as safe (a `UserMessage()` method) and uses a default sentence per code otherwise. An input over 32 KiB (but a body under 64 KiB) is 413 `too_large`.
- Activate while connected (decision 19): the new config is checked first, sing-box restarts once through the reload path (the 30 s restart gap applies: the restart waits for it, the 202 does not), and if it does not start the previous server file, outbound and active mark are put back and `state.error` becomes `connect_failed` ("The new server could not be used. The previous server is still in place."). Activate while disconnected only swaps the file; the dry run still runs.
- Delete of the active server while disconnected is allowed: the `tunnel` file is removed and the next `connect` reports `tunnel_missing`.
- `AddServer`, `ActivateServer` and `DeleteServer` serialize with `Connect`, `Disconnect` and reload on the same lock (waiting honours the request context); `GET /servers` and `POST /servers/test` do not wait for it.
- Request bodies of `POST /servers` and `POST /servers/test` are never logged. The request log line shows the path with `{id}` in place of the id (also for requests that fail authentication).
- No new event kind: the UI refetches `GET /servers` after any server call.
- `internal/pc` starts in UI mode without a tunnel file (a first run has none): the app is disconnected and `connect` answers `tunnel_missing` until a server is in use (decision 21).

### Gateway testing

`httptest` against the fake engine (which mirrors the `ErrBusy` and active-server rules of `pc`): every endpoint with its codes (200 vs 202, busy, not found, bad id shapes, wrong method with `Allow`), the body rules (64 KiB, Content-Type, unknown fields, name rules), golden JSON in `testdata/` for every success and error body (updated only with `-update`), 401/403 on every server route, and a secret-leak test: a marker in the request body and in an engine that echoes it reaches no response, error body or log line, and the log shows `{id}`. The parsers, the store and the rollback are tested in `internal/pc/servers` and `internal/pc` (see their `API.md`).

### `ui/` mock endpoints to add later (list only, nothing touched by this milestone)

`GET/POST /mock/servers`, `POST /mock/servers/{id}/activate`, `DELETE /mock/servers/{id}`, `POST /mock/servers/test`, with a mock engine that checks the link shape and returns the codes above.

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
10. **`--addr-file` lifecycle.** Decided 2026-10-31: the file is removed on a clean exit (normal end, `/shutdown`, signal-driven stop) so a dead address is never left behind; a crash may still leave it. Startup deletes a pre-existing file before binding and writing.
11. **`needs_setup` `commands`.** Decided 2026-10-31: they stay as they are, only what is still missing; no teardown commands are sent through the API.

## Server management decisions

Approved by the human on 2026-10-31 (milestone 4 contract): every open question of the server-management section is decided as recommended. The question text above is kept as history. Changing one later needs the human and an update here.

12. **Inputs in v1.** Decided: share links plus raw sing-box outbound JSON, own Go parsers, standard library only, no new dependency. Subscription URLs later.
13. **First protocol set.** Decided: VLESS (including Reality), Trojan, Shadowsocks, Hysteria2, VMess; TUIC second.
14. **Xray sidecar.** Decided: out of scope (L11 needs a new binary and a separate approval). Only the `engine` field is reserved.
15. **Active servers.** Decided: one active server, no groups, no failover.
16. **Index location.** Decided: `Paths.Secrets/servers/index.json`.
17. **`GET /servers` host and port.** Decided: returned in full (loopback and token protected); the UI hides them behind a reveal by default.
18. **Auto-activation.** Decided: a new server is never activated automatically, not even the first one.
19. **Activate / delete while connected.** Decided: activate reloads sing-box through the existing reload path (one restart, rollback on failure); deleting the active server while connected is 409 `busy`.
20. **`POST /servers/test`.** Decided: offline only in v1 (parse plus the sing-box dry run); no `latencyMs`.
21. **UI mode without a tunnel file.** Decided: `internal/pc` pre-flight no longer fails when the tunnel file is missing in UI mode; the app starts disconnected and `connect` answers `tunnel_missing`. This is an approved behaviour change of `internal/pc` (document it in `internal/pc/API.md`).
22. **Limits.** Decided: 20 servers, input 32 KiB, name 60 characters.
23. **Raw JSON hardening.** Decided: `detour` and bind keys in raw JSON are rejected.
24. **Read-only server calls are lock-free.** Decided 2026-10-31: `GET /servers` and `POST /servers/test` do not take the Connect/Disconnect/reload lock, so a list refresh never waits behind a connect of up to 45 s. Add, activate and delete do take it.
25. **Oversized input.** Decided 2026-10-31: a link or JSON over 32 KiB (body still under the 64 KiB cap) answers 413 `too_large`.
26. **Dry run without a sing-box binary.** Decided 2026-10-31: the dry run then checks only that the config renders and skips `sing-box check` (debug-logged), so a first run before the binary is installed is not blocked.

## Known limits (v1, fixed 2026-10-31)

Deliberate limits of milestone 4. Lifting one is a new decision.

- **Hostname servers while connected.** On activation the resolved addresses are forgotten and re-resolved at the next connect. Until then a desync plan is refused and the engine keeps its state. An IP-literal server updates the exclude list at once. Prefer an IP in the server.
- **Hysteria2 `pinSHA256`** is rejected as `invalid_input` (it cannot be applied).
- **Not supported:** Xray-only transports (xhttp, splithttp, mKCP) and the quic transport (`unsupported_protocol`); subscription URLs; the optional `proxy` outbound; groups, failover and auto-select; a real reachability or latency test (`test` is offline).
- **Numbers:** 20 servers, input 32 KiB, name 60 characters, body 64 KiB, 4 event streams.
- **Not verified when this was written:** the Linux file-mode (0600/0700) test and Linux nft/ip-rule paths with a server change (never run on Linux), `-race` (no cgo on the dev machine), `sing-box check` with a Linux binary, an end-to-end `splitter --ui` run with a real UI, and Windows ACL inheritance of `Paths.Secrets/servers`.
- **One unreproduced test failure.** On 2026-10-31 `go test ./internal/pc/` failed once (12 s run, while many packages were tested in parallel) and passed in 19 later runs (12 repeated, 3 parallel, 4 alone). The failing test was not captured. Suspect: a timing-dependent test in `internal/pc` (fake-clock or event tests). If it shows up again, run with `-count` under load and record the test name here. Resolved 2026-10-31: reproduced by running the compiled `internal/pc` test binary in 20 parallel processes (`-test.count=3 -test.cpu 1,2,8`; 2 of 20 failed). Two harness races, no non-test code was wrong: (1) `TestCheckEventsForScheduledCheckAndGrace` (the 12 s failure, `no [check] event`) advanced `deps.evClk` before `watch` had created its tickers, so the first poll tick was lost; (2) `TestTunnelOnlyChangeRestartsOnlySingBox` cleared the op log after `status.start` but the monitor goroutine started by `Connect` logged `monitor.start` into the window. Fix: every test now waits for `harness.waitRunning` (status started, both event tickers armed, monitor started when not in UI mode) instead of `waitOp("status.start")`. Afterwards 270 loaded `internal/pc` runs, 75 `api/desktop` runs, 50 `internal/pc/servers` runs and 30 `GOMAXPROCS=1` runs passed.
