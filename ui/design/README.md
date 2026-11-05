# Obhod design kit (from the MVP)

Everything the desktop app uses to look and behave the way it does, in a form the app (Tauri + Vue + TypeScript) imports directly. It was extracted from the clickable MVP that stood in `ui/web/` until Stage B (2026-11-01); that MVP is deleted and lives in git history only. Page behaviour, actions and API mapping stay in `docs/UI.md`; this folder is the visual and copy contract.

| File | What | Use in production |
|---|---|---|
| `tokens.json` | All tokens in W3C DTCG format (light value + `$extensions.dark`) | Source of truth; feed Style Dictionary or similar if you add a pipeline |
| `tokens.css` | The same tokens as CSS variables, light/dark via `prefers-color-scheme`, overridable with `<html data-theme>` | `import './design/tokens.css'` once in `main.ts` |
| `tokens.ts` | Typed tokens for places CSS cannot reach (SVG/canvas charts, tray icon, tests) | `import { routeColors } from './design/tokens'` |
| `copy.ts` | Every user-facing sentence: route words, button states, error sentences, page text, tray | Seed for the i18n messages (vue-i18n); keep the keys |
| `icons/` | Nav icons (24×24, stroke 1.8, `currentColor`), power icon, the Home "fan", app icon | Inline as Vue SFC icons or via `vite-svg-loader` |

Source of truth: the tokens are `tokens.json` (DTCG), `tokens.css` and `tokens.ts`, which must agree with each other; component styling is `ui/src/styles/app.css`, built only from those tokens. If a component value in `app.css` and this README disagree, fix the one that is wrong and keep both in step.

## 1. Principles

- **One accent.** Purple `#534ab7` (dark `#7f77dd`) is the only brand colour. Route colours exist only to tell the five paths apart (dots, chart bars); they are never used for buttons or large surfaces. The Blocked (tunnel) route reuses the accent.
- **Neutral cards on a plain background.** Warm greys (`card`, `border`, `muted`). No shadows except the focus ring, the big button halo and floating panels.
- **System font, no external assets.** No web fonts, no CDNs, no remote images. Everything ships in the bundle.
- **No network words on main surfaces** (`copy.ts` routes). Engine names appear only in Details and in Advanced hints (styled as `.tech`).
- **Honest state.** UI state only mirrors the engine's `state`; it never invents one.
- **Privacy.** Never show a public IP; a network is shown by its label only.

## 2. Theme

Light and dark follow the OS (`prefers-color-scheme`). `tokens.css` also accepts `data-theme="light"|"dark"` on `<html>` for a future in-app switch. Every colour token has both values; never hardcode a hex in a component. `color-scheme` is set so native controls (scrollbars, `<select>`) match.

## 3. Type

- Base 15px, line height 1.45, `--font-sans`. Monospace `--font-mono` at 0.86rem for code, logs and commands.
- Headings: h1 1.5rem (margin 0 0 4px), h2 1.1rem (24px 0 8px), h3 1rem (18px 0 6px). Paragraph margin 6px 0.
- Weights: 400 body, 600 for labels, active nav/tab, pills, card titles, 700 for brand and big button.
- Numbers that change live use `font-variant-numeric: tabular-nums`.

## 4. Layout

- **Shell:** left nav 200px wide, sticky, full height, right border. Main content padding 28px 32px 48px, max width 1100px. Home content max 820px, centered.
- **Nav:** brand row (14px accent dot + "Obhod", 700), items Home, Sites, Activity, Settings, then a separator and Help pinned to the bottom. Item: 8px 10px padding, radius 8px, 20px icon + label with a 10px gap; hover `card`; current page `accent-soft` background, `accent` text, 600. Under the list a status line (0.82rem, muted) with an 8px dot: `border` idle, `accent` connected, `r-block` engine down.
- **Below 640px** (`max-width: 639px`): the nav becomes a fixed bottom tab bar (icon above a 0.72rem label, brand and status hidden), main padding 18px 16px 96px, cards one column, fan hidden, toasts lifted to bottom 80px.
- **Below 900px** (`max-width: 899px`): the Sites side panel stacks above the list at full width.
- Dialog width `min(680px, 100vw - 32px)`, max height `100vh - 48px`.

## 5. Components

### Big button (Home)
168px circle, no border, label 1.35rem/700 under a 34px power icon (`icons/power.svg`), 6px gap, margin 24px 0 18px.

| Look (`copy.ts` `bigButton.*.look`) | Fill | Text | Shadow |
|---|---|---|---|
| `idle` (Start / Try again) | `accent` | `on-accent` | halo 20px `halo` |
| `on` (Stop) | `bg` | `accent` | inset 8px `accent` ring + halo 20px |
| `busy` (Starting…) | `accent` | `on-accent` | pulse: halo 12px ↔ 26px, 1.4s ease-in-out infinite, cursor `progress` |
| `busy-dim` (Stopping…) | `card` | `muted` | 20px `card` ring, no pulse |
| `off` (no engine) | `card` | `muted` | 20px `card` ring, `not-allowed` |

Hover (enabled): `scale(1.02)` over 120ms; fill and shadow transition 200ms. Focus-visible keeps the halo and adds a 3px `bg` gap + 3px `accent` ring outside it. Disabled states keep full opacity (the look already shows it). The accessible name always includes the state ("Stop, currently on").

Under it: subtitle (1.05rem, centered, `aria-live="polite"`, reserves one line), then at most one suggested-action button for errors or no engine.

### Fan
`icons/fan.svg`: three curves from the button down to the three path cards, 44px tall, stretched to the card row width, stroke `border` 1.5px non-scaling. Decorative (`aria-hidden`). Hidden on mobile and when the engine is down.

### Path cards
Grid of three equal columns, gap 12px (one column on mobile). Card: `card` fill, transparent 1px border, radius 12px, padding 14px 16px, column with 6px gap. Head: route dot + title (600). Line: 0.88rem muted (`routes.*.line`). Numbers at the bottom: count bold + "N MB today" muted, 0.92rem. Clickable cards are `<button>`s: hover border `accent`. Extra cards (Via proxy, Blocked by you) are small (padding 10px 14px, no line), auto-fill min 180px, shown only when their count is above zero.

### Route dot
10px circle, colour `--r-<route>`, 6px right margin. Always next to the user word, never alone (colour is not the only signal).

### Buttons
- Default: `card` fill, 1px `border`, radius 10px, padding 8px 14px, line height 1.2. Hover border `accent`.
- Primary: `accent` fill and border, `on-accent` text. Danger: default with `r-block` text.
- Small: padding 5px 10px, 0.88rem. Disabled: opacity 0.55, `not-allowed`.
- Link button: no chrome, `accent`, underlined. Plain link: `text` colour, underline on hover.
- Icon button: 1.4rem glyph, muted → text on hover, radius 6px.
- "later" badge: 0.78rem muted, 1px dashed `border`, pill, padding 1px 8px. Marks features that are designed but not built.

### Inputs
Select and search: `bg` fill, 1px `border`, radius 10px, padding 7px 10px, inherit font. Search is 260px wide (shrinks).

### Switch
42×24 track, pill, `border` when off, `accent` when on; 18px knob in `bg`, 3px inset, slides 18px; 150ms. A setting row is the switch + label + optional muted hint, padding 12px 0, bottom border.

### Tabs
Row with a bottom border, 4px gap, horizontal scroll when needed. Tab: no chrome, muted, padding 8px 12px, 2px transparent underline; selected `accent` text and underline, 600. Optional count chip (0.78rem, `card` fill, muted, pill) and route dot. Proper `role="tablist"`/`tab`/`tabpanel` with arrow-key navigation.

### Pills
0.82rem/600, pill radius, padding 2px 10px. `ok` (`ok-bg`/`ok-text`, e.g. "Ready") and `warn` (`warn-bg`/`warn-text`, e.g. "Missing").

### Banner
`warn-bg`/`warn-text`, radius 12px, padding 10px 14px, 16px below. Round 22px "!" icon (2px current-colour border), text, then a bold link-button **Details**. Shows only the first warning.

### Sites table and side panel
Table 0.93rem; header cells muted 0.82rem/600 with a `border` bottom; rows with a `card` bottom line, hover `card`, selected `accent-soft`; numbers right-aligned. Side panel 320px, `card`, radius 12px, padding 14px 16px, sticky at 16px; domain heading breaks anywhere. Facts as a two-column `<dl>` (muted terms, gap 6px 14px). History as a plain list, 0.9rem. Empty states: muted, 24px vertical padding.

### Activity
Timeline items with a `card` bottom line. Stats: three columns (one on mobile) with 1.4rem tabular numbers. Charts: auto-fill min 260px, `card` panels radius 12px, 64px tall bar SVGs, bars filled with the route colour, axis line `border`.

### Code and logs
`pre.code`: `card` fill, radius 8px, padding 10px 12px, horizontal scroll, no wrap. Log viewer: tabs per process (splitter, sing-box, desync engine), max height 340px, **Copy** and **Refresh**.

### Dialog
Native `<dialog>`: `bg`, 1px `border`, radius 12px, padding 20px 22px, backdrop `rgba(0,0,0,.45)`. Actions right-aligned, 8px gap, 16px above. Esc closes; focus returns to the opener.

### Toast
Bottom-right stack (16px from edges, 8px gap): inverted (`text` fill, `bg` text), radius 10px, padding 10px 14px, 0.92rem. Visible 2.8s, fades 300ms, removed at 3.2s. For confirmations only ("Copied", "Checking all sites again…"); errors go in the page, never only in a toast.

### Form fields and messages
Text input and textarea (`.field`): `bg` fill, 1px `border`, radius 10px, padding 8px 10px, full width; the paste field is monospace (`.mono`, 0.86rem, 5 rows, resizable). Invalid: `aria-invalid="true"` and a `r-block` border. Each field has a visible `<label>` (600) and a muted 0.85rem help line wired with `aria-describedby`. The inline message under a form (`.form-msg`, `role="status"`, `aria-live="polite"`) is empty-hidden; `err` uses `warn-bg`/`warn-text`, `ok` uses `ok-bg`/`ok-text`; radius 10px, padding 8px 12px. Errors stay in the dialog, never only in a toast.

### Server list and first-run card
Server row (`.server`): 12px vertical padding, bottom `border`, name 600 with an `ok` pill "Active", then a muted line "Protocol · Address hidden" (the address, when revealed, is a tech tag). Actions: link button "Show address" (`aria-expanded`), small "Make active", small danger "Remove"; each has an `aria-label` that names the server. Row wraps on narrow widths. The first-run card (`.setup-card`): `card` fill, radius 12px, padding 16px 18px, h2 + one muted line + a primary and a default button; it sits above the big button and is replaced by the warning banner after "Skip for now".

### Tech tag
Engine names in Details: 0.78rem muted, 1px `border`, radius 4px, padding 0 4px.

## 6. Icons

Nav icons are 24×24 outline icons, `stroke="currentColor"`, width 1.8, round caps and joins, rendered at 20px. Power icon: stroke 2.2, rendered at 34px. App icon: accent circle with a white power symbol (`icons/app-icon.svg`); the tray needs grey, accent and "grey with a dot" variants (docs/UI.md §4), which are not drawn yet.

## 7. Motion

Durations in `tokens.*` (`press` 120ms, `toggle` 150ms, `state` 200ms, `fade` 300ms, `pulse` 1.4s). Under `prefers-reduced-motion: reduce` the pulse stops and all transitions are off.

## 8. Accessibility

- Focus-visible ring on every control: `box-shadow: 0 0 0 3px var(--bg), 0 0 0 5px var(--accent)`, radius 6px; no outline on mouse focus.
- Skip link "Skip to content" as the first element, shown on focus (accent pill top-left).
- Live regions: the big button subtitle and the nav status line are `aria-live="polite"`.
- Decorative SVGs have `aria-hidden="true" focusable="false"`. Cards and the network line get full `aria-label`s ("Everyday sites: 31 sites, 1.2 GB today. Show in Sites").
- Colour is never the only signal (route dot + word; pill + word).
- Keyboard: Ctrl+Enter Start/Stop, Ctrl+1…4 pages, Ctrl+F search (Sites), Ctrl+R check again, Esc closes panel/dialog, Ctrl+Q quit. Mark them with `aria-keyshortcuts`.

## 9. Formatting

| Value | Rule | Examples |
|---|---|---|
| Bytes | 1024-based, B/KB/MB/GB/TB; one decimal below 100, none from 100 or for B; ≤0 or invalid → "0 B" | `0 B`, `512 B`, `1.5 MB`, `120 GB` |
| Past time | <45 s "just now", <60 min "N min ago", <24 h "N h ago", else "N d ago"; invalid "never" | `5 min ago` |
| Future time | ≤0 "now", <60 min "in N min", else "in N h"; invalid "–" | `in 12 min` |
| Clock time | locale `HH:MM` | `14:02` |
| Percent | `Math.round(x * 100)%` from a 0–1 fraction | `78%` |
| Counts | "1 site" / "N sites" (use plural rules once localised) | |
| Placeholder | en dash "–" for missing values | |

Ellipsis is the single character "…" in labels ("Starting…", "Export diagnostics…").

## 10. Porting notes (Vue + Tauri)

- Ported (Stage A and B): the engine client is the Rust side; the frontend calls Tauri `invoke` commands and listens to one Tauri event (`ui/src/backend/`; the Rust side holds the token, ADR 0003). Payload shapes are `ui/src/backend/types.ts` and `api/desktop/API.md`.
- The store is `ui/src/store.ts` (Vue reactivity, no Pinia): state, snapshot, routes, live traffic series, checking flag.
- UI settings are in `localStorage` for now; they move to the Rust side (autostart, tray behaviour need it anyway).
- Not designed yet: tray icon variants, first-run screens, OS notifications, diagnostics export UI, Russian copy (the language list shows "Русский (later)").
