# BOOTSTRAP.md — Initialize traffic-splitter from an empty repo

Turns an empty folder into the project skeleton. It contains only **general rules and architecture rules**. Module details live in each module's `API.md`; this file never names a specific module.

This is milestone **M0**. No module logic is implemented here.

---

## 1. Architecture rules (fractal)

### 1.1 Module shape

The code is a tree. Every module is a folder with the same shape, at any depth:

```
<module>/
├── API.md          # REQUIRED: the module's contract (header + public API)
├── doc.go          # package doc (one-line purpose from API.md)
├── *.go            # root package = public API + wiring of its children
├── <child>/        # submodules used ONLY by this module
├── lib/            # modules shared by 2+ of this module's children
├── DESIGN.md       # optional: detailed design
└── AGENTS.md       # optional: scope, milestones and rules while the module is active
```

Trees:

- `internal/` is the application tree. `internal/lib/` holds modules shared across apps.
- `lab/` is the test-lab tree.
- `cmd/<binary>/` are entry points, outside both trees.
- `lib/` folders, `internal/` and `internal/lib/` are plain directories: no Go files and no `API.md`. Only modules inside them have those.

### 1.2 Placement rules

1. A module used by only one module lives **inside** it, as a child.
2. A module used by two or more modules lives in the `lib/` of their **lowest common ancestor**.
3. **Promote** a module when a consumer appears outside its parent's subtree (move it to the new lowest common ancestor's `lib/`). **Demote** it when only one consumer remains. Both require asking the human first and updating `API.md` headers in the same change.
4. **Consumers define the interfaces they need.** A module that needs something from a sibling declares a small interface in its own package; the parent wires the concrete implementation in.
5. Fakes live next to the interface or type they fake (`NewFake…` in the same package), never in a shared "fakes" module.
6. Each module owns its `Config` struct. App modules compose their children's and libs' configs into the app's YAML. There is no global config module.
7. Docs live with the code. `docs/` holds only project-level material.

### 1.3 Import rules (enforced by `arch_test.go`)

A package **P** may import a package **Q** from this repo only if one of these holds:

- **(a) Direct child:** Q is a direct child folder of P. Grandchildren are reached only through the child's API.
- **(b) Ancestor lib, root only:** Q is `<A>/lib/<name>` where A is P itself or any ancestor of P. Only that lib module's **root package** may be imported, never its children.
- **(c) Entry point:** `cmd/<binary>` imports only the app module whose `API.md` declares `Binary: <binary>`.
- **(d) Lab:** packages in `lab/` follow (a) and (b) within `lab/`, and may also import `internal/lib/<name>` roots.
- **(e) Test-only:** modules with `Kind: test-only` in `API.md` may be imported only from `_test.go` files and from `lab/`.

Everything else is forbidden: no sibling imports, no importing a parent, no reaching into another module's children. Siblings that need to share something get a `lib/` in their parent.

### 1.4 `API.md` header (machine-read)

Every module's `API.md` starts with this header. The architecture test and the bootstrap read it.

```markdown
# Module: <name>

| Field | Value |
|---|---|
| Path | internal/... |
| Kind | app \| child \| lib \| test-only \| lab |
| Binary | <binary name> (apps only, else –) |
| Purpose | one line, becomes the package doc |
| Consumers | modules that import this one |
| Children | direct child modules (incl. its own lib/ modules), or – |
| Uses libs | lib modules this one imports, or – |
| Build tags | e.g. `lab`, `linux`, or – |
| Status | planned \| design ready \| in progress \| done |
```

List fields are comma-separated repo paths. Text in parentheses is an annotation and is ignored by tools, e.g. `internal/lib/x (tests)`.

After the header, the rest of `API.md` follows this structure: **Responsibility**, **Public API** (Go signatures), **Behavior / contract**, **Config**, **Errors**, **Testing**, **Out of scope**, and **Bootstrap (M0)**, which says what this module needs in the skeleton (default: `doc.go` only).

`API.md` is a contract. Agents implement to it. Changing a public signature means asking first and updating `API.md` in the same change.

---

## 2. General rules

### 2.1 Code

- Go, one module (`go.mod` at the root). Standard library first.
- No global mutable state. Time, network, filesystem roots and processes go behind interfaces or are passed in.
- Everything network-facing or long-running takes a `context.Context`.
- OS-specific code only in `*_windows.go` / `*_linux.go`. **Every package compiles for all target platforms**: windows/amd64, linux/amd64, linux/arm64 and linux/mipsle (softfloat). Unsupported parts return a clear `ErrUnsupported`.
- Exported identifiers have doc comments. Code, comments and identifiers in English.
- `cmd/<binary>/main.go` is one line: `os.Exit(<app>.Main(os.Args[1:]))`.

### 2.2 Dependencies

- Approved third-party Go modules are listed in root `AGENTS.md`. Anything else: ask first. Do not upgrade pinned versions unless asked.
- Third-party binaries are never committed. They are listed in `third_party/manifest.yaml` with pinned versions and sha256.

### 2.3 Testing

- `go test ./...` must pass without network access or admin rights.
- Real-network tests: `//go:build integration`, never required for "done".
- Custom TLS roots and test CAs: only under `//go:build lab`.
- Lab tests always tear down, even on failure.
- Golden files live in the module's `testdata/` and are updated only with an explicit `-update` flag.

### 2.4 Hard rules (never break)

- Never install or trust root certificates on a real system. Never MITM user traffic.
- Agents never run commands needing admin/root and never change system DNS, proxy, firewall, routes or services. Print the exact command for the human instead.
- Never hardcode or commit secrets, server addresses or credentials.
- Logs, decisions and reports stay local (they are browsing history). No telemetry.
- Keep probing polite: low concurrency, jitter. Never target censorship infrastructure.
- No help with carrier billing bypass, malware, or attacking/overloading any infrastructure.

### 2.5 Agent workflow

1. One milestone of one module per task, as named in the prompt.
2. Read root `AGENTS.md`, then the module's `API.md` (plus `DESIGN.md` / `AGENTS.md` if present). State a short plan first.
3. Small, focused changes. No refactors outside the task.
4. Done = `go vet ./...`, `go test ./...` (includes the architecture test), `go test -tags lab ./...` and `make cross` all pass.
5. Report what was done, how it was tested, and open questions. **Do not commit**; the human reviews and commits.
6. Stop and ask when: docs conflict, a dependency is needed, a public API or placement would change, something needs admin rights, or a test needs the real network.

---

## 3. Part A — Human steps

1. Install Go (stable), Git, optionally `make` and `golangci-lint`, and your agent. Check that `go version` and `git --version` work.
2. Choose the module path, e.g. `github.com/<you>/traffic-splitter`.
3. Create the empty repo and copy in:
   - `BOOTSTRAP.md` (repo root);
   - `docs/project-notes.md`;
   - every module's `API.md` at its path, plus any `DESIGN.md` / `AGENTS.md` that already exist.
4. Commit ("docs: initial contracts").
5. Start the agent in the repo root:
   > Read BOOTSTRAP.md and do Part B. Module path is `github.com/<you>/traffic-splitter`. Stop when the checks in B.8 pass and report back.
6. Review the diff, commit ("chore: project skeleton"), push, check that CI is green.

---

## 4. Part B — Agent task

### B.0 Scope

Build the skeleton from the `API.md` files. Implement no module logic beyond each `API.md`'s **Bootstrap (M0)** section. Add no third-party Go dependencies. Do not edit any existing `.md` file except where B.5 says so.

### B.1 Go module

`go mod init <module path from the prompt>`. Keep the Go version it writes.

### B.2 Modules from `API.md`

For every `API.md` under `internal/` and `lab/`:

- ensure the folder exists;
- create `doc.go`: `// Package <name> <Purpose>.` plus `package <name>`, with the header's build tags as `//go:build` lines;
- apply its **Bootstrap (M0)** section exactly.

For every module with `Kind: app`, create `cmd/<Binary>/main.go` as the one-liner from 2.1.

If any header field is missing or inconsistent (e.g. a listed child without its own `API.md`, or Consumers that break the placement rules), stop and report it.

### B.3 Architecture test

Create `arch_test.go` at the repo root (`package arch_test`, standard library only):

- **Module registry:** walk the repo, parse every `API.md` header (Path, Kind, Binary, Build tags).
- **Registry checks:** every Go package under `internal/` and `lab/` has an `API.md` in its folder (`testdata/` excluded), and every `API.md` has a Go package.
- **Import checks:** load packages with `go list -json ./...` (via `os/exec`), for the default build, with `-tags lab`, and with `GOOS=linux` and `GOOS=windows`, including `Imports`, `TestImports` and `XTestImports`. Apply rules (a)–(e) to every in-repo import.
- **Consumer check:** for every in-repo import P → Q, P must be listed in Q's `Consumers` (by path), and Q must be in P's `Children` or `Uses libs`. Undeclared dependencies fail.
- Put the rule logic in a pure function `allowed(from, to string, isTest bool, reg Registry) (bool, string)` and table-test it with synthetic paths. Minimum cases:

| from | to | test? | expected |
|---|---|---|---|
| `internal/x` | `internal/x/y` | no | allow (a) |
| `internal/x` | `internal/x/y/z` | no | deny: grandchild |
| `internal/x/y` | `internal/x/lib/s` | no | allow (b) |
| `internal/x/y` | `internal/lib/m` | no | allow (b) |
| `internal/x/y` | `internal/lib/m/n` | no | deny: lib child |
| `internal/x/y` | `internal/x/w` | no | deny: sibling |
| `internal/x/y` | `internal/x` | no | deny: parent |
| `internal/lib/m/n` | `internal/lib/m/lib/t` | no | allow (b) |
| `internal/v` | `internal/x/lib/s` | no | deny: not an ancestor's lib |
| `cmd/b` | app with `Binary: b` | no | allow (c) |
| `cmd/b` | `internal/lib/m` | no | deny (c) |
| `internal/x` | test-only lib | yes | allow (e) |
| `internal/x` | test-only lib | no | deny (e) |
| `lab` | `lab/k` | no | allow (d) |
| `lab/k` | `internal/lib/m` | no | allow (d) |
| `lab/k` | `internal/x` | no | deny (d) |

- Failures print a readable list: `from → to: reason`.

### B.4 Root files

- **`.gitignore`**: build output (`/bin/`, `/dist/`, `*.exe`, `*.test`, `*.out`, `coverage.*`), `/third_party/bin/*` (keep `.gitkeep`), runtime data and secrets (`/runtime/`, `*.log`, `.env`, `*.key`, `*.pem`, `secrets/`, `app.yaml`), and editor/OS files.
- **`.gitattributes`**: `* text=auto eol=lf`, with `*.ps1` and `*.bat` as `eol=crlf`.
- **`.editorconfig`**: LF, final newline, UTF-8, 2 spaces; tabs for `*.go` and `Makefile`.
- **`.golangci.yml`**: `version: "2"`, `linters: default: standard`.
- **`Makefile`**:
  - `build`: every `cmd/*` into `bin/`, with `-ldflags` setting the version variable described by the version module's `API.md`;
  - `test`, `test-lab` (`-tags lab`), `arch` (`go test -run TestArchitecture .`), `vet`, `lint`;
  - `cross`: `go build ./...` for windows/amd64, linux/amd64, linux/arm64, linux/mipsle (`GOMIPS=softfloat`);
  - `clean`.
- **`README.md`**:
  - one paragraph on the project (from `docs/project-notes.md`);
  - "Early development — skeleton only";
  - the binaries (from the app `API.md` headers);
  - the fractal layout in two sentences, with a link to `docs/architecture.md`;
  - the make and plain `go` commands.
- **`third_party/manifest.yaml`**: `binaries: []` with a comment on the format (name, version, platforms, url, sha256 per platform). Filled in by the human.
- `third_party/bin/.gitkeep`, `lists/.gitkeep`, `configs/.gitkeep`, `packaging/.gitkeep`, `scripts/.gitkeep`, `docs/adr/.gitkeep`.

### B.5 Agent instruction files

- **Root `AGENTS.md`** containing:
  - sections 1 and 2 of this file, condensed;
  - "Approved Go modules: `github.com/refraction-networking/utls`, `github.com/miekg/dns`, `gopkg.in/yaml.v3`. Others: ask.";
  - "Never weaken `arch_test.go` to make a change pass.";
  - "Where to find things: `docs/project-notes.md`, `docs/architecture.md`, each module's `API.md`."
- **Root `CLAUDE.md`**: `See AGENTS.md.`
- For every module folder that has an `AGENTS.md`, add a `CLAUDE.md`: `See AGENTS.md in this folder and the root AGENTS.md.`

### B.6 Docs

- **`docs/architecture.md`**, **generated from the `API.md` headers** (no invention):
  1. Overview (from `docs/project-notes.md`)
  2. The rules from section 1 of this file
  3. Module tree (paths + Purpose)
  4. Placement table: Path, Kind, Consumers, Children, Status
  5. Binaries → app modules
- **`docs/adr/0001-go-monorepo.md`**: Go, single module, several binaries; why (sing-box ecosystem, uTLS, static cross-compiled binaries, router RAM).
- **`docs/adr/0002-fractal-architecture.md`**: the placement and import rules; why (each module testable and replaceable alone, agents work on one subtree, shared code visible in one place); enforcement via `arch_test.go` rather than nested Go `internal/` folders.

### B.7 CI

`.github/workflows/ci.yml` on push and pull_request, `ubuntu-latest`:

1. checkout, setup-go with `go-version-file: go.mod` (latest major versions of the actions);
2. `go vet ./...`, `go test ./...`, `go test -tags lab ./...`;
3. `make cross`;
4. golangci-lint action.

### B.8 Checks

```bash
go vet ./...
go test ./...            # includes TestArchitecture
go test -tags lab ./...
make cross
go run ./cmd/<each binary> --version
```

Also verify:

- `go.mod` has no `require` lines;
- a temporary bad import makes `TestArchitecture` fail with a clear message (then remove it);
- `git status` shows nothing that `.gitignore` should catch;
- no existing `.md` file was modified.

### B.9 Report

Reply with the `tree` of `internal/` and `lab/`, the check outputs, and any inconsistencies found in `API.md` headers. Do not commit.
