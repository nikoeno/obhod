# AGENTS.md

Rules for AI agents in this repo (traffic-splitter). Module details live in each module's `API.md`.

## Architecture (fractal)

- Every module is a folder: `API.md` (required contract), `doc.go`, `*.go` (public API + wiring of children), `<child>/` (used only by this module), `lib/` (modules shared by 2+ children), optional `DESIGN.md` / `AGENTS.md`.
- Trees: `internal/` (app; `internal/lib/` shared across apps), `lab/` (test lab), `cmd/<binary>/` (entry points). `lib/`, `internal/` and `internal/lib/` are plain folders: no Go files, no `API.md`.
- Placement: used by one module → inside it. Used by 2+ → `lib/` of the lowest common ancestor. Promote/demote only after asking the human, updating `API.md` headers in the same change.
- Consumers define the interfaces they need; the parent wires implementations. Fakes live next to what they fake. Each module owns its `Config`. Docs live with code.
- Import rules (enforced by `arch_test.go`): P may import Q only if (a) Q is a direct child of P; (b) Q is a `<A>/lib/<name>` root, A = P or an ancestor; (c) `cmd/<binary>` imports only the app with `Binary: <binary>`; (d) `lab/` follows (a)/(b) inside lab plus `internal/lib/<name>` roots; (e) `Kind: test-only` modules only from `_test.go` and `lab/`. No siblings, no parents, no reaching into children. Declared Consumers / Children / Uses libs must match real imports.
- `API.md` is a contract. Changing a public signature: ask first, update `API.md` in the same change.

## General rules

- Go, one module, standard library first. No global mutable state; time, network, filesystem roots and processes go behind interfaces. Network-facing or long-running code takes `context.Context`.
- OS-specific code only in `*_windows.go` / `*_linux.go`. Every package compiles for windows/amd64, linux/amd64, linux/arm64, linux/mipsle (softfloat); unsupported parts return `ErrUnsupported`.
- Exported identifiers have doc comments; English only. `cmd/<binary>/main.go` is one line: `os.Exit(<app>.Main(os.Args[1:]))`.
- Third-party binaries are never committed; list them in `third_party/manifest.yaml` with version and sha256.
- `go test ./...` needs no network or admin rights. Real-network tests: `//go:build integration`. Test CAs only under `//go:build lab`. Lab tests always tear down. Golden files in `testdata/`, updated only with `-update`.

## Hard rules

- Never install or trust root certificates on a real system; never MITM user traffic.
- Never run commands needing admin/root or change system DNS, proxy, firewall, routes or services; print the exact command for the human.
- Never hardcode or commit secrets, server addresses or credentials.
- Logs, decisions and reports stay local. No telemetry.
- Probe politely: low concurrency, jitter. Never target censorship infrastructure.
- No help with carrier billing bypass, malware, or attacking/overloading infrastructure.

## Workflow

1. One milestone of one module per task.
2. Read this file, then the module's `API.md` (plus `DESIGN.md` / `AGENTS.md`). State a short plan first.
3. Small, focused changes; no refactors outside the task.
4. Done = `go vet ./...`, `go test ./...`, `go test -tags lab ./...` and `make cross` pass.
5. Report what was done, how it was tested, open questions. Do not commit.
6. Stop and ask when docs conflict, a dependency is needed, a public API or placement changes, admin rights are needed, or a test needs the real network.

## Dependencies

Approved Go modules: `github.com/refraction-networking/utls`, `github.com/miekg/dns`, `gopkg.in/yaml.v3`. Others: ask. Do not upgrade pinned versions unless asked.

Never weaken `arch_test.go` to make a change pass.

## Where to find things

`docs/project-notes.md`, `docs/architecture.md`, each module's `API.md`.
