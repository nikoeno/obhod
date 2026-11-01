# Obhod desktop UI (Tauri v2 + Vue 3 + TypeScript)

The production desktop UI of ADR 0003 (`docs/adr/0003-desktop-ui-and-packaging.md`), specified in `docs/UI.md`. Rules for working here: `ui/AGENTS.md`. Engine contract: `api/desktop/API.md`.

Status (Stage B, 2026-11-01): all five pages are built: **Home**, **Sites**, **Activity**, **Settings** (including the Detour server flow) and **Help**, plus the app shell, the connect state machine and error mapping. The old clickable prototype (`ui/web/`, `ui/mock/`) is deleted; it lives in git history only. `ui/design/` stays as the visual and copy contract.

Per-site rules (2026-11-01): Sites has **Add a site** and, in the side panel, **Always use…** and **Remove my rule** (Rust commands `sites_list`, `site_set`, `site_delete`; engine endpoints `/sites`). The fake backend validates and limits like the engine (500 rules); in `npm run dev` a rule can be added, changed and removed on every scenario.

## How it fits together

```
Vue 3 frontend (webview)  --invoke / events-->  Rust (src-tauri)  --HTTP + bearer token, loopback-->  splitter run --ui
   no token, no fetch                            only API client, owns the child                        (elevated, started by Rust)
```

- The Rust side creates a random token, writes it to a user-only file in the per-user app data folder, starts `splitter run --ui --token-file P --addr-file P2 --error-file P3 --parent-pid <ui pid>` (Windows: `ShellExecute runas`; Linux: `pkexec`), reads the address from the ready line or the address file, and talks to the control API. It forwards the event stream to the webview as the Tauri event `engine-event` (`{ kind, data }`). The token never reaches JavaScript.
- The frontend calls typed commands through `src/backend/` (`Backend` interface). `src/backend/tauri.ts` is the production implementation; `src/dev/fakeBackend.ts` is an in-memory stand-in for `npm run dev` in a plain browser and for tests (there is no separate mock server any more).
- The engine is started when the app opens (one UAC / pkexec prompt, then it runs for the session and exits with the UI). If permission is refused, Home shows "Permission was not given." with **Try again**.

## Prerequisites

- Node 22 or newer (developed with 24) and npm.
- Rust stable with the MSVC toolchain on Windows (Visual Studio Build Tools, "Desktop development with C++") and WebView2 (preinstalled on Windows 11). On Linux the Tauri system packages (webkit2gtk 4.1, etc.): see https://v2.tauri.app/start/prerequisites/.

## Commands (run in `ui/`)

| Command | What |
|---|---|
| `npm ci` | reproducible install from `package-lock.json` |
| `npm run dev` | Vite dev server on http://127.0.0.1:1420 in a plain browser, with the **fake backend** (no Rust, no engine, no other server to start). `Ctrl+Shift+D` opens the scenario panel: scenarios (normal, no-server, permission-denied, needs-setup, crash, engine-unreachable) and data switches (warnings, proxy and blocked-by-you sites, still learning, a failing server switch). Every page reads from it, so each one can be checked in every scenario |
| `npm run typecheck` | `vue-tsc --noEmit` |
| `npm test` | vitest (store, error mapping, view models of Home / Sites / Activity / Help, per-site rules (input cleaning, errors, dialog model), server dialog logic, mocked invoke layer, source rules, icon sync) |
| `npm run build` | typecheck + production web build into `dist/` (no fake backend inside) |
| `npm run tauri:dev` | the real app in dev mode (starts the engine elevated; see below) |
| `npm run tauri:build` | release build and bundle (not part of Stage A checks) |
| `cargo test --locked` (in `src-tauri/`) | Rust tests: fake HTTP server, fake `Launcher`, SSE, error mapping, secrets |
| `cargo clippy --all-targets` (in `src-tauri/`) | lints |

`cargo build`/`tauri dev` embed `dist/`, so on a fresh clone run `npm ci && npm run build` once before building in `src-tauri/` (`tauri dev` and `tauri build` do it for you through `beforeDevCommand`/`beforeBuildCommand`).

Running the real app needs a `splitter` binary: in a debug build the UI looks for `splitter` next to its own executable, or at the path in the `OBHOD_SPLITTER` environment variable (debug builds only; a release build never reads the environment). The engine needs administrator rights, so the real launch triggers a UAC prompt. Agents never run it; the human does:

```powershell
# PowerShell, from the repo root: build the engine once, then start the UI (it will ask for permission)
go build -o bin\splitter.exe .\cmd\splitter
$env:OBHOD_SPLITTER = "$PWD\bin\splitter.exe"; npm --prefix ui run tauri:dev
```

## Layout

| Path | What |
|---|---|
| `design/` | **Kept.** Design kit: tokens (JSON, CSS, TS), user copy, icons, component spec. Imported by the app (`tokens.css`, `copy.ts`, `tokens.ts`) |
| `src/main.ts`, `src/App.vue` | bootstrap, shell (nav, status line, dialogs, keyboard shortcuts) |
| `src/backend/` | `types.ts` (the command/event contract), `tauri.ts` (invoke + listen), `index.ts` (picks the backend; fake only in dev) |
| `src/store.ts` | state mirrored from the engine, actions, event handling |
| `src/lib/` | pure logic: formatting, error mapping, server rules and text, view models (`homeModel`, `sitesModel`, `activityModel`, `helpModel`), Add-server model, icons data |
| `src/pages/` | `Home.vue`, `Sites.vue`, `Activity.vue`, `Settings.vue`, `Help.vue` |
| `src/components/` | dialogs (including `SiteRuleDialog`: Add a site / Always use…), tabs, toasts, icon, log viewer, process list |
| `src/dev/` | **dev/test only**: `fakeBackend.ts`, `DevPanel.vue` (tree-shaken from production builds) |
| `src/styles/app.css` | component CSS on top of `design/tokens.css` |
| `src-tauri/src/` | `token`, `http` (loopback HTTP client), `sse` (parser + reconnecting reader), `client` (typed endpoints, error mapping), `launcher` (`Launcher` trait, ShellExecute/pkexec), `engine` (start, ready, stop), `commands` (Tauri commands: engine, `api_*`, `servers_*`, `sites_list` / `site_set` / `site_delete`) |
| `src-tauri/capabilities/default.json` | the webview may only listen to events |

## Not done yet

Tray icon and menu; OS notifications; diagnostics export (no UI designed, `docs/UI.md` section 12); Open logs folder; first-run Welcome and Permission screens; moving General/Advanced settings to the Rust side; sidecar packaging (`bundle.externalBin`, ADR 0003).
