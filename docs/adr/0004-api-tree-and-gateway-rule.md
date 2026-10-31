# ADR 0004: The `api/` tree and the gateway rule

Status: accepted (2026-10-30)

## Context

The desktop UI (ADR 0003) talks to the engine over a local HTTP API. That API must see many modules: status, checker, analyzer, later server management. Two placements do not work under ADR 0002:

- A child of `internal/pc` can reach only its parent's wiring. Reaching other modules would need an interface injected by `pc` for each of them, and `pc` would grow into the API's data layer.
- A module above `pc` (so it can see everything) would have to import `pc`, which is a parent/child inversion and, once `pc` wires it, an import cycle.

## Decision

1. A new top-level tree `api/`, sibling of `internal/`, `lab/`, `cmd/` and `docs/`. It holds the HTTP API apps. `api/` is a plain folder like `lib/`: no Go files, no `API.md`.
2. Each `api/<name>` is a module (a **gateway**) with `Kind: app` and a `Binary`, and gets its entry point in `cmd/<binary>` by rule (c).
3. Layering: UI (Tauri) → `api/<name>` → modules (`internal/pc`, `internal/lib/...` and their children) → submodules.
4. Import rule (f), enforced by `arch_test.go`: a module under `api/` may import the public package of any module under `internal/`, children and submodules included, except its ancestors, `Kind: test-only` modules, other modules under `api/` and `cmd/`. Each such import is declared in the `Uses modules` field of its `API.md`; unlisted imports and unused entries both fail the test. It needs no `Consumers` entry on the imported module. Its own children and libs follow (a)/(b) and `Children` / `Uses libs` as usual. Nothing outside `api/` imports a gateway except `cmd/<binary>`.

## Consequences

- **Blast radius.** A gateway sees everything, so it is kept thin: it maps and validates requests and calls modules. No business logic, no decisions that belong in a module. A bug or an injection here must not become a bug in the engine.
- Gateways never log or echo request bodies or secrets (tunnel keys, tokens).
- Transport follows ADR 0003: loopback-only bind, a token on every request, the Tauri Rust side as the only client.
- Because gateways are leaves of the import graph (only `cmd/<binary>` imports them), they cannot be wired into modules and cannot create cycles.
- Milestones:
  1. This ADR, rule (f) and `Uses modules` in `arch_test.go`. No gateway yet.
  2. Move the app role out of `internal/pc`: `api/desktop` becomes the `splitter` app and wires `pc`. Done (2026-10-30): `api/desktop` owns `Main` and the command line; `internal/pc` is `Kind: child`, used by `api/desktop`, and exports what the CLI needs.
  3. Control API (`internal/pc/PLAN.md` M12). Done (2026-10-31, commit f793081): contract and decisions in `api/desktop/API.md`.
  4. Server-management endpoints. Done (2026-10-31): the new child `internal/pc/servers` (parsers, validation, storage, activation; contract in its `API.md`), the `pc` exports (`pc.Server` alias, `AddServer`, `ActivateServer`, ...) and the endpoints `GET/POST /servers`, `POST /servers/{id}/activate`, `DELETE /servers/{id}`, `POST /servers/test` in `api/desktop` (decisions 12-23 in `api/desktop/API.md`). `internal/pc` now starts in UI mode without a tunnel file. The gateway still imports only `internal/pc` for this (the alias), so its `Uses modules` did not change.
- ADR 0003 point 4 and its Open list now place the control API in `api/desktop` (updated with milestone 2).
