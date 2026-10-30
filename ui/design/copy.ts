// Obhod user-facing copy from the MVP (ui/web/words.js and the pages), as typed data.
// English only today; keep keys stable so these become the i18n source (e.g. vue-i18n).
// Rule: no network words on main surfaces. Engine names (direct, tunnel, ...) appear only in Details.

import type { Route } from './tokens';

export type EngineState = 'disconnected' | 'connecting' | 'connected' | 'disconnecting';

/** User words for engine routes (docs/UI.md §2). Order: main paths, then extras shown only when non-empty. */
export const routes: Record<Route, { word: string; long: string; line: string }> = {
  direct: { word: 'Everyday', long: 'Everyday sites', line: 'Straight through, full speed' },
  tunnel: { word: 'Blocked', long: 'Blocked sites', line: 'A safe detour, only when needed' },
  desync: { word: 'Picky', long: 'Picky sites', line: 'A quiet fix, right on your device' },
  proxy: { word: 'Via proxy', long: 'Via proxy', line: 'Through your extra proxy' },
  block: { word: 'Blocked by you', long: 'Blocked by you', line: 'Never opened' },
};
export const mainRoutes: Route[] = ['direct', 'tunnel', 'desync'];
export const extraRoutes: Route[] = ['proxy', 'block'];

/**
 * Big button per state (docs/UI.md §4). `name` is the accessible name (state included).
 * `look` maps to the button styles in README.md "Big button". {savings} is a percentage like "78%".
 */
export const bigButton = {
  disconnected: { look: 'idle', label: 'Start', name: 'Start, currently off', sub: 'Off. Your traffic goes as usual.' },
  connecting: { look: 'busy', label: 'Starting…', name: 'Starting, please wait', sub: 'Asking for permission and starting.' },
  connected: { look: 'on', label: 'Stop', name: 'Stop, currently on', sub: 'On. {savings} of your traffic stays off the detour.' },
  disconnecting: { look: 'busy-dim', label: 'Stopping…', name: 'Stopping, please wait', sub: '' },
  error: { look: 'idle', label: 'Try again', name: 'Try again, currently off: {sentence}', sub: '{sentence}' },
  noEngine: { look: 'off', label: '–', name: 'Start, unavailable: the engine is not running', sub: 'The engine is not running.' },
} as const;

/** Suggested action kinds for errors; each error has exactly one. */
export type ErrorAction = 'retry' | 'setup' | 'none' | 'restart-engine' | 'detour' | 'log';

/** Error codes from the control API → sentence and the one suggested action (docs/UI.md §10). */
export const errors: Record<string, { sentence: string; action: ErrorAction }> = {
  permission_denied: { sentence: 'Permission was not given.', action: 'retry' },
  needs_admin: { sentence: 'The app needs permission to change how traffic is routed.', action: 'retry' },
  needs_setup: { sentence: 'One-time setup is needed on this computer.', action: 'setup' },
  already_running: { sentence: 'Obhod is already running.', action: 'none' },
  port_busy: { sentence: "Another program is using the app's port.", action: 'restart-engine' },
  tunnel_missing: { sentence: 'The detour server is not set up or cannot be reached.', action: 'detour' },
  crashed: { sentence: 'The connection stopped unexpectedly.', action: 'log' },
  engine_unreachable: { sentence: 'The engine is not running.', action: 'restart-engine' },
};
export const unknownError = 'Something went wrong.';

export const actions = {
  restartEngine: 'Restart engine',
  showLog: 'Show engine log',
  showSetup: 'Show setup commands',
  openDetour: 'Open Detour server settings',
  checkAll: 'Check all again',
  checkSite: 'Check this site again',
  checking: 'Checking…',
  checkingToast: 'Checking all sites again…',
  details: 'Details',
  copy: 'Copy',
  copyCommands: 'Copy commands',
  close: 'Close',
  quit: 'Quit Obhod',
  restartNow: 'Restart now',
  clearView: 'Clear view',
  importServer: 'Import server file…',
  importProxy: 'Import proxy file…',
  remove: 'Remove',
  exportDiagnostics: 'Export diagnostics…',
  openLogs: 'Open logs folder',
  checkUpdates: 'Check for updates',
  later: 'later',
  refresh: 'Refresh',
  loading: 'Loading…',
} as const;

export const pages = {
  nav: { home: 'Home', sites: 'Sites', activity: 'Activity', settings: 'Settings', help: 'Help' },
  home: {
    networkLine: 'Network: {label}',
    learning: 'Still learning this network',
    savings: '{savings} of your traffic stays off the detour',
    cardCount: { one: '{n} site', other: '{n} sites' },
    cardTraffic: '{bytes} today',
    cardNeverOpened: 'never opened',
  },
  sites: {
    title: 'Sites',
    lead: 'What the app decided for each site. It updates by itself.',
    search: 'Search sites',
    tabAll: 'All',
    columns: { site: 'Site', path: 'Path', since: 'Since', today: 'Today' },
    empty: 'Still learning this network. Sites will appear in a few seconds.',
    count: { one: '{n} site', other: '{n} sites' },
    countMatching: ' matching “{query}”',
    noMatch: 'No sites match “{query}”.',
    noneOnPath: 'No sites on this path yet.',
    panel: { path: 'Path', since: 'Since', lastCheck: 'Last check', history: 'History', alwaysUse: 'Always use…' },
    alwaysUseLater: 'later: choosing a path per site is not available yet',
    addSite: 'Add a site',
  },
  activity: {
    title: 'Activity',
    tabs: { changes: 'Changes', traffic: 'Traffic', network: 'Network', warnings: 'Warnings' },
    changesLead: 'Route changes the app made. Click one to open that site.',
    change: '{site} moved from {from} to {to} · {time}',
    noChanges: 'No changes this session.',
    noChangesShown: 'No changes to show.',
    noNetworksYet: 'None yet.',
    trafficLead: 'Live numbers appear while the app is on. Totals below are for today.',
    networkPrivacy: 'Networks are shown by name only. The app never shows or stores your public address.',
    networksSeen: 'Networks seen this session',
    nextCheck: 'Next check',
    noWarnings: 'No warnings. Everything looks fine.',
    whatToDo: 'What to do: ',
  },
  settings: {
    title: 'Settings',
    tabs: { general: 'General', detour: 'Detour server', advanced: 'Advanced', about: 'About' },
    general: {
      autostart: 'Start the app when I sign in',
      autoconnect: 'Connect automatically when the app starts',
      hideToTray: { label: 'Close button hides to tray', hint: 'Quit from the tray menu or below.' },
      notifications: { label: 'Show notifications', hint: 'Connected, stopped after an error, and when a detour is first needed on a network.' },
      language: 'Language',
    },
    detour: {
      lead: 'Needed to open Blocked sites. The file is kept private and never shown again.',
      ready: 'Ready',
      missing: 'Missing',
      proxy: 'Extra proxy (optional)',
      proxyHint: 'Sites on the "Via proxy" path go through it.',
    },
    advanced: {
      blockQuic: { label: 'Block QUIC', hint: 'Sites fall back to regular HTTPS, which the app handles better.' },
      otherDns: { label: 'Allow other DNS query types', hint: 'Technical: dns.allow_other_query_types. Off by default.' },
      logLevel: 'Log level',
      restartNeeded: 'Restart needed. The engine reads these settings when it starts.',
    },
    about: { app: 'App (UI)', engine: 'Engine (splitter)', bundled: 'Bundled programs', noAutoUpdate: 'The app makes no automatic network calls to check for updates.' },
    quitHint: 'Stops the engine and closes the app.',
  },
  help: {
    title: 'Help',
    howItWorks: 'How it works',
    howItWorksText: 'Press Start once. The app checks each site and picks one of three paths for it by itself. You do not need to set anything up.',
    logs: 'Logs and diagnostics',
    recovery: 'If the internet stops working',
    setupDialog: 'Run these commands once in a terminal, then press Start again. The app never runs them itself.',
  },
  quit: { done: 'Obhod has quit.', openAgain: 'Open again' },
} as const;

/** Tray menu (docs/UI.md §9). */
export const tray = {
  status: { on: 'Obhod: On', off: 'Obhod: Off' },
  start: 'Start',
  stop: 'Stop',
  check: 'Check sites again',
  open: 'Open Obhod',
  quit: 'Quit',
} as const;

/** First run (docs/UI.md §6); not built in the MVP. */
export const firstRun = {
  detour: 'To open blocked sites the app needs a server file from your provider.',
  skip: 'Skip for now',
  permissionWindows: 'When you press Start, Windows will ask for permission. This is needed to route your traffic.',
  permissionLinux: 'When you press Start, your system will ask for your password. This is needed to route your traffic.',
  startNow: 'Start now',
} as const;

/** Process names in the log viewer. */
export const processes = { splitter: 'splitter', 'sing-box': 'sing-box', desync: 'desync engine' } as const;
