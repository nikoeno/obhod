# Implementation plan — checker (internal/lib/checker)

Status: M1–M7 done (checker `implemented`); M8 (lab scenario) blocked on `lab/*`. The open questions in §3 were resolved by taking each proposal (Q1 `Deps.Resolver`, Q2 `Store.Touch`, Q3 `Store.Warnings()`, Q4 `UnknownDefault`, Q5 no `paths` import, Q6 no probing until known, Q7 `Config.Retention`, Q8 `FakeNetwork`, Q9 type aliases, Q10 `Scheduler.Next()`, Q11 no); the API changes are recorded in the modules' `API.md`. Decisions, deviations and known gaps are folded into `DESIGN.md`; delete this file once M8 is done or moved to the lab plan. Source of truth stays in each module's `API.md`; this file only orders the work and lists the gaps found while reading them. Delete it (or fold the leftovers into `DESIGN.md`) when the checker reaches `implemented`.

## 0. Starting point

| Module | State |
|---|---|
| `checker` + 6 children | `doc.go` + `API.md` only (M0) |
| `internal/lib/route` | implemented (Route, Entry, Table, Diff, Normalize) |
| `internal/lib/clock` | implemented (Real, Fake with `BlockUntil`) |
| `internal/lib/netif` | Uplink, BindControl, Watch, gateway/ARP in the working tree (uncommitted); SSID always "" |
| `internal/lib/analyzer` | implemented, direct-only: Runner, Report, Calibration, SuggestRoute, LookupASN |

Every dependency the checker needs already exists, so the work goes bottom-up with no stubs in other modules.

## 1. Milestones

One milestone per task (root `AGENTS.md`, Workflow 1). Each ends with `go vet ./...`, `go test ./...`, `go test -tags lab ./...`, `make cross` passing, and a status update in that module's `API.md` header and in `docs/architecture.md` §4.

Dependency order:

```
M1 model ─┬─ M2 decide ─┐
          ├─ M4 store  ─┤
          ├─ M5 schedule┼─ M7 checker root ── M8 lab scenario
          └─ M6 netwatch┘
M3 lists (independent, needs only route) ─┘
```

M2–M6 can run in any order after M1; M3 can start at once.

### M1 — `lib/model`

Files: `model.go`, `model_test.go`.

- `NetworkID` + `Valid()`: non-empty, prefix `asn:` or `gw:` (or `asn:…/gw:…`), no whitespace, bounded length (it becomes a JSON key and appears in logs).
- `Decision`, `Pending` with explicit `json:"snake_case"` tags; `Pending` and `Last` use `omitempty`.
- `Expired(now)`: `!ExpiresAt.IsZero() && !now.Before(ExpiresAt)`.

Tests: JSON round-trip (including a nil `Pending` and an empty `Last`), a golden JSON file in `testdata/` so field names can't drift silently, `Expired` at the boundary.

### M2 — `decide` (pure)

Files: `decide.go`, `decide_test.go`.

- `Policy` defaults via an unexported `withDefaults()` (Confirmations 2, TTL 24h, Order direct/desync/tunnel); `Order` entries must be `Checkable()`.
- `Candidate`:
  - one result for `route.Direct` → `analyzer.SuggestRoute`; `LOCAL_ERROR`/`UNKNOWN` or `ok=false` → no conclusion;
  - several results → the first route in `Order` whose verdict is `OK`/`OK_REDIRECT`; all failed → "site down", no conclusion.
- `Decide`:
  1. No conclusion → return `Current` unchanged (`Changed=false`), no `CheckedAt` refresh. Nil `Current` + no conclusion → zero `Decision`, and the caller must not store it (see §3, Q6).
  2. Nil `Current` → adopt the candidate, `Source=checked`, `DecidedAt=CheckedAt=Now`.
  3. Candidate == `Current.Route` → clear `Pending`.
  4. Candidate ≠ current → `Pending` for the same route: `Count++`; a different route: reset to `{route, 1, Now}`. Switch when `Count >= Confirmations`, then clear `Pending` and set `DecidedAt`.
  5. Every conclusive run: `CheckedAt=Now`, `ExpiresAt=Now+TTL`, `Last` = the verdicts of this run.
- Never returns `proxy`/`block`. Enforce it with a guard and cover the guard with a test.

Tests: table-driven over every `Verdict` for direct-only; hysteresis sequences (A,B,A,B never switches; A,B,B switches when Confirmations=2); Confirmations=1; TTL refresh without a change; a stale `Pending` for another route gets replaced.

### M3 — `lists`

Files: `lists.go`, `lists_test.go`, `testdata/*.txt`.

- `Parse`: `bufio.Scanner` with a raised max line length; strip `#` comments (whole-line and trailing) and whitespace; strip the leading `*.` / `.`; `route.Normalize`; invalid → warning `name:line: reason`. Dedup inside the file.
- `Load`: read every file in the three groups; precedence block > proxy > candidate; a domain in a static list is removed from `Candidates`; `Static` entries get `Source=static`; the output is sorted and deduped.
- Filesystem access: `Load(cfg)` opens paths directly, and tests use `t.TempDir()`. Taking an `fs.FS` would need an API change (Q7), so it's not planned.

Tests: fixtures with comments, IDN, `*.`, IPs, URLs (warnings with correct line numbers), precedence (the same domain in all three lists), a missing file → error.

### M4 — `store`

Files: `store.go` (types, Open), `file.go` (format, atomic write), `memory.go` (`NewMemory`), `sync_linux.go` / `sync_other.go` (directory fsync), tests.

- In memory: `map[NetworkID]*network{label, lastSeen, decisions map[string]Decision}` under a `sync.RWMutex`. `Get`/`All` return copies (`Last`/`Pending` deep-copied).
- `Put`: compare with the stored value (`reflect.DeepEqual` or a hand-written equality); if nothing changed → no dirty flag. Update `LastSeen = clock.Now()`.
- Flush: `FlushInterval == 0` → write inside `Put`; otherwise mark dirty and let a timer from `clock` write. `Close` flushes. A write failure is returned from `Put` (immediate mode) or kept and returned from the next `Put`/`Close` (coalesced mode).
- Atomic write: `os.CreateTemp(dir, "decisions-*.tmp")` → encode → `Sync` → `Close` → `os.Rename` → fsync the dir (Linux only; a no-op elsewhere). Permissions 0600: `Chmod` on Linux; on Windows reuse the `internal/lib/paths` helper if it offers one (Q5).
- `Open`: a missing file → empty; bad JSON or `schema != 1` → rename to `decisions.json.corrupt-<unix>` and start empty; delete leftover `decisions-*.tmp` files.
- `Prune(before, keep)`: drop networks with `LastSeen < before`; drop decisions whose domain isn't in `keep`.

Tests: round-trip through `Open`; a leftover temp file is ignored and removed; corrupt-file recovery; coalescing with `clock.Fake` (N Puts → 1 write, counted with a write hook in a test-only seam); unchanged Put → no write; `-race` with concurrent Get/Put.

### M5 — `schedule`

Files: `schedule.go`, `schedule_test.go`.

- A single goroutine (`Run`) owns the state; public methods send events on an internal channel (non-blocking for callers, with a buffer, since `Failure` can come from a hot monitor path).
- State: `pending *Trigger` (at most one outstanding), `inFlight bool`, `lastFull time.Time`, a timer for the next periodic check, failure counters `map[key][]time.Time` pruned to `FailWindow`.
- Merge: kinds by priority (network_change > failures > manual > expired > timer > startup) for the reported `Kind`; domains are a union and `nil` absorbs everything. A network change replaces the pending trigger's `Network`.
- Emit: the pending trigger goes out on `Triggers()` (unbuffered/1) only when `!inFlight`; `Done` clears `inFlight` and re-emits whatever merged meanwhile. Full checks (`Domains == nil`, other than a network change) wait until `lastFull + MinGap`.
- Startup: emit `Startup` (full) once `Run` begins.
- Jitter: an unexported `newWithRand(cfg, clock, *rand.Rand)` for tests; `New` seeds from the clock. Keep `Interval - Jitter` ≥ `MinGap`.

Tests with `clock.Fake`: jitter bounds over many ticks, merge semantics (nil absorbs), MinGap delays a timer trigger but not a network change, threshold/window (2 failures, window expires, 1 more → no trigger), the counter resets after a trigger, no triggers while in flight.

### M6 — `netwatch`

Files: `netwatch.go`, `identify.go`, `echo.go` (default public-IP lookup), tests.

- `Run`: subscribe to `Events` (default `netif.Watch`), identify once at start, then on each event (debounced via `clock` timer) and every `Refresh`.
- Identify: `Uplink()` → `PublicIP()` (default: GET the echo endpoints through an `http.Client` whose dialer uses `netif.BindControl(uplink)`, so the lookup goes direct) → `analyzer.LookupASN(ctx, Resolver, ip)`.
- ID: `asn:<n>`; fallback `gw:<hex(sha256(salt|gatewayMAC|SSID))[:16]>`; with `IncludeGateway`, both. Neither available → `Known=false`, except when the uplink name and gateway match the previous one, in which case keep the last ID.
- `Changes()` is buffered 1, latest-wins, and emits only when `ID` or `Known` changes. `Current()` is guarded by a mutex.
- `PublicIP` never leaves memory and is never logged.

Tests: fakes for all four deps; ASN path; ASN failure → gw fallback; `IncludeGateway`; debounce (5 events in 1 s → 1 identify); same ID → no event; the same salt gives the same ID and a different salt a different one; identify fails with an unchanged uplink → last ID kept.

### M7 — checker root

Files: `checker.go` (New, Deps, Config, re-exports), `loop.go` (Run, the cycle), `table.go` (build the table), `adapter.go` (netwatch → NetworkSource), `fake.go` (a `FakeNetwork` for apps' tests, if the API allows it; see Q8), tests.

Wiring in `New`:

1. Validate the config; fill `Store.File` from `Deps.DataDir` when empty.
2. `lists.Load` (warnings → `Status.Warnings`).
3. `store.Open`.
4. `schedule.New`.
5. `Deps.Network` or a netwatch adapter (needs a resolver; see Q1).

`Run(ctx)`: an `errgroup`-style set of goroutines using only the stdlib (`sync.WaitGroup` + first error): network source, scheduler, main loop. The main loop selects on:

- `Network.Changes()` → switch `cur`, rebuild and publish the table from the store, then `sched.NetworkChanged(id)`.
- `sched.Triggers()` → run the cycle (below) synchronously → `sched.Done(t)`. There is one loop, so only one check runs at a time.
- the expiry timer (earliest `ExpiresAt` among the current network's decisions) → `sched.Expired(id, domains)`.
- `ctx.Done()` → `store.Close()`, return.

The cycle:

1. The network is unknown → skip probing (Q6), but still publish the `UnknownDefault` table.
2. Targets = `t.Domains ∩ candidates`, or all candidates.
3. `Runner.Run(ctx, Batch{Targets, Dialers})`. An error or `Calibration.Internet != ok` → no store changes; record a warning; done.
4. Group the results by domain into `map[Route]ProbeResult`; `decide.Decide` per domain; collect the changed outputs → one `store.Put(net, ...)`.
5. Rebuild the table: static + stored (`Source=checked`) + `SourceDefault` for candidates with no decision (route = `UnknownDefault` or `Policy.Order[0]`? Q4). `route.NewTable` → publish if `!Equal(prev)`.
6. Update `Status` (LastCheck, NextCheck from the scheduler if exposed, Counts, Expired, LastReport).
7. The network changed while the probe ran → drop the results (they belong to the old network).

Publishing: `Table()` reads an `atomic.Pointer[route.Table]`. `Subscribe` keeps a set of `chan route.Table` (cap 1); on publish, drain the old value and send the new one (latest-wins); the returned cancel func removes the channel and closes it.

`ReportFailure`: map the domain to its table entry; ignore static routes and failures on a route other than the current one; otherwise `sched.Failure(cur, domain, route, at)`. `CheckNow` → `sched.Manual`.

A store `Put` error in `Run` is fatal (API.md, Errors); everything else goes to warnings.

Tests (listed in API.md) with a fake Runner (scripted Reports), a fake NetworkSource, `clock.Fake`, `store.NewMemory` (through an unexported `newWithStore` seam):

- start on an unknown network → a table with defaults, no probes;
- network A decided → switch to B → switch back to A → A's decisions come back before any probe;
- hysteresis over two runs;
- 3 `ReportFailure` in the window → a targeted recheck of that domain only;
- a static entry stays static even when a probe says otherwise;
- calibration failed → no store writes, the table is unchanged;
- a network change during a probe → the results are dropped;
- `Subscribe` latest-wins with a slow reader; cancel closes the channel;
- `-race` on everything.

### M8 — lab scenario (optional, `//go:build lab`)

In `lab/`, with `fakedpi` + `fakeasn` once they exist: a checker with a real Runner in a netns; blocking a domain flips it direct → desync/tunnel after `Confirmations` runs; changing the fake ASN switches the table. Blocked on `lab/*` being implemented, so it may move into the lab's own milestone plan.

## 2. Cross-cutting rules

- No goroutine outlives `Run`; every test calls `cancel()` and waits for `Run` to return (`goleak`-style check without the dependency: compare `runtime.NumGoroutine` loosely, or just rely on `-race` + a deterministic shutdown).
- All time comes from `clock`; tests never `time.Sleep`. Use `Fake.BlockUntil` to sync.
- Logging: none inside the checker. Surface everything through `Status.Warnings` / `Output.Reason`; apps decide what to log.
- Cross builds: only `store` (dir fsync) and possibly `netwatch` (nothing yet) have OS files.

## 3. Open questions (need a human before or during the relevant milestone)

1. **Resolver for netwatch** (M6/M7): `netwatch.Deps` needs a DoH `analyzer.Resolver`, but `checker.Deps` has none. Options: add `Deps.Resolver analyzer.Resolver` to the checker, or require apps to pass `Deps.Network`. Proposal: add `Resolver` (an API change, so ask first).
2. **Store labels / LastSeen** (M4/M7): `store.Put` doesn't take a label, and there is no call to mark a network as seen without writing decisions. Proposal: `Put` updates `LastSeen`, and add `Touch(net model.NetworkID, label string) error` (an API change).
3. **Store warnings** (M4): the API says "report via the returned warning", but `Open` returns `(Store, error)`. Options: `Open(...) (Store, []string, error)` or a `Warnings() []string` method on `Store`. Proposal: a method on the interface.
4. **Default route for undecided candidates on a known network** (M7): API.md says "`SourceDefault` entries for candidates with no decision" without a route. Proposal: `UnknownDefault` (tunnel), the safe side.
5. **0600 on Windows** (M4): `paths` only has an unexported `restrictACL(dir)`, which `Paths.Ensure` applies to directories. Proposal: store relies on the data dir's ACL from `paths.Ensure` (the files inherit it) and calls `Chmod(0600)` only on Linux. Store doesn't import `paths`, so no new dependency.
6. **Probing on an unknown network** (M7): decisions can't be stored without an ID. Proposal: don't probe until `Known`; netwatch's first identification emits a change, and that triggers the check.
7. **Prune policy** (M7): nobody calls `store.Prune`. Proposal: on `Run` start, `Prune(now-90d, candidates)`; add `Config.Retention` (an API change).
8. **Test fake for apps** (M7): should the checker export a `Fake` (e.g. `checker.NewFake(table)`) for `internal/pc` / `internal/router` tests? Nothing in API.md yet.
9. **`checker.ListsConfig` re-exports** (M7): API.md types `Config` fields as `lists.Config` etc. but also says apps use `checker.ListsConfig`. Proposal: type aliases in the root, and the struct fields use the aliases.
10. **NextCheck in Status** (M5/M7): the scheduler exposes no "next due" time. Proposal: `func (s *Scheduler) Next() time.Time` (an API change).
11. **Failures on a domain with a pending switch** (M2): should a live failure count as an extra confirmation? Proposal: no; it only triggers a recheck, and the probe decides.
