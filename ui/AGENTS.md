# AGENTS.md (ui/)

Rules for the Tauri desktop app in `ui/` (ADR 0003: Tauri v2, Rust in `src-tauri/`, Vue 3 + TypeScript + Vite in `src/`). This tree is outside the Go module rules of the root `AGENTS.md` (no `API.md`, no `arch_test.go`), but every Hard rule of that file still applies. Product spec: `docs/UI.md`. Engine contract: `api/desktop/API.md`. Visual and copy contract: `ui/design/` (kept, imported by the app).

## Architecture

- The **Rust side is the only API client.** It owns the `splitter` child, the token, the HTTP calls and the event stream. The webview never talks to the engine.
- The **token never reaches JavaScript**: not as a command argument or result, not in an event, not in an error, not in a log. The Rust side keeps it in memory and in a user-only file passed to `splitter --token-file`.
- JavaScript uses **Tauri commands and events only** (`ui/src/backend/`). No `fetch`/`XMLHttpRequest`/`EventSource`/`WebSocket` to the engine or to anything else. The Tauri capability file grants the webview nothing beyond event listening.
- Dev/test without an engine goes through `src/dev/fakeBackend.ts`, which implements the same `Backend` interface in memory. It is dev/test only and must be tree-shaken from production builds (`import.meta.env.DEV` guard; check `dist/` after a build).
- The UI mirrors the engine's `state`; it never invents connection states.

## Security and privacy

- No `innerHTML`, `v-html`, `outerHTML`, `insertAdjacentHTML`, `document.write` or `eval` with any data. Vue text bindings (`{{ }}`, `:attr`) only.
- No external URLs, fonts, CDNs, images or scripts. Everything ships in the bundle. The CSP in `tauri.conf.json` stays `default-src 'self'`-based.
- No telemetry, no automatic update checks, no crash upload. Logs stay local.
- Never log or display a pasted server link/JSON, a token, or a public IP. Server addresses are hidden behind a reveal.
- Never run elevated in dev or tests. The Rust launchers (`ShellExecute runas`, `pkexec`) are only compiled in tests; tests use a fake `Launcher`. Never start `splitter run --ui` for real from an agent; print the command for the human.
- The Rust client only connects to loopback addresses and refuses anything else, so a tampered address file cannot receive the token.

## Dependencies

- Minimal. No UI kit, no router library, no state library beyond Vue's own reactivity.
- Pin **exact** versions (no `^`/`~` in `package.json`; `=x.y.z` in `Cargo.toml`) and commit both lockfiles (`package-lock.json`, `src-tauri/Cargo.lock`). Install with `npm ci`; build Rust with `--locked`.
- A new dependency needs the human's approval (same rule as Go). List direct dependencies with versions in the report.

## Tests (required)

- Frontend: `npm test` (vitest) for the store, error mapping and dialog logic, with the fake backend or a mocked `invoke`. Also `npm run typecheck` and `npm run build`.
- Rust: `cargo test --locked` in `src-tauri/` against an in-process fake HTTP server speaking the contract (golden JSON from `api/desktop/testdata/` where useful), a fake `Launcher`, and tests that secrets never appear in logs or errors. `cargo clippy` if available.
- A change is done when typecheck, vitest, `npm run build`, `cargo test` and `cargo build` pass. Report what could not be verified (the real elevated flow, the Linux launcher).

## Layout

| Path | What |
|---|---|
| `design/` | tokens, copy, icons (source of truth, do not fork values) |
| `src/` | Vue app: `backend/` (command interface), `store.ts`, `pages/` (Home, Sites, Activity, Settings, Help), `lib/` (pure view models, tested), `components/`, `styles/app.css`, `dev/` (dev only) |
| `src-tauri/` | Rust: `token`, `http`, `sse`, `client`, `launcher`, `engine`, `commands` |

Do not edit Go code or `api/`/`internal/` from here. If the contract needs a change, stop and ask.
