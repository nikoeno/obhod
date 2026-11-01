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
| Uses modules | internal/pc, internal/pc/status, internal/lib/route, internal/lib/paths, internal/lib/version, internal/lib/netif (tests only), internal/lib/checker/lists (tests only) |
| Build tags | – |
| Status | implemented: CLI and control API (ADR 0004 milestone 3, `internal/pc/PLAN.md` M12) and server-management endpoints (ADR 0004 milestone 4, `internal/pc/PLAN.md` M13); contracts below, approved by the human 2026-10-31; per-site rules (`/sites`, user-approved 2026-11-01, Go side only) |

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
splitter run --ui --token-file PATH [--listen ADDR] [--addr-file PATH] [--error-file PATH] [--config PATH] [--parent-pid PID]
splitter check-config [--config PATH]
splitter --version | --help
```

`run` connects at once. `run --ui` (used by the desktop app) starts disconnected, with the checker and the status page running, serves the control API (below) and waits for a `connect` request. `--parent-pid` makes the process shut down cleanly when that process exits. `check-config` loads the config and the secrets, renders the sing-box config and the desync plan once, and runs `sing-box check` when the binary exists; it never prints secrets (`pc.CheckConfig`). The default config is `<Config dir>/app.yaml`; `paths.root` in that file moves the other directories. With `run --ui` and no `--config`, a missing default `app.yaml` is created on start (see "First start" below). See `internal/pc/API.md` for what the commands do.

## Behavior / contract

Exit codes: 0 ok, 1 config or usage error (including a bad `--listen`, a bad token file and a busy listen port), 2 needs administrator rights or a manual setup step (the message says which), 3 runtime failure. The code of a failed run comes from `pc.ExitCode`. `SIGINT` and `SIGTERM` end `run` and `check-config` through the context. `Main` writes help and `--version` to stdout and errors and usage to stderr; the log goes to stderr and to the rotated file in `Paths.Logs`.

## First start (default config)

Decided 2026-11-01 (the desktop app launches `run --ui` elevated with no `--config`, and on a fresh Windows machine `<ProgramData>\splitter\app.yaml` does not exist yet):

- Only in `run --ui` and only when `--config` is **not** given: if the default `<Paths.Config>/app.yaml` does not exist, `splitter` creates the directories with `paths.Ensure` (owner-only; an `ErrACL` failure is a warning on stderr, as in `pc`) and writes a default `app.yaml`, then continues normally. The file is written to a private temporary file and hard-linked into place (rename after a check where links are unavailable), so it is atomic and never overwrites an existing file; mode 0600 on Linux.
- Desync preset (decided 2026-11-01, stage 1 of 3 of the preset catalogue; see `internal/pc/desync/PRESETS.md`): the default file also writes `desync.preset: alt2-multisplit` (the built-in default made visible; an empty value means the same) with a comment that lists every preset name, explains `custom` (the raw `desync.strategy` for experts) and `auto` (reserved, "not available yet"), and says the presets need the `fake/` folder next to the engine. A test in `internal/pc` keeps the comment and the catalogue in step (every preset id appears; the default id is the one set). The setting is the whole of the desktop API's part in this change: no endpoint, no event, no new key beyond `desync.preset`; the UI picker and the "Automatic" choice are stages 3 and 2.
- The default file keeps every key at its built-in default (`pc.DefaultConfig`) except `checker.lists` and `desync.preset`; `checker.lists` points at the three starter list files below (relative paths, resolved by `pc.LoadConfig` against the folder of `app.yaml`). It loads with `pc.LoadConfig` unmodified and holds no secret and no server address. Servers are added later through the server-management API.
- Starter lists (decided 2026-11-01, so the Sites page is not empty on a fresh install): the files `lists/candidates.txt`, `lists/proxy.txt` and `lists/block.txt` are embedded (`go:embed`, folder `defaultlists/`) and written to `<Paths.Config>/lists/` before `app.yaml`, with the same private temporary file and link-into-place write, so an existing file (a user edit) is never overwritten, even when only `app.yaml` was deleted. `candidates.txt` holds about 140 public domains of services commonly reported as blocked or throttled by DPI (TSPU) in Russia (video, messengers, social networks, music, independent media, AI services, privacy tools, developer services, trackers); the checker probes each and decides direct / desync / tunnel per domain. `proxy.txt` and `block.txt` ship with comments only. The content has no IP, URL or secret (test-enforced).
- Configurable: edit the text files, add files to a list or change `checker.lists` in `app.yaml`. The lists are read once, when the checker is built at the start of `splitter run`; there is no hot reload, so a change applies the next time `splitter` starts (the UI engine restart). A missing listed file is a startup error; an invalid line is a warning.
- An explicit `--config` that is missing is still exit 1 and nothing is created. `run` without `--ui` and `check-config` never create anything (a missing default config stays exit 1).
- Startup then reaches the ready line with no `tunnel.json` (the pre-flight tolerates it in UI mode, decision 21) and without the `sing-box` / `winws2` binaries: they are looked up only at `connect` (and `check-config`). With no `checker.lists` configured the route table starts empty (the Sites page shows "still learning").

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

The only client is the Rust side of the Tauri app (ADR 0003); the web frontend never calls it. Payload shapes follow the Go field names of `status.Snapshot`; the TypeScript mirror is `ui/src/backend/types.ts`. Deviations from the clickable mock the UI started from (history, removed in Stage B, 2026-11-01; see git history of `ui/web` and `ui/mock`) are listed at the end of "Endpoints".

## Command line additions

```
splitter run --ui --token-file PATH [--listen ADDR] [--addr-file PATH] [--error-file PATH] [--config PATH] [--parent-pid PID]
```

- The control API is served only with `--ui`. `--ui` without `--token-file`, and `--token-file`/`--listen`/`--addr-file`/`--error-file` without `--ui`, are usage errors (exit 1). `splitter run` from a terminal has no control API.
- `--listen ADDR`: default `127.0.0.1:0` (the OS picks the port). The host must be a loopback IP literal (`127.0.0.0/8` or `::1`); `localhost`, a host name, `0.0.0.0`, `::`, an empty host or a missing port are rejected with exit 1 before anything else starts. Bind failure (port busy) is exit 1.
- Ready line: after the listener is bound **and** the app is ready (pre-flight passed, checker started, state `disconnected`), `splitter` prints exactly one line to stdout, flushes it, and starts serving:

  ```
  OBHOD-API v1 127.0.0.1:54321
  ```

  Format: `OBHOD-API`, one space, `v1`, one space, `host:port` as in `net.Addr.String()` (`[::1]:54321` for IPv6), then `\n`. The UI matches `^OBHOD-API v1 (\S+)$`. It is the only line on stdout in `--ui` mode (logs go to stderr and the log file). If start fails, no line is printed and the exit code says why. Connections made between bind and the line wait in the listen backlog.
- `--addr-file PATH` (Q3): also write the same `host:port` (no prefix) to PATH, atomically, mode 0600, when the ready line is printed. A Windows `runas` launch cannot capture stdout. A file that already exists at start is deleted before the port is bound (a dead address from a crashed run, decision 10), and the file is removed again when the process exits cleanly (normal end, `shutdown`, signal, `--parent-pid`). Only a crash or kill may leave it behind, so the UI treats a file whose address does not answer as stale.
- `--token-file PATH`: see Auth.
- `--error-file PATH` (decided 2026-11-01): a Windows `runas` launch cannot capture stderr either, so when `run --ui` fails **before** the ready line (usage or config error, needs-administrator, a failed start, a busy port, an unwritable `--addr-file`) it also writes the same one-line stderr message (only its first line, so no usage text) and `exit code: N` to PATH, atomically, owner-only, best effort (a write error is ignored):

  ```
  splitter: config: open C:\ProgramData\splitter\app.yaml: ...
  exit code: 1
  ```

  A file that already exists is deleted at start (before anything can fail), and nothing is written for a clean run or for a failure after the ready line. Only `run --ui` accepts it (otherwise a usage error). The message is the one already printed on stderr: it never holds the token (the token file path may appear, never its content; a message that contains the token is masked as `[hidden]`), and the engine never puts server addresses in startup errors. The UI deletes a stale file before each launch, reads it when the start fails (bounded, sanitized) and shows it under "Details".

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
| `bad_request` | 400, 415 | malformed JSON, unknown field, wrong type, bad `name`, bad domain in `check-now`, a missing `input`, `id` and `input` both or neither, a missing `domain` or `action` in `POST /sites` |
| `forbidden` | 403 | wrong `Host` |
| `not_found` | 404 | unknown path; unknown server id (an id that is not 16 lowercase hex characters is also 404); `DELETE /sites/{domain}` with no rule for that site |
| `unknown_domain` | 404 | `check-now`: none of the domains is in the table |
| `method_not_allowed` | 405 | wrong method (`Allow` is set) |
| `busy` | 409, 429 | 409: `connect` while disconnecting, a server call during a connect or disconnect, deleting the active server while connected; 429: too many event streams |
| `too_large` | 413 | body over 64 KiB; a server link or JSON over 32 KiB |
| `invalid_input` | 422 | server calls: malformed or incomplete link or JSON, forbidden keys, or the sing-box dry run failed; site calls: the domain is not a plain domain name (URL, path, port, IP address, wildcard inside, empty after cleaning) or the action is not `direct`, `desync`, `tunnel` or `block` |
| `unsupported_protocol` | 422 | server calls: a protocol, transport, cipher or option this version cannot use (including Xray-only ones) |
| `duplicate` | 409 | `POST /servers`: same protocol, host, port and credential already stored |
| `limit_reached` | 409 | `POST /servers`: 20 servers stored; `POST /sites`: 500 rules stored (an update of an existing rule still works) |
| `engine_unreachable` | 503 | the app is shutting down (the listener is still up) |
| `internal` | 500 | unexpected; details only in the log |

These codes are not HTTP errors: they appear in `state.error` (and the `state` event) after a failed or interrupted transition.

| Code | Meaning | Source in `internal/pc` |
|---|---|---|
| `permission_denied` | the app lacks the rights to change routing | `procman.ErrNeedsAdmin`, exit 2 without commands |
| `needs_setup` | one-time manual setup needed; `commands` lists them (never run by the app) | Linux `inet splitter` table / `ip rule` check, exit 2 with commands |
| `tunnel_missing` | a detour server is in use but cannot be used: its host name did not resolve (and desync domains need its address) | resolve error, exit 1. **No server in use at all is not an error since 2026-11-01** (degraded mode, decision 34); a tunnel file that exists but is not valid still stops the start of `splitter` (exit 1) |
| `program_missing` | a required helper program (sing-box, or the desync engine when some site needs it) is not next to the app (additive, 2026-11-01). The sentence is fixed ("A required program is missing next to the app. Reinstall the app."), no path; the log line `connect failed` names the program | `errProgramMissing` from `resolveBinaries` / the desync plan, exit 1 |
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
- `snapshot` is `status.Snapshot` with its Go names: `Version` (string), `State` (same as `state`), `Network` (label, never the public IP; `unknown network` until identified), `LastCheck`, `NextCheck` (RFC3339; the zero time `0001-01-01T00:00:00Z` when unknown), `Routes` (`{route: domainCount}`), `Traffic` (`{route: bytes}`), `Savings` (float, 0 to 1), `Processes` (`[{Name, State, Restarts}]`), `Warnings` (`[string]`), `Recent` (`[{Domain, From, To, At}]`, the last 20 route changes), `Tunnel` (`"ready"` when a detour server is in use, `"none"` when there is none; added 2026-11-01, decision 34). Nil slices and maps are encoded as `[]` and `{}`, never `null`. Routes are `direct | desync | tunnel | proxy | block`.
- `networks`: networks seen since the app started (newest last, at most 10), `label` as in `Snapshot.Network`.
- `snapshot.Tunnel == "none"` is the degraded mode (decision 34): the app is (or would be) connected without a detour server, so every site whose route is `tunnel` is served **direct**. The UI shows its banner from this field. While connected it describes the running sing-box config: after `POST /servers/{id}/activate` it stays `"none"` until the reload has restarted sing-box with the server (up to 30 s), then becomes `"ready"`. Disconnected it says whether a server is in use.

**Route** (`GET /routes`): `{ "domain", "route", "source", "since", "lastCheck", "history": [{ "From", "To", "At" }], "degraded": bool }`, sorted by domain. `degraded` (added 2026-11-01) is `true` for an entry whose `route` is `tunnel` while `snapshot.Tunnel` is `"none"`: the entry keeps the route the checker or the user chose, but the site is served direct and is probably still blocked. It is `false` for every other entry. `source` is `route.Source` as a string: `static` (proxy/block list), `checked`, `default`, or `user` (a per-site rule, added 2026-11-01; additive). `since` is when the entry got its current route (the first table of this run for entries that never changed). `lastCheck` is the time of the last finished check cycle (global, not per domain). `history`: at most 10 route changes of this run, oldest first. The list is empty while the table is empty ("still learning").

**Process** (`GET /processes`): `[{ "Name", "State", "Restarts" }]` for `splitter` (always `running`), `sing-box` and `desync` (the `winws2`/`nfqws2` engine). `State` is the procman state (`starting | running | stopping | stopped | failed`); a process that was never started is `stopped`.

**Logs** (`GET /logs?name=`): `{ "name": "sing-box", "lines": ["..."] }`, the last 200 lines. `sing-box` and `desync` come from the procman ring buffers; `splitter` from a ring of the app's own log lines (the same text as the log file). The gateway adds and filters nothing, so the producers' rule applies (full hostnames only when debug is on).

**Version** (`GET /version`): `{ "splitter": "splitter 0.12.0", "singBox": "", "desync": "", "api": "v1" }`. `splitter` is `version.String("splitter")`. `singBox` and `desync` are `""` when unknown (Q5). The UI adds its own version.

**check-now body**: `domains` (optional) is a list of at most 100 names, each normalized with `route.Normalize` (an invalid name is 400). Names not in the table are ignored; if none is in the table: 404 `unknown_domain`. Without `domains` all candidates are checked. The call returns at once; progress is on the event stream.

**connect/disconnect** are asynchronous: the handler starts the operation on the app's run context (not the request's) and returns. The result arrives as `state` events: `connecting`, then `connected`, or back to `disconnected` with `error`. `disconnect` while `connecting` cancels the connect and ends in `disconnected` without an error.

**shutdown**: answers 200, then cancels the run context. Open event streams get a final `shutdown` event and are closed; the listener closes after the response is flushed; `splitter` exits with 0 (or the code `pc.ExitCode` gives). A repeated call before the listener closes answers 200 again.

Deviations from the old clickable mock (history; the mock is removed) that the UI follows:

1. No `trafficToday` in a Route: traffic is counted per route, never per domain (monitor rule). The UI shows per-route totals from `Snapshot.Traffic` (Q6).
2. `lastCheck` of a Route is global, not per domain (Q8).
3. Added: Route `source`. Version has no `ui` (the UI knows its own).
4. `shutdown` really shuts down and closes the streams.
5. The mock-only endpoints (`/mock/*`) do not exist (and the mock itself is gone). The UI's dev/test stand-in is `ui/src/dev/fakeBackend.ts`, which implements the Tauri command interface, not these HTTP endpoints.

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

## First-run regression tests

Five first-run failures on a real Windows machine got past tests that covered the pieces with fakes. These tests run the real first-run path; all of them run in `go test ./...` (no network, no admin). What each one guards:

| Test | Guards |
|---|---|
| `api/desktop` `TestFirstRunEndToEnd` | `run --ui` from empty directories creates `app.yaml` and the starter lists; the engine gets a config whose lists exist, load with the real `lists` package and give >= 100 candidates; a real `pc.New` (real checker, real store) accepts that config. (Missing `app.yaml` at start; Sites empty because the lists were not referenced.) |
| `internal/pc` `TestFirstRunDefaultConfigRoutesAndProbes` | A real `pc.App` on the real default config (the YAML text is read from `defaultconfig.go`, the lists from `defaultlists/`) with the real checker: with the network still unknown the table and `Routes()` already have >= 100 entries and nothing is probed; `Connect` with no detour server succeeds (no `tunnel_missing`, degraded flags set); once the network is known one check probes all candidates, decisions reach `Routes()`, and `CheckNow()` probes again. Only sing-box, the desync engine, the status server and the prober (a counting runner) are fakes. |
| `internal/pc` `TestFirstRunRealNetworkIdentificationProbes` | The same with the real netwatch and the real `netif.Uplink` of the machine: the network becomes known with a `local:` or `gw:` ID (no echo endpoint, so no network access) and the first check probes. Skipped when the machine has no uplink with a default gateway. (No network ID on Windows, so no site was ever probed.) |
| `internal/pc` `TestConnectMissingProgramIsSpecific`, `TestClassifyProgramMissing` | A missing sing-box, or a missing desync engine that a site needs, ends `Connect` with `program_missing` and its fixed sentence (no path in it), sing-box is not started, and the log line `connect failed` names the cause; a missing desync engine is no problem when no site uses it. (Generic `connect_failed` with only a log hint.) |
| `api/desktop` `TestStateErrorGoldens` | The `program_missing` state body (golden). |
| `internal/lib/checker/netwatch` `TestOfflineFactsIdentifyTheNetwork`, `TestRealOSUplinkIsIdentified` | Each set of offline OS facts (Windows: adapter and gateway IP only; Linux: gateway MAC; Wi-Fi: SSID) gives a known network with the right ID kind, and nothing but "truly nothing" gives an unknown one; plus the real OS check above. No build tag: identification makes no network call. |
| `scripts/fetch` `TestAppFilesListMatchesManifest`, `TestBuildScriptAndMakefileUseTheList` | `scripts/app-files.txt` (the single list copied next to `splitter.exe` by `scripts/build-app.ps1`, `make app`) equals the `windows-amd64` files of `third_party/manifest.yaml`, and the script and the Makefile use it. (Helper programs missing from the app folder.) |

Not automatic: that the Windows app folder really starts (UAC, WinDivert, the Wintun adapter) and `scripts/build-app.ps1` itself (a human runs `make app`; `-SkipBuild` assembles and checks only the pinned files). A `app.yaml` from an older version that has no `checker.lists` is still read as is (an empty Sites list); only a missing file is replaced.

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
- Activate while connected (decision 19): the new config is checked first, sing-box restarts once through the reload path (the 30 s restart gap applies: the restart waits for it, the 202 does not), and if it does not start the previous server file, outbound and active mark are put back and `state.error` becomes `connect_failed` ("The new server could not be used. The previous server is still in place."; when the app was connected without a server: "...The app keeps running without a detour server."). Connected in degraded mode, this is also how the app switches to full mode: when the restart is done `snapshot.Tunnel` becomes `"ready"` and `degraded` clears. Activate while disconnected only swaps the file; the dry run still runs.
- Delete of the active server while disconnected is allowed: the `tunnel` file is removed and the next `connect` runs in degraded mode (decision 34). Deleting the active server while connected stays 409 `busy`.
- `AddServer`, `ActivateServer` and `DeleteServer` serialize with `Connect`, `Disconnect` and reload on the same lock (waiting honours the request context); `GET /servers` and `POST /servers/test` do not wait for it.
- Request bodies of `POST /servers` and `POST /servers/test` are never logged. The request log line shows the path with `{id}` in place of the id (also for requests that fail authentication).
- No new event kind: the UI refetches `GET /servers` after any server call.
- `internal/pc` starts in UI mode without a tunnel file (a first run has none): the app is disconnected, and since 2026-11-01 `connect` then succeeds in degraded mode (decisions 21 and 34).

### Gateway testing

`httptest` against the fake engine (which mirrors the `ErrBusy` and active-server rules of `pc`): every endpoint with its codes (200 vs 202, busy, not found, bad id shapes, wrong method with `Allow`), the body rules (64 KiB, Content-Type, unknown fields, name rules), golden JSON in `testdata/` for every success and error body (updated only with `-update`), 401/403 on every server route, and a secret-leak test: a marker in the request body and in an engine that echoes it reaches no response, error body or log line, and the log shows `{id}`. The parsers, the store and the rollback are tested in `internal/pc/servers` and `internal/pc` (see their `API.md`).

### UI stand-in for server management (history)

The server calls were first prototyped in the old `ui/mock` (removed in Stage B, 2026-11-01). They are now covered by `ui/src/dev/fakeBackend.ts`, which checks the link shape and returns the codes above.

## Per-site rules (user-approved 2026-11-01, Go side)

Status: **implemented**; asked for and approved by the human on 2026-11-01 (Go side only; the UI follows as a separate task). Same transport, auth, limits, error format and Host check as above. The logic is not in this module: normalization, validation, the 500-rule limit, the file and the live apply are `internal/pc/sites` and `internal/pc` (contracts: `internal/pc/sites/API.md`, `internal/pc/API.md`, `internal/lib/checker/API.md`). The gateway reaches the rule type through the `pc.SiteRule` alias, so `Uses modules` did not change. The `engine` interface grew `Sites`, `SetSite` and `DeleteSite`.

A **rule** is a domain plus an action. The domain covers its subdomains, like a list entry. Actions: `direct`, `desync`, `tunnel` (always use that path), `block`. "No rule" is the automatic behaviour; it is made by deleting the rule (there is no `none` action; `proxy` is not offered because v1 has no proxy outbound).

**Rule object**: `{ "domain": "example.com", "action": "tunnel", "addedAt": "2026-11-01T10:00:00Z" }`. `domain` is the normalized form (lower case, punycode, no trailing dot); `addedAt` is when the rule was first added (an update keeps it).

| Method | Path | Request | Success | Errors |
|---|---|---|---|---|
| GET | `/sites` | - | 200 `[Rule]`, sorted by domain, `[]` when none | 401 403 405 503 |
| POST | `/sites` | `{ "domain": string, "action": string }` (upsert) | 201 Rule when new, 200 Rule when an existing rule changed | 400 `bad_request` (missing `domain` or `action`, unknown field, wrong type), 413, 415, 422 `invalid_input`, 409 `limit_reached` |
| DELETE | `/sites/{domain}` | - | 200 `{ok:true}` | 404 `not_found` (no rule for that domain), 422 `invalid_input` (the segment is not a domain name) |

- **Input cleaning is the UI's job.** The domain is normalized (`route.Normalize`: trimmed, lower case, trailing dot removed, IDN to punycode) and, like list files, a leading `*.` or `.` is accepted and dropped. A pasted URL, a path, a port, an IP address or a wildcard inside the name is **rejected with 422** (nothing is stripped for the user). The same normalization applies to `DELETE /sites/{domain}` (the segment is percent-decoded by HTTP, so an IDN name works in either form). A path segment with a `/` or an empty one is no route (404).
- **Precedence** (highest first): a user rule for a domain beats a proxy/block list entry and a candidate (decided or default) of the **same** domain. A list entry of a more specific domain (a longer name) still decides for its own names, because the table matches the longest suffix: the rule `example.com direct` does not unblock a listed `ads.example.com`. The starter list files are never written by the app.
- **A rule for a domain in no list** adds it to the route table, so it appears in `GET /routes` with `source: "user"` and a `routes` event (`From` is `""`). A rule for a candidate replaces its route and `source`; the checker stops probing that domain while the rule exists but keeps its stored decisions.
- **Deleting a rule** returns the domain to automatic: a candidate goes back to its stored decision for the current network (or to the safe default, `tunnel`, until checked); a domain of the proxy/block lists goes back to that entry; a domain in no list **leaves the route table** (`routes` event with `To` `""`) and its traffic follows the engine's default, which is the tunnel (the sing-box final route).
- **Live apply**: no restart, also while connected. The rule changes the table at once; the existing reload path regenerates the configs after its 3 s debounce (sing-box restarts at most once per 30 s; a rule to or from `desync` also restarts the desync engine). Disconnected: only the sing-box config file is updated; the next `connect` uses the rules. The response does not wait for the reload. Events: the existing `routes` event (reused, none added). A rule that only changes the `source` of an entry, not its route (for example `tunnel` onto a candidate that was already `tunnel`), produces no `routes` event, so the client refetches `GET /sites` and `GET /routes` after every site call.
- A `desync` rule is subject to the same needs as any desync domain (an IP or resolvable tunnel server, an uplink, the nft setup on Linux); if the plan cannot be built the reload logs it and sing-box still gets its update.
- **Persistence**: `Paths.Data/sites.json`, owner-only, atomic. An unreadable file is moved to `sites.json.corrupt` and the rules start empty (a warning in the log).
- **Privacy**: a site the user named is never logged. The request log shows `/api/v1/sites/{domain}` for the delete path; request bodies are not logged; error sentences are fixed.
- **Not in this feature**: no import or export of rules, no pattern or regex rules, no rule for an IP address, no `proxy` action, no per-rule note, no bulk call.

### Gateway testing (sites)

`httptest` against the fake engine (which mirrors the normalization, the four actions and the 500 limit of `pc`): golden JSON for the empty and filled list and every success and error body, the body rules (missing fields, unknown field, wrong type, 64 KiB, Content-Type), 422 for a URL, port, IP and wildcard, create 201 vs update 200, delete 200/404/422, odd paths (encoded slash, empty name, GET on a rule), 401/403 on every site route, all error kinds with a fixed sentence, and the log test: the site name never appears, the path shows `{domain}`.

## Decisions

Approved by the human on 2026-10-31: every question below is decided as recommended. Changing one later needs the human and an update here.

1. **Keep `internal/pc/status` (page and `status.listen`)?** Decided: keep it for now (milestone 3 only adds the gateway); remove it, its port and `?token=` once the UI ships, as a separate change with `docs/architecture.md` and ADR updates.
2. **Require `--token-file` with `--ui`, and refuse a token file readable by others on Linux?** Decided: yes to both (the API is the only control path in UI mode, and the token the only authentication).
3. **Add `--addr-file`?** A UAC `runas` launch cannot capture stdout, so the ready line may never reach the UI. Decided: yes (the same address, written atomically to a 0600 file in the UI's directory); the UI reads the file or the line, whichever it can.
4. **Hostnames in `/routes`, `Recent` and `routes` events.** Read the monitor rule as: curated-list entries are fine, hostnames observed in traffic are not. Decided: keep as specified; the API then cannot reveal which sites the user actually visited.
5. **`Version.singBox` and `desync`.** Decided: empty strings in v1 (no process is run to ask); fill from `third_party/manifest.yaml` data baked in at build time later.
6. **Per-domain `trafficToday`.** Decided: drop it (it needs per-host accounting, against the monitor rule); the UI shows per-route totals. Say if the Sites page really needs it.
7. **`connect` as 202 plus events** (as the old mock did) instead of a blocking call. Decided: keep 202; a blocking call would hold one request for up to 45 s (`startTimeout`).
8. **Per-domain `lastCheck`** (the old mock had it). Decided: global only in v1.
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
21. **UI mode without a tunnel file.** Decided: `internal/pc` pre-flight no longer fails when the tunnel file is missing in UI mode; the app starts disconnected and `connect` answers `tunnel_missing`. This is an approved behaviour change of `internal/pc` (document it in `internal/pc/API.md`). **Superseded in part by decision 34 (2026-11-01):** `connect` no longer answers `tunnel_missing` when there is no server; it connects without a tunnel.
22. **Limits.** Decided: 20 servers, input 32 KiB, name 60 characters.
23. **Raw JSON hardening.** Decided: `detour` and bind keys in raw JSON are rejected.
24. **Read-only server calls are lock-free.** Decided 2026-10-31: `GET /servers` and `POST /servers/test` do not take the Connect/Disconnect/reload lock, so a list refresh never waits behind a connect of up to 45 s. Add, activate and delete do take it.
25. **Oversized input.** Decided 2026-10-31: a link or JSON over 32 KiB (body still under the 64 KiB cap) answers 413 `too_large`.
26. **Dry run without a sing-box binary.** Decided 2026-10-31: the dry run then checks only that the config renders and skips `sing-box check` (debug-logged), so a first run before the binary is installed is not blocked.

## Per-site rule decisions

User-approved on 2026-11-01 (Go side). Changing one later needs the human and an update here.

27. **Where rules live.** Decided: `internal/pc/sites` stores them (a new child of `internal/pc`), `internal/pc` hands them to the checker, `api/desktop` only maps HTTP. The route table is the checker's, so the checker got one method (`SetOverrides`) and `route` one additive source (`user`); no list file is written.
28. **Precedence.** Decided: user rule > block > proxy > candidate for the same domain; a longer (more specific) list entry still decides for its own names (longest suffix). Alternative not taken: let a rule also override more specific list entries below it (it would silently unblock listed subdomains).
29. **Actions.** Decided: `direct`, `desync`, `tunnel`, `block`; removal is `DELETE`; no `proxy` (no proxy outbound in v1).
30. **Input.** Decided: the gateway rejects pasted URLs, paths, ports, IPs and inner wildcards with 422 (the UI cleans input); a leading `*.` or `.` is accepted as in list files.
31. **Limit.** Decided: 500 rules (409 `limit_reached`); updating an existing rule at the limit works.
32. **Status codes.** Decided: `POST /sites` answers 201 for a new rule and 200 for a changed one; `DELETE` of a name that is not a domain is 422, of a valid name without a rule 404.
33. **Events.** Decided: reuse `routes`; none added. A source-only change has no event, so the client refetches after each call.

## Degraded mode decisions

User-approved on 2026-11-01 (Go side; engine decision for docs/UI.md open question 3).

34. **Connect without a detour server.** Decided: when no tunnel outbound is in use (no tunnel file: first run, no server activated, or the active server deleted while disconnected), `connect` succeeds in a degraded mode (the "Everyday + Picky only" mode): sing-box runs with **no** `tunnel` outbound and its final route is `direct`; every domain whose route is `tunnel` (checker default for unknown and blocked sites, user `tunnel` rules, list entries) is sent to the `direct` outbound, never anywhere else; `block` still blocks; `desync` works as usual (the desync engine is told a documentation address, `192.0.2.1/32`, as its exclusion because there is no tunnel server to protect). The rest is unchanged: a server that is in use but unusable (host name does not resolve while desync domains exist) is still `tunnel_missing` and any other failure is still `connect_failed`.
35. **What the API says in this mode.** Decided: additive `snapshot.Tunnel` (`"ready"` | `"none"`) and `degraded` on each tunnel-route entry of `GET /routes`. The route table is not rewritten and the checker is unchanged: it never probes through a tunnel, so a "Blocked" site keeps route `tunnel` (its source stays `checked`/`default`) and is only flagged `degraded: true`. Traffic is counted by the outbound that carried it, so these sites show up under `direct` in `snapshot.Traffic`. Alternative not taken: rewriting the route to `direct` (it would hide that the site needs a detour and would change `routes` events when a server is added).
36. **Leaving degraded mode.** Decided: `POST /servers/{id}/activate` while connected without a server uses the existing reload path (check, one restart after the 30 s gap, rollback to the degraded config if sing-box does not start). Deleting the last/active server while connected stays 409 `busy`; disconnected it is allowed.

## Known limits (v1, fixed 2026-10-31)

Deliberate limits of milestone 4. Lifting one is a new decision.

- **Hostname servers while connected.** On activation the resolved addresses are forgotten and re-resolved at the next connect. Until then a desync plan is refused and the engine keeps its state. An IP-literal server updates the exclude list at once. Prefer an IP in the server.
- **Hysteria2 `pinSHA256`** is rejected as `invalid_input` (it cannot be applied).
- **Not supported:** Xray-only transports (xhttp, splithttp, mKCP) and the quic transport (`unsupported_protocol`); subscription URLs; the optional `proxy` outbound; groups, failover and auto-select; a real reachability or latency test (`test` is offline).
- **Numbers:** 20 servers, input 32 KiB, name 60 characters, body 64 KiB, 4 event streams.
- **Not verified when this was written:** the Linux file-mode (0600/0700) test and Linux nft/ip-rule paths with a server change (never run on Linux), `-race` (no cgo on the dev machine), `sing-box check` with a Linux binary, an end-to-end `splitter --ui` run with a real UI, and Windows ACL inheritance of `Paths.Secrets/servers`.
- **One unreproduced test failure.** On 2026-10-31 `go test ./internal/pc/` failed once (12 s run, while many packages were tested in parallel) and passed in 19 later runs (12 repeated, 3 parallel, 4 alone). The failing test was not captured. Suspect: a timing-dependent test in `internal/pc` (fake-clock or event tests). If it shows up again, run with `-count` under load and record the test name here. Resolved 2026-10-31: reproduced by running the compiled `internal/pc` test binary in 20 parallel processes (`-test.count=3 -test.cpu 1,2,8`; 2 of 20 failed). Two harness races, no non-test code was wrong: (1) `TestCheckEventsForScheduledCheckAndGrace` (the 12 s failure, `no [check] event`) advanced `deps.evClk` before `watch` had created its tickers, so the first poll tick was lost; (2) `TestTunnelOnlyChangeRestartsOnlySingBox` cleared the op log after `status.start` but the monitor goroutine started by `Connect` logged `monitor.start` into the window. Fix: every test now waits for `harness.waitRunning` (status started, both event tickers armed, monitor started when not in UI mode) instead of `waitOp("status.start")`. Afterwards 270 loaded `internal/pc` runs, 75 `api/desktop` runs, 50 `internal/pc/servers` runs and 30 `GOMAXPROCS=1` runs passed.
