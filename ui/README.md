# Obhod desktop UI: clickable MVP with a mock engine

This folder is a clickable prototype of the desktop UI planned in ADR 0003 (`docs/adr/0003-desktop-ui-and-packaging.md`) and specified in `docs/UI.md`. It is not the Tauri app yet. All buttons talk to a local **mock** server that pretends to be the engine (`splitter`) and its implemented control API (`api/desktop/API.md`: M12 and the server-management section, milestone 4). Nothing real happens: no routing, no elevation, no network changes.

## Run

```sh
node ui/mock/server.js
```

Then open the URL it prints (default `http://127.0.0.1:8787/`). Set `PORT` to use another port (`PORT=9000 node ui/mock/server.js`; in PowerShell `$env:PORT=9000; node ui/mock/server.js`). Needs Node 18 or newer. There are no npm dependencies, no build step and no external URLs or fonts.

Press **Ctrl+Shift+D** in the UI for the mock controls (scenario, warnings, proxy/blocked-by-you sites, "still learning", "switching server while On fails", change network, reset data).

## Layout

| Path | What |
|---|---|
| `mock/server.js` | `node:http` server, binds 127.0.0.1 only. Serves `web/` and the mock `/api/v1` |
| `web/index.html`, `web/style.css` | shell, navigation, colour tokens (light and dark) |
| `web/api.js` | the only module that calls the engine (fetch + EventSource); to be swapped for Tauri `invoke` calls |
| `web/store.js` | state mirrored from the engine, live events, actions |
| `web/shell.js`, `web/dom.js`, `web/words.js` | navigation, dialogs, log viewer; DOM helpers (textContent only); user words, error sentences and the detour-server copy |
| `web/serverui.js` | the Add server dialog (paste, optional name, Test, Add) and the Remove confirmation; used by Settings and the Home first-run card |
| `web/app.js` | router, keyboard shortcuts, dev panel |
| `web/pages/*.js` | Home, Sites, Activity, Settings, Help |
| `design/` | Design kit for the production app: tokens (JSON, CSS, TS), user copy, icons, component spec (`design/README.md`) |

## What the mock simulates

The mock follows the real contract (`api/desktop/API.md`): payload shapes, field names, status codes and error bodies. Errors are `{ "error", "code" }` (plus `commands` for `needs_setup`). A body is optional on POSTs; if present it must be JSON (`Content-Type: application/json`, else 415), an object, with no unknown fields (400), at most 64 KiB (413 `too_large`).

| Endpoint | Behaviour |
|---|---|
| `GET /api/v1/state` | `{ state, error, checking, snapshot, networks }`; `snapshot` has the Go field names (`Version`, `State`, `Network`, `LastCheck`, `NextCheck`, `Routes`, `Traffic`, `Savings`, `Processes`, `Warnings`, `Recent` (last 20)). While "still learning": `Network` is `unknown network`, the times are the zero time, the table is empty |
| `GET /api/v1/routes` | about 40 fake domains: `{ domain, route, source, since, lastCheck, history }`. `source` is `checked` or `static` (the proxy and block lists); `lastCheck` is global; no per-site traffic (monitor rule) |
| `GET /api/v1/processes` | splitter, sing-box, desync with state and restarts |
| `GET /api/v1/logs?name=splitter\|sing-box\|desync` | fake log lines `{ name, lines }` (last 200); they never contain a pasted link or a server address |
| `GET /api/v1/version` | `{ splitter, singBox, desync, api }`; `singBox` and `desync` are `""`. The UI keeps its own version in `api.js` |
| `POST /api/v1/connect` | 202; disconnected → connecting → (1.5 s) → connected, or back to disconnected with an error (see scenarios). Without an active server: `tunnel_missing`. 409 `busy` while disconnecting |
| `POST /api/v1/disconnect` | 202; connected → disconnecting → (0.8 s) → disconnected |
| `POST /api/v1/check-now` | optional `{ "domains": [...] }` (at most 100, normalized; bad name 400, none known 404 `unknown_domain`); 202, after 2 s moves 1–2 random sites to another route |
| `POST /api/v1/shutdown` | 200, then a final `shutdown` event, the streams close and every call is 503 `engine_unreachable` for 2 s (the mock "restarts" itself so reloading the page works) |
| `GET /api/v1/events` | Server-Sent Events, at most 4 streams (the 5th is 429 `busy`): `state` first and on every transition, `routes` (`{ changes }`), `check` (`{ running, domains, changed }`), `traffic` every 2 s while connected (`uploadTotal`, `downloadTotal`, `connections`, `delta`, `Traffic`, `Savings`, `At`), `shutdown` |

Server management (milestone 4). The mock keeps only `name`, `protocol`, `host` and `port`; the pasted text is checked and thrown away, never logged, never returned. Only fake data is used (`example.com`, documentation addresses). Error sentences are the engine's fixed ones; none quotes the input.

| Endpoint | Behaviour |
|---|---|
| `GET /api/v1/servers` | `[Server]`, oldest first: `{ id, name, protocol, engine: "singbox", host, port, active, addedAt }`. The normal scenario starts with two fake servers, one active |
| `POST /api/v1/servers` | `{ input, name? }` → 201 Server, never activated (not even the first). `input` is a link (`vless`, `vmess`, `trojan`, `ss`, `hy2`/`hysteria2`, `tuic`) or one JSON object with a `type`. Checked: scheme and structure (user ID, port, address, Reality key and name, ss cipher), `detour`/bind keys rejected at any depth, JSON shape limits. Errors: 400 `bad_request` (empty input, bad name, unknown field), 413 `too_large` (over 32 KiB), 422 `invalid_input`, 422 `unsupported_protocol` (xhttp, splithttp, mKCP, quic, unknown scheme or type, unknown cipher, plugin or flow), 409 `duplicate`, 409 `limit_reached` (20), 409 `busy` while connecting or disconnecting. Name: 1–60 printable characters; default from the link's fragment, else `Server N` |
| `POST /api/v1/servers/{id}/activate` | 200 `{ ok, active, reloaded: false }` while disconnected or already active; while connected 202 `{ ok, active, reloaded: true }` and a short `connecting` → `connected` flicker (1.2 s) as `state` events. 404 `not_found` (also for an id that is not 16 lowercase hex characters), 409 `busy`. With the mock switch "Switching server while On fails" the old server stays and `state.error` becomes `connect_failed` while `state` stays `connected` |
| `DELETE /api/v1/servers/{id}` | 200 `{ ok }`; 409 `busy` for the active server while connected (or any server while connecting or disconnecting); 404. Deleting the active server while disconnected leaves none, so the next Start fails with `tunnel_missing` |
| `POST /api/v1/servers/test` | `{ id }` or `{ input }` (exactly one, else 400) → 200 `{ ok, protocol, code }`, `code` is `""`, `invalid_input` or `unsupported_protocol`; offline, adds nothing. 404 unknown id, 413 over 32 KiB |

To try the Add dialog, paste this fake link (it only passes the mock's shape check; nothing connects):

```
vless://00000000-0000-4000-8000-000000000001@example.com:443?security=tls&sni=example.com&type=ws#Demo
```

Mock-only controls:

- `GET|POST /api/v1/mock/scenario` with `{ scenario }` (or `?scenario=`), also `warnings`, `extras`, `learning`, `reloadFails` (booleans), `network: "next"`, `reset: true` (re-seeds the sites and the two servers; the list stays empty in `no-server`). `?scenario=<name>` on any request switches too.
- `POST /api/v1/mock/restart-engine` stands in for the UI restarting the `splitter` sidecar (also returns to `normal`).

Scenarios:

| Scenario | What happens |
|---|---|
| `normal` | everything works; leaving `no-server` brings the two fake servers back |
| `permission-denied` | Start fails back to disconnected with `permission_denied` ("Permission was not given.") |
| `needs-setup` | Start fails with `needs_setup` and the Linux nft commands; the UI shows them in a dialog with **Copy** (example commands, never run) |
| `crash` | connects, then drops to disconnected after a few seconds with `crashed` |
| `no-engine` | every API call answers 503 `engine_unreachable`; the UI shows "The engine is not running." and **Restart engine** |
| `no-server` | the server list is emptied (first run). Home shows "Add a detour server to start"; Start fails with `tunnel_missing` and the UI offers Settings → Detour server. Add a server and make it active to get a working Start (a new server is never activated by itself) |

No real IP addresses appear anywhere; the network is a label such as "Home Wi-Fi (Example ISP)" and servers use `example.com` or documentation addresses. Settings are stored in the browser's `localStorage` only (including "Skip for now" on Home).

## Notes

- Settings → Detour server (list, address reveal, Add with Test, Make active, Remove with confirmation, Ready/Missing) and the first-run card on Home are built. "Skip for now" only hides the card: the real engine cannot start without a server (`tunnel_missing`), so Start then fails honestly.
- `ui/` is outside the Go module rules in `AGENTS.md`: it holds no Go code, and `arch_test.go` only walks `internal/` and `lab/`.
- In the real app the web frontend calls Tauri commands only; the Rust side is the only API client and holds the token (ADR 0003). The mock has no token.
- Not built here: tray menu, the Welcome and Permission first-run screens, OS notifications, diagnostics export, the extra `proxy` outbound, subscription URLs.
