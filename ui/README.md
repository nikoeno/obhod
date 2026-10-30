# Obhod desktop UI: clickable MVP with a mock engine

This folder is a clickable prototype of the desktop UI planned in ADR 0003 (`docs/adr/0003-desktop-ui-and-packaging.md`) and specified in `docs/UI.md`. It is not the Tauri app yet. All buttons talk to a local **mock** server that pretends to be the engine (`splitter`) and its planned M12 control API (`internal/pc/PLAN.md`). Nothing real happens: no routing, no elevation, no network changes.

## Run

```sh
node ui/mock/server.js
```

Then open the URL it prints (default `http://127.0.0.1:8787/`). Set `PORT` to use another port (`PORT=9000 node ui/mock/server.js`; in PowerShell `$env:PORT=9000; node ui/mock/server.js`). Needs Node 18 or newer. There are no npm dependencies, no build step and no external URLs or fonts.

Press **Ctrl+Shift+D** in the UI for the mock controls (scenario, warnings, proxy/blocked-by-you sites, "still learning", change network, reset data).

## Layout

| Path | What |
|---|---|
| `mock/server.js` | `node:http` server, binds 127.0.0.1 only. Serves `web/` and the mock `/api/v1` |
| `web/index.html`, `web/style.css` | shell, navigation, colour tokens (light and dark) |
| `web/api.js` | the only module that calls the engine (fetch + EventSource); to be swapped for Tauri `invoke` calls |
| `web/store.js` | state mirrored from the engine, live events, actions |
| `web/shell.js`, `web/dom.js`, `web/words.js` | navigation, dialogs, log viewer; DOM helpers (textContent only); user words and error sentences |
| `web/app.js` | router, keyboard shortcuts, dev panel |
| `web/pages/*.js` | Home, Sites, Activity, Settings, Help |
| `design/` | Design kit for the production app: tokens (JSON, CSS, TS), user copy, icons, component spec (`design/README.md`) |

## What the mock simulates

Endpoints (JSON, errors as `{ "error", "code" }`):

| Endpoint | Behaviour |
|---|---|
| `GET /api/v1/state` | `{ state, error, checking, snapshot, networks }`; `snapshot` has the status `Snapshot` shape with Go field names (`State`, `Network`, `LastCheck`, `NextCheck`, `Routes`, `Traffic`, `Savings`, `Processes`, `Warnings`, `Recent`, `Version`) |
| `GET /api/v1/routes` | about 40 fake domains: `{ domain, route, since, trafficToday, lastCheck, history }` |
| `GET /api/v1/processes` | splitter, sing-box, desync with state and restarts |
| `GET /api/v1/logs?name=splitter\|sing-box\|desync` | fake log lines `{ name, lines }` |
| `GET /api/v1/version` | UI and engine versions |
| `POST /api/v1/connect` | disconnected → connecting → (1.5 s) → connected |
| `POST /api/v1/disconnect` | connected → disconnecting → (0.8 s) → disconnected |
| `POST /api/v1/check-now` | optional `{ "domains": [...] }`; after 2 s moves 1–2 random sites (or some of the given ones) to another route and adds them to `Recent` |
| `POST /api/v1/shutdown` | acknowledges only |
| `GET /api/v1/events` | Server-Sent Events: `state` (on every transition, full state payload), `routes` (`{ changes: [...] }`), `check` (`{ running }`), `traffic` every 2 s while connected (`uploadTotal`, `downloadTotal`, `connections`, per-route `delta`) |

Mock-only controls:

- `GET|POST /api/v1/mock/scenario` with `{ scenario }` (or `?scenario=`), also `warnings`, `extras`, `learning` (booleans), `network: "next"`, `reset: true`. `?scenario=<name>` on any request switches too.
- `POST /api/v1/mock/restart-engine` stands in for the UI restarting the `splitter` sidecar (also returns to `normal`).

Scenarios:

| Scenario | What happens |
|---|---|
| `normal` | everything works |
| `permission-denied` | Start fails back to disconnected with `permission_denied` ("Permission was not given.") |
| `needs-setup` | Start fails with `needs_setup` and the Linux nft commands; the UI shows them in a dialog with **Copy** (example commands, never run) |
| `crash` | connects, then drops to disconnected after a few seconds with `crashed` |
| `no-engine` | every API call answers 503 `engine_unreachable`; the UI shows "The engine is not running." and **Restart engine** |

No real IP addresses appear anywhere; the network is a label such as "Home Wi-Fi (Example ISP)". Settings are stored in the browser's `localStorage` only.

## Notes

- `ui/` is outside the Go module rules in `AGENTS.md`: it holds no Go code, and `arch_test.go` only walks `internal/` and `lab/`.
- In the real app the web frontend calls Tauri commands only; the Rust side is the only API client and holds the token (ADR 0003). The mock has no token.
- Not built here: tray menu, first-run screens, OS notifications, real file import, diagnostics export.
