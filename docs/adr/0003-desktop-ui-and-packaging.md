# ADR 0003: Desktop UI (Tauri) and packaging

Status: accepted (2026-10-01); open points listed at the end

## Context

The desktop product needs a GUI, and the user wants "one app": one download, one icon. The engine (`splitter`, `internal/pc`) needs admin/root for the TUN adapter and for WinDivert/nfqws2. sing-box and winws2/nfqws2 (zapret2) are external programs (`docs/HUMAN-third-party.md`), so the product is several processes whatever the UI technology is.

## Decision

1. **The UI is a separate Tauri app**, and the engine stays the Go binary `splitter`. They are two processes that talk over a local control API.
2. **One package for the user.** The Tauri bundler produces one installer per platform (Windows: NSIS `.exe` or `.msi`; Linux: `.deb` / `.rpm`, optionally `.AppImage`). It installs one folder: the UI executable, `splitter`, `sing-box`, `winws2` + `WinDivert.dll` + `WinDivert64.sys` + `cygwin1.dll` (Windows) or `nfqws2` (Linux), plus zapret2's `lua/` folder. They are declared as Tauri sidecars (`bundle.externalBin`). The user only ever starts the UI; the UI starts `splitter` hidden (no console window).
3. **Privileges.** The UI always runs as a normal user and never elevated.
   - v1: the UI starts `splitter` elevated on Connect (Windows: UAC prompt via `ShellExecute` `runas`; Linux: `pkexec`).
   - Later: the installer (already elevated) registers `splitter` as a Windows service / systemd unit, and the UI only connects to it. No prompt per connect, and the connection survives closing the window.
4. **Control API.** `splitter` serves a versioned JSON API (`/api/v1/...`) on loopback only, plus an event stream for live updates. Every request needs a token. The **Rust side of the Tauri app is the only client**: the web frontend calls Tauri commands, never the API directly. So there is no CORS, and the token never reaches JavaScript.
5. **Lifecycle.** `splitter` has an explicit *disconnected* state: the checker keeps running and the status is served, but sing-box and the desync engine are stopped. The UI's Connect/Disconnect switches between the states. `splitter run` from a terminal connects immediately (as before). When started by the UI, `splitter` starts disconnected and exits when the UI process exits (v1).
6. The existing Go rules (`AGENTS.md`, `arch_test.go`) do not apply to the UI code. The UI lives in its own tree with its own rules (see Open).

## Alternatives considered

| Alternative | Why not |
|---|---|
| One process: Go engine built as a library (cgo `c-shared`/`c-archive`) inside the Tauri/Rust app | the whole UI and web view would run as admin; cgo and cross-compiling are painful; an engine crash kills the UI; sing-box/winws are still separate processes |
| Wails (Go + web view) instead of Tauri | possible and needs no Rust, but the UI would run as admin (same process as the engine); the user chose Tauri |
| sing-box linked as a Go library into `splitter` | removes one process, but sing-box is GPL, which would make `splitter` GPL; can be revisited separately |
| Literal single file (a self-extracting exe that unpacks the binaries at runtime) | antivirus heuristics often flag executables that unpack and load a driver (WinDivert); the files must exist on disk anyway. A portable zip of the install folder is fine as a secondary format |

## Consequences

- `internal/pc` gains a control API (extending or replacing the read-only status page) and connect/disconnect states. Planned in `internal/pc/PLAN.md` (M10, M12; G15–G18).
- The binary lookup default (D1 in `docs/HUMAN-third-party.md`) becomes "next to `splitter`", which matches the Tauri install folder.
- Packaging (stage C in `docs/HUMAN-third-party.md`) is done by the Tauri bundler. Sidecars must be named with the Rust target triple (e.g. `splitter-x86_64-pc-windows-msvc.exe`), so the release build renames them.
- Licenses of the bundled binaries must be handled before the first public release (D4 in `docs/HUMAN-third-party.md`).

## Open

- Where the UI code lives: `ui/` in this repo (needs an `AGENTS.md` note that `ui/` is outside the Go module rules) or a separate repo.
- Whether the control API is a new child (`internal/pc/control`) or an extension of `internal/pc/status` (an API change: ask first).
- When to switch from "UAC prompt per connect" to a service.
