# Desktop UI: pages, tabs and user actions

Status: draft (2026-10-25), updated 2026-11-01. Describes the Tauri desktop app from ADR 0003 (`docs/adr/0003-desktop-ui-and-packaging.md`). The Tauri app lives in `ui/` (ADR 0003, resolved 2026-11-01; `ui/README.md`); Stage A (2026-11-01) ported the shell, Home and Settings and Stage B (2026-11-01) ported Sites, Activity and Help; all five pages are built in `ui/src/pages/`. The old clickable prototype (`ui/web`, `ui/mock`) was deleted after the port and lives in git history only. Without an engine the app runs against the in-memory fake backend (`npm run dev`, `ui/src/dev/fakeBackend.ts`), which follows the implemented control API (`api/desktop/API.md`: M12 and server management, milestone 4). The server-management flow (Settings → Detour server and the first-run card on Home) was **prototyped first** (2026-10-31) and is built. Every action below maps to a control API call served by the gateway `api/desktop` (ADR 0004); actions marked **later** need a separate decision first.

Visual style: one large round accent button, neutral cards, one purple accent (`#534ab7` light / `#7f77dd` dark), system font.

## 1. Principles

- **One button.** The main window is a Connect/Disconnect button. Everything else is optional and one click away.
- **No network words on the main surfaces.** Users see "Everyday / Blocked / Picky sites", not `direct / tunnel / desync`. Technical names appear only in Details and in exported diagnostics.
- **Automatic first.** The app decides routes itself; the UI explains and lets the user check, never requires configuration.
- **Privacy.** No public IP anywhere (the network is shown by its label). Host names are shown only for the user's own recent activity and never leave the machine. No telemetry, no external assets.
- **Honest state.** The UI never shows "connected" unless `splitter` says so; every error has a plain sentence and one suggested action.

## 2. Vocabulary (user words ↔ engine routes)

| User sees | Route | One-line explanation |
|---|---|---|
| Everyday sites | `direct` | Straight through, full speed |
| Blocked sites | `tunnel` | A safe detour, only when needed |
| Picky sites | `desync` | A quiet fix, right on your device |
| Via proxy | `proxy` | Through your extra proxy (only shown when one is configured) |
| Blocked by you | `block` | Never opened (only shown when the list has entries) |

## 3. Window structure

```
┌───────────────────────────────────────────────┐
│  Obhod                         [—] [□] [×]     │
├──────────┬────────────────────────────────────┤
│ Home     │                                    │
│ Sites    │          page content              │
│ Activity │                                    │
│ Settings │                                    │
│ ──────── │                                    │
│ Help     │                                    │
└──────────┴────────────────────────────────────┘
```

- Left navigation with four pages plus Help. Narrow windows (< 640 px) collapse it to a bottom tab bar.
- A tray / notification-area icon is always present while the app runs (section 9).
- Closing the window hides it to the tray; **Quit** lives in the tray menu and in Settings. In v1, quitting also stops `splitter` (it runs with `--parent-pid`).

## 4. Connection states

The UI mirrors `App.State()`; it never invents its own states.

| State | Button | Button label | Subtitle | Tray icon |
|---|---|---|---|---|
| disconnected | accent, enabled | Start | "Off. Your traffic goes as usual." | grey |
| connecting | accent, pulsing, disabled | Starting… | "Asking for permission and starting." | grey, animated |
| connected | accent ring, enabled | Stop | "On. N% of your traffic stays off the detour." | accent |
| disconnecting | disabled | Stopping… | – | grey, animated |
| error (disconnected after a failure) | accent, enabled | Try again | the error sentence (section 10) | grey with a dot |
| no engine (`splitter` not running / API unreachable) | disabled | – | "The engine is not running." + **Restart engine** | grey with a dot |

## 5. Pages

### 5.1 Home (default page)

The one-button screen.

| Element | Content | Source |
|---|---|---|
| Big button | Start / Stop (section 4) | `state` |
| Three path cards | Everyday / Blocked / Picky with the domain count and today's traffic for each; Proxy / Blocked-by-you cards appear only when non-empty | `Snapshot.Routes`, `Snapshot.Traffic` |
| Savings line | "78% of your traffic stays off the detour" | `Snapshot.Savings` |
| Network line | "Network: Home Wi-Fi (Example ISP) · checked 5 min ago" | `Snapshot.Network`, `LastCheck` |
| Warning banner | the first warning with a **Details** link; hidden when there are none | `Snapshot.Warnings` |
| Detour card | shown while no server is in use, never blocks **Start**. Off: "Add a detour server. You can start without one..." with **Add server**. On without a server (`Snapshot.Tunnel == "none"`): "Running without a detour server. Blocked sites stay unavailable until you add one." with **Add server**. A server saved but not in use: "Pick the server to use" with **Make active** | `Snapshot.Tunnel`, `GET /servers` |
| Blocked card caveat | while `Tunnel == "none"` the Blocked card has a muted line: "No detour server yet: these sites open directly and may not load." | `Snapshot.Tunnel` |

User actions:

| Action | Result | API |
|---|---|---|
| Click **Start** | Windows UAC / Linux pkexec prompt (v1), then connecting → connected | `POST connect` |
| Click **Stop** | disconnecting → disconnected | `POST disconnect` |
| Cancel the UAC/pkexec prompt | back to disconnected, subtitle "Permission was not given." | – |
| Click a path card | opens Sites filtered to that route | – |
| Click the network line | opens Activity → Network | – |
| Click **Check again** (next to the network line) | re-probes all sites now; the line shows "Checking…" | `POST check-now` |
| Click a warning's **Details** | opens Activity → Warnings | – |

### 5.2 Sites

What the app decided for each site, and the rules the user sets themselves.

Tabs:

| Tab | Shows |
|---|---|
| All | every domain in the current table |
| Everyday / Blocked / Picky | one route each (Proxy, Blocked by you appear only when non-empty) |

List columns: site, path (user word + colour dot), since (when it moved there), decided (how: checked on this network, from a fixed list, or a safe default until checked; `Route.source`). The API has no per-site traffic (monitor rule, decision 6), so there is no traffic column; traffic is shown per path on Home and Activity.

User actions:

| Action | Result | API |
|---|---|---|
| Search box | filters the list by substring, locally | – |
| Switch tab | filters by route | – |
| Click a site | side panel: current path, how it was decided, history of route changes for it, time of the last check (global, not per site) | `routes`, events |
| **Check this site again** (side panel) | re-probes that one domain | `POST check-now {domains:[…]}` |
| **Check all again** (toolbar) | re-probes everything | `POST check-now` |
| **Always use …** (side panel) | opens a dialog with the four paths in user words (Everyday, Picky, Blocked, Blocked by you); **Save** sets the per-domain rule (`direct`, `desync`, `tunnel`, `block`) | `POST sites` (API.md "Per-site rules") |
| **Remove my rule** (side panel, only when the site has a rule) | back to automatic | `DELETE sites/{domain}` |
| **Add a site** (toolbar) | dialog with a site field and the same four paths; a rule for a domain in no list adds it to the table (`source: "user"`); editing the list files stays out of scope (the starter lists are read-only to the app) | `POST sites` |

No detour server (2026-11-01): a row of the Blocked path with `degraded: true` shows a small **No detour** flag next to the path, and the side panel repeats the flag with a sentence ("There is no detour server yet, so this site opens directly and may not load..."). The row keeps its path and tab (Blocked); the flags clear by themselves when a server is in use (`Tunnel` becomes `ready` and the table is refetched).

Empty state (no network identified yet): "Still learning this network. Sites will appear in a few seconds."

Built (Stage B, 2026-11-01): rows are sorted by domain; the tab counts come from the table; Via proxy and Blocked by you tabs appear only when non-empty. Deep links: `#/sites?route=<route>` (Home cards), `#/sites?domain=<d>&q=<d>` (a change on Activity opens that site's panel). **Check all again** and **Check this site again** use `check-now`; the result arrives as a `check` event and a toast. Esc closes the panel and returns focus to the row; Ctrl+F focuses the search box.

Built (per-site rules, 2026-11-01): **Add a site** and **Always use…** open the same dialog (`SiteRuleDialog`; Esc closes it and focus returns to the button that opened it). The UI cleans the typed or pasted site before sending (`cleanSiteInput`): scheme, user info, path, query, fragment, port, a trailing dot and a leading `*.` or `.` are dropped and the name is lower-cased; a leading `www.` is kept only if typed. Empty input, IP addresses and anything that is not a name get an inline message and nothing is sent. Engine errors stay inline in the dialog in fixed sentences (`invalid_input`, `limit_reached` at 500 rules, `not_found`); success shows a toast ("example.com will always use Blocked."). After every site call the app refetches `GET /sites` and `GET /routes`, since a change of source alone sends no `routes` event. A row with `source: "user"` shows **Set by you** in the decided column; the tab follows the `route` the engine reports (a `block` rule is under **Blocked by you**, a `tunnel` rule under **Blocked**). For such a site **Check this site again** is disabled (the checker leaves a ruled site alone) and the panel says why. **Remove my rule** shows when the row's source is `user` or a stored rule exists for the domain. The Rust side validates the action and the name length, percent-encodes the name in the `DELETE` path, and replaces the engine's message of `invalid_input`, `limit_reached`, `not_found` and `bad_request` with a fixed sentence.

### 5.3 Activity

What happened recently. Tabs:

| Tab | Shows | Source |
|---|---|---|
| Changes | timeline of route changes: "youtube.com moved from Everyday to Picky · 14:02" | `Snapshot.Recent`, events (`route.Diff`) |
| Traffic | live upload/download totals and connection count, a small chart per route for the session | `/api/traffic`, traffic tick event |
| Network | current network label, last and next check, history of networks seen this session (labels only) | `Snapshot.Network`, `LastCheck`, `NextCheck` |
| Warnings | full list of current warnings with a plain explanation and a suggested action each | `Snapshot.Warnings` |

User actions: switch tabs, click a change to open that site in Sites, **Clear view** (clears only the on-screen timeline, not any data).

Built (Stage B, 2026-11-01): Changes shows `Snapshot.Recent` (the last 20 from the engine) plus the changes of `routes` events, newest first, capped at 50 on screen. Traffic shows the totals of the last `traffic` event and one chart per path (30 samples of 2 s, one shared scale; Via proxy only when it has traffic); the numbers are per path, never per host. Network lists the label, last and next check, and the networks seen this session (labels only). Warnings lists each warning with a plain explanation and one suggested action; below it the **engine processes** with their states (the same list as Help). Everything shows "The engine is not running." while the engine is down.

### 5.4 Settings

Few settings by design. Tabs:

| Tab | Settings / actions | Notes |
|---|---|---|
| General | Start the app when I sign in (on/off); Connect automatically when the app starts (on/off); Close button hides to tray (on/off); Language | UI-side settings, stored by the Tauri app |
| Detour server (**prototyped 2026-10-31**) | A list of servers (name, protocol; host and port hidden behind **Show address**) with an **Active** marker and **Make active** and **Remove** (with a confirmation) per server; **Add server** opens a dialog (paste a link or a sing-box JSON object, optional name, **Test** checks it offline first, **Add**; errors appear inline in plain words and the pasted text is gone once the dialog closes); a "Ready" / "Missing" pill (Missing when none is active); "N of 20 servers". The optional extra proxy (`proxy` outbound) is not managed in v1 (a "later" row) | `GET/POST /servers`, `POST /servers/{id}/activate`, `DELETE /servers/{id}`, `POST /servers/test`. The engine writes the secrets; the pasted text is never shown again and never logged. **Test** answers only a code (`invalid_input`, `unsupported_protocol`), so its sentence is the UI's; **Add** shows the engine's own fixed sentence. A new server is never active by itself: after **Add** the focus lands on its **Make active**. **Make active** while connected restarts the tunnel once (about a second; it may wait up to 30 s after the last restart) and **Remove** of the active server needs a disconnect first (the confirmation says so; the engine answers `busy`). While the app is starting or stopping the server buttons are disabled (the engine answers `busy`) |
| Advanced | Block QUIC (on/off); Allow other DNS query types (`dns.allow_other_query_types`); log level | Each change needs a restart of the engine; the UI says so and offers **Restart now**. Writing `app.yaml` from the UI is open (section 12) |
| About | version of the UI and of `splitter`, licenses of the bundled programs, **Check for updates** (opens the release page in the browser; no automatic network calls) | `GET version` |

Bottom of every tab: **Quit Obhod** (stops the engine and exits).

### 5.5 Help

| Element | Action |
|---|---|
| Short "How it works" (the three paths, same cards as Home) | – |
| **Open logs folder** | opens `Paths.Logs` in the file manager |
| **Show engine log** | read-only viewer with tabs per process: splitter, sing-box, desync engine; **Copy** button | `GET logs?name=` |
| **Export diagnostics…** | saves a zip (version, state, warnings, process states, last log lines) to a place the user picks; host names stripped unless debug is on; nothing is uploaded | **not built:** the UI is not designed (section 12, question 9) |
| **Recovery** | link to the steps in `docs/RECOVERY.md`, shown in-app | |

Built (Stage B, 2026-11-01): Help shows the three path cards, the engine processes with their states (`Snapshot.Processes`, live), the log viewer inline (**Show engine log**, tabs splitter / sing-box / desync engine, **Refresh**, **Copy**; `GET logs?name=`), the versions (UI, `splitter`, sing-box, desync engine, control API; `GET version`), buttons to Settings → Detour server / Advanced / About, and the Windows recovery steps. **Open logs folder** is disabled with a "later" badge (no command opens a folder yet); **Export diagnostics…** is not shown.

## 6. First run

1. **Welcome**: one screen with the reference design (Start + three paths) and **Next**.
2. **Detour server** (**built 2026-11-01 as a card on Home**, not as a separate wizard step; UI side done 2026-11-01): **Start works without a server**. While none is in use, Home shows a calm card, not a gate: "Add a detour server. You can start without one. Blocked sites stay unavailable until you add the server link from your provider." **Add server** opens the paste dialog; after it the card changes to "Pick the server to use" with **Make active** (a new server is never activated by itself). Once the app is on without a server (`snapshot.Tunnel == "none"`) the card reads "Running without a detour server. Blocked sites stay unavailable until you add one." The old **Skip for now** button and its remembered choice are gone: there is nothing to skip. Making a server active while On switches to the full mode after one restart (up to 30 s; Home keeps reading the state until `Tunnel` is `ready`). `tunnel_missing` is left for a server that is in use but unusable and keeps its message and **Open Detour server settings**.
3. **Permission note**: "When you press Start, Windows will ask for permission. This is needed to route your traffic." (Linux: "your system will ask for your password.")
4. **Done**: lands on Home, disconnected. Optional checkbox "Start now".

## 7. Global actions summary

| Action | Where | API / effect |
|---|---|---|
| Connect | Home button, tray, shortcut | `POST connect` |
| Disconnect | Home button, tray, shortcut | `POST disconnect` |
| Check sites again | Home, Sites, tray | `POST check-now` |
| Check one site | Sites side panel | `POST check-now {domains}` |
| Show window | tray click | – |
| View logs | Help | `GET logs?name=` |
| Export diagnostics | Help | local file only |
| Add / make active / remove / test a server | First run, Settings → Detour server | `POST /servers`, `POST /servers/{id}/activate`, `DELETE /servers/{id}`, `POST /servers/test` |
| Restart engine | "no engine" state, Settings → Advanced | UI restarts the `splitter` sidecar |
| Quit | tray, Settings | `POST shutdown`, then the UI exits |

## 8. Keyboard

| Keys | Action |
|---|---|
| Ctrl+Enter | Start / Stop |
| Ctrl+1 … Ctrl+4 | Home, Sites, Activity, Settings |
| Ctrl+F | focus search (Sites) |
| Ctrl+R | Check sites again |
| Esc | close side panel / dialog |
| Ctrl+Q | Quit |

All controls reachable with Tab; the big button has an accessible name that includes the state ("Start, currently off").

## 9. Tray menu

```
● Obhod: On                 (status line, not clickable)
  Stop  /  Start
  Check sites again
  ─────────────
  Open Obhod
  Quit
```

Notifications (OS toasts, can be turned off in General): connected, disconnected after an error, "a detour is needed on this network" (first time a site moves to Blocked on a new network). No toast for routine route changes.

## 10. Errors shown to the user

| Situation (engine) | Sentence | Suggested action |
|---|---|---|
| UAC/pkexec cancelled | "Permission was not given." | Start again |
| `ErrNeedsAdmin` / exit 2 | "The app needs permission to change how traffic is routed." | Start again (asks again) |
| Linux `inet splitter` table missing (exit 2 with commands) | "One-time setup is needed on this computer." | dialog with the exact commands and a **Copy** button; the UI never runs them itself |
| Second instance (exit 3) | "Obhod is already running." | bring the existing window to front |
| Status/control port busy (exit 1) | "Another program is using the app's port." | Restart engine; link to Help |
| Tunnel server missing / unresolved | "The detour server is not set up or cannot be reached." | Settings → Detour server |
| `invalid_input` (server call) | The engine's own sentence, for example "The link has no port." (fixed sentences, never a value from the input) | fix the pasted text |
| `unsupported_protocol` | "This kind of server is not supported." (the engine's sentence names the part: protocol, transport, cipher) | ask the provider for another link |
| `duplicate` | "This server is already in the list." | – |
| `limit_reached` | "The list is full (20 servers). Remove one first." | Settings → Detour server |
| `busy` (server call) | "The engine is busy connecting or disconnecting. Try again in a moment." | retry |
| `too_large` (server call) | "The link or JSON is too large." | check what was pasted |
| Activation could not be applied while connected (`state.error`, `connect_failed` while `state` is `connected`) | "The new server could not be used. The previous server is still in place." (banner on Home and a toast) | pick another server |
| Any other connect failure (`connect_failed` while disconnected) | "Could not connect. See the engine log for details." | Try again; **Show engine log** |
| sing-box gave up (disconnected after a failure) | "The connection stopped unexpectedly." | Try again; **Show engine log** |
| API unreachable | "The engine is not running." | Restart engine |

## 11. Mapping to the control API (M12)

| UI need | Endpoint |
|---|---|
| state + snapshot on open | `GET /api/v1/state` |
| Sites list | `GET /api/v1/routes` |
| process list (Help, Details) | `GET /api/v1/processes` |
| logs | `GET /api/v1/logs?name=` |
| About | `GET /api/v1/version` |
| live updates (state, table diffs, check, traffic tick, final `shutdown`) | `GET /api/v1/events` (SSE) |
| buttons | `POST connect`, `disconnect`, `check-now`, `shutdown` |
| servers list, add, make active, remove, offline test | `GET/POST /api/v1/servers`, `POST /api/v1/servers/{id}/activate`, `DELETE /api/v1/servers/{id}`, `POST /api/v1/servers/test` (milestone 4; no event kind, refetch the list after each call) |

The web frontend calls Tauri commands only; the Rust side is the only API client and holds the token (ADR 0003, decision 4).

## 12. Open questions

1. ~~**Importing the server file.**~~ **Answered (2026-10-31, ADR 0004 milestone 4):** the engine owns secret writing. The UI sends the pasted link or JSON with `POST /servers`; `splitter` stores it under `Paths.Secrets/servers`, copies the active server to the `tunnel` file and never returns a secret. The installer step and a UI-written secrets file are dropped. The `proxy` outbound is not covered in v1.
2. **Editing Advanced settings.** Does the UI write `app.yaml`, or does the API get a config endpoint? Today no endpoint changes config.
3. **"Configure nothing" vs "add a server link".** The reference design promises no setup; a detour server is still required. The "import a server file" half is **answered (2026-10-31, ADR 0004 milestone 4):** the user pastes a link and the engine stores it (question 1). **Go side done (2026-11-01, `api/desktop` decisions 34 to 36):** `connect` with no server in use now succeeds in an Everyday + Picky only mode (sing-box without the tunnel outbound; tunnel-route sites go direct, block and desync work as usual). `state.snapshot.Tunnel` is `"none"` then and each tunnel-route entry of `GET /routes` has `degraded: true`, so the UI can show the banner and mark those sites; activating a server while connected switches to the full mode. **UI side done (2026-11-01):** Start works without a server; Home shows the card "Running without a detour server" (section 5.1, 6) with **Add server**; Sites flags `degraded` rows with **No detour** (section 5.2); the Blocked card on Home has a muted caveat; the Skip for now reminder is removed. The dev fake backend has the scenarios "No detour server" and "Detour server cannot be used". Not verified against the real engine (needs the elevated flow). **Still open (product):** the exact wording (card and flag copy are in `ui/design/copy.ts`), whether the card should also be a one-time prompt after the first successful Start, and whether the engine sends a `state` event when `Tunnel` flips to `ready` after a switch (the UI polls `GET /state` every 2 s for up to 45 s after an activation while On, so it works either way).
4. **Per-domain overrides and adding sites** (PLAN.md §3): **Go side done (2026-11-01):** `GET/POST /api/v1/sites`, `DELETE /api/v1/sites/{domain}` (`api/desktop/API.md`, "Per-site rules"); rules are stored in `Paths.Data/sites.json`, beat the lists for the same domain (a more specific list entry still wins for its own names), apply live without a restart, a rule for an unlisted domain appears in `GET /routes` with `source: "user"`, and deleting it returns the domain to automatic (an unlisted one leaves the table). **UI built 2026-11-01:** the rules live on the Sites page (side panel **Always use…** / **Remove my rule**, toolbar **Add a site**); `source: "user"` reads "Set by you"; after every site call the app refetches `/sites` and `/routes` (a source-only change has no `routes` event); pasted URLs are cleaned in the UI (section 5.2). Not verified against the real engine (needs the elevated flow).
5. **DNS reject notices** (G19): if built, show them under Activity → Warnings.
6. **Service mode** (ADR 0003 decision 3, later): removes the UAC prompt per Start and lets the connection survive Quit; the Quit wording and first-run step 3 change then.
7. Localisation: which languages ship in v1.
8. **Server list details the prototype had to guess and the app kept** (2026-10-31, all open): (a) servers cannot be renamed or edited (the API has no call; remove and add again); (b) **Test** is offline, so the dialog says "Looks fine" but cannot say the server is reachable (no latency, decision 20); (c) the full first-run wizard (Welcome, Detour server, Permission note, Done) is not prototyped, only the Detour card on Home; (d) with several servers, Home shows no name of the active one (could go in the Network line or the subtitle); (e) the address reveal is per session and not remembered; (f) the "Active" wording on the list versus "In use" in the docs; (g) whether a failed switch (`connect_failed` while On) should clear itself, today the banner stays until the next Start because the engine clears the error only then.
9. **Diagnostics export** (2026-11-01, open): the zip is described in section 5.5 but has no designed UI (where it is saved, what the user sees while it is made, whether log lines are redacted by the UI or by the engine). Stage B leaves it out. Needs a decision before a Rust command is added.
10. **Open logs folder** (2026-11-01, open): needs a Tauri command that opens `Paths.Logs` in the file manager, and the engine does not report that path through the API. Shown disabled with "later" for now.
11. **Processes on Activity or only on Help?** (2026-11-01, open): Stage B shows the process list on Help and, under Activity → Warnings, as "Engine processes" because a stopped or failed process is the most useful thing to see next to a warning. If Activity should keep exactly the four tabs of section 5.3 with no process list, remove it there.
12. **Order of the Sites list** (2026-11-01, open): sorted by domain so rows do not jump when a check moves a site. Alternatives: most recently changed first, or grouped by path.
13. **Recovery steps are Windows only** (2026-11-01, open): the Help text is the PowerShell version; on Linux it only points to `docs/RECOVERY.md`. Decide whether Help shows the steps of the running OS.
14. **"Today" traffic after a restart** (2026-11-01, open): the path cards and charts show `Snapshot.Traffic`, and the copy says "today". `api/desktop/API.md` does not say whether the counters restart with the engine or follow the calendar day; check it, and change the copy to "this session" if they restart.
