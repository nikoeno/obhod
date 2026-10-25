# Desktop UI: pages, tabs and user actions

Status: draft (2026-10-25). Describes the Tauri desktop app from ADR 0003 (`docs/adr/0003-desktop-ui-and-packaging.md`). Nothing here is built yet; the only UI that exists today is the read-only status page (`internal/pc/status`). Every action below maps to a control API call planned in M12 (`internal/pc/PLAN.md`); actions marked **later** need a separate decision first.

Visual reference: `docs/reference-one-button.svg` and `docs/one-button.html` (one large accent button, neutral cards, one purple accent).

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

What the app decided for each site. Read-only in v1.

Tabs:

| Tab | Shows |
|---|---|
| All | every domain in the current table |
| Everyday / Blocked / Picky | one route each (Proxy, Blocked by you appear only when non-empty) |

List columns: site, path (user word + colour dot), since (when it moved there), traffic today.

User actions:

| Action | Result | API |
|---|---|---|
| Search box | filters the list by substring, locally | – |
| Switch tab | filters by route | – |
| Click a site | side panel: current path, history of route changes for it, last check time | `routes`, events |
| **Check this site again** (side panel) | re-probes that one domain | `POST check-now {domains:[…]}` |
| **Check all again** (toolbar) | re-probes everything | `POST check-now` |
| **Always use …** (side panel) | **later**: per-domain override (PLAN.md §3) | – |
| **Add a site** | **later**: list editing (PLAN.md §3) | – |

Empty state (no network identified yet): "Still learning this network. Sites will appear in a few seconds."

### 5.3 Activity

What happened recently. Tabs:

| Tab | Shows | Source |
|---|---|---|
| Changes | timeline of route changes: "youtube.com moved from Everyday to Picky · 14:02" | `Snapshot.Recent`, events (`route.Diff`) |
| Traffic | live upload/download totals and connection count, a small chart per route for the session | `/api/traffic`, traffic tick event |
| Network | current network label, last and next check, history of networks seen this session (labels only) | `Snapshot.Network`, `LastCheck`, `NextCheck` |
| Warnings | full list of current warnings with a plain explanation and a suggested action each | `Snapshot.Warnings` |

User actions: switch tabs, click a change to open that site in Sites, **Clear view** (clears only the on-screen timeline, not any data).

### 5.4 Settings

Few settings by design. Tabs:

| Tab | Settings / actions | Notes |
|---|---|---|
| General | Start the app when I sign in (on/off); Connect automatically when the app starts (on/off); Close button hides to tray (on/off); Language | UI-side settings, stored by the Tauri app |
| Detour server | Shows whether a tunnel is configured ("Ready" / "Missing"); **Import server file…**; **Remove**; optional extra proxy: **Import proxy file…** | The file goes into `Paths.Secrets`; its contents are never shown again, never logged. Needs an API or installer step — open (section 12) |
| Advanced | Block QUIC (on/off); Allow other DNS query types (`dns.allow_other_query_types`); log level | Each change needs a restart of the engine; the UI says so and offers **Restart now**. Writing `app.yaml` from the UI is open (section 12) |
| About | version of the UI and of `splitter`, licenses of the bundled programs, **Check for updates** (opens the release page in the browser; no automatic network calls) | `GET version` |

Bottom of every tab: **Quit Obhod** (stops the engine and exits).

### 5.5 Help

| Element | Action |
|---|---|
| Short "How it works" (the three paths, same cards as Home) | – |
| **Open logs folder** | opens `Paths.Logs` in the file manager |
| **Show engine log** | read-only viewer with tabs per process: splitter, sing-box, desync engine; **Copy** button | `GET logs?name=` |
| **Export diagnostics…** | saves a zip (version, state, warnings, process states, last log lines) to a place the user picks; host names stripped unless debug is on; nothing is uploaded | |
| **Recovery** | link to the steps in `docs/RECOVERY.md`, shown in-app | |

## 6. First run

1. **Welcome**: one screen with the reference design (Start + three paths) and **Next**.
2. **Detour server**: "To open blocked sites the app needs a server file from your provider." **Import file…** or **Skip for now** (then Blocked sites stay unavailable and Home shows a warning).
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
| Import server/proxy file | First run, Settings | open (section 12) |
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
| live updates (state, table diffs, process events, traffic tick) | `GET /api/v1/events` (SSE) |
| buttons | `POST connect`, `disconnect`, `check-now`, `shutdown` |

The web frontend calls Tauri commands only; the Rust side is the only API client and holds the token (ADR 0003, decision 4).

## 12. Open questions

1. **Importing the server file.** The tunnel secret must be in `Paths.Secrets`, which is owner-only and may belong to the elevated side. Options: the UI writes it to the per-user secrets dir before connect; a new `POST secrets` action (API change); or the installer. Needs a decision.
2. **Editing Advanced settings.** Does the UI write `app.yaml`, or does the API get a config endpoint? Today no endpoint changes config.
3. **"Configure nothing" vs "import a server file".** The reference design promises no setup; a tunnel file is still required. Is "Skip for now" (Everyday + Picky only) an acceptable default?
4. **Per-domain overrides and adding sites** (PLAN.md §3): which page they live on once decided (proposed: Sites side panel).
5. **DNS reject notices** (G19): if built, show them under Activity → Warnings.
6. **Service mode** (ADR 0003 decision 3, later): removes the UAC prompt per Start and lets the connection survive Quit; the Quit wording and first-run step 3 change then.
7. Localisation: which languages ship in v1.
