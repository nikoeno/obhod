// Obhod user-facing copy from the clickable MVP (deleted in Stage B, 2026-11-01), as typed data.
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
  connect_failed: { sentence: 'Could not connect. See the engine log for details.', action: 'log' },
  crashed: { sentence: 'The connection stopped unexpectedly.', action: 'log' },
  engine_unreachable: { sentence: 'The engine is not running.', action: 'restart-engine' },
};
export const unknownError = 'Something went wrong.';

/** connect_failed that arrives while the state is `connected` means a server switch that could not be applied. */
export const switchFailed = 'The new server could not be used. The previous server is still in place.';

/**
 * Failed server calls (docs/UI.md §10). invalid_input and unsupported_protocol show the engine's own fixed sentence
 * (never a value from the input); the others are the UI's.
 */
export const serverErrors = {
  duplicate: 'This server is already in the list.',
  limit_reached: 'The list is full (20 servers). Remove one first.',
  too_large: 'The link or JSON is too large.',
  not_found: 'This server is no longer in the list.',
  busy: 'The engine is busy connecting or disconnecting. Try again in a moment.',
  busyRemove: 'This server cannot be removed now. Stop the app first, or wait until it has finished starting or stopping.',
  badName: 'The name must be 1 to 60 characters.',
  invalidFallback: 'The link or JSON is not valid.',
  unsupportedFallback: 'This kind of server is not supported.',
} as const;

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
  addServer: 'Add server',
  makeActive: 'Make active',
  remove: 'Remove',
  cancel: 'Cancel',
  test: 'Test',
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
    startDetails: {
      summary: 'Details',
      hint: 'What the engine reported. It stays on this computer; copy it if you ask for help.',
      label: 'Engine details',
      copied: 'Details copied.',
      copyFailed: 'Could not copy. Select the text instead.',
    },
  },
  sites: {
    title: 'Sites',
    lead: 'What the app decided for each site. It updates by itself.',
    search: 'Search sites',
    tabAll: 'All',
    columns: { site: 'Site', path: 'Path', since: 'Since', decided: 'Decided' },
    empty: 'Still learning this network. Sites will appear in a few seconds.',
    count: { one: '{n} site', other: '{n} sites' },
    countMatching: ' matching “{query}”',
    noMatch: 'No sites match “{query}”.',
    noneOnPath: 'No sites on this path yet.',
    panel: { path: 'Path', since: 'Since', decided: 'Decided', lastCheck: 'Last check (all sites)', history: 'History', alwaysUse: 'Always use…', removeRule: 'Remove my rule', ruleNote: 'You chose this path, so the app does not check this site.' },
    sources: { checked: 'Checked on this network', static: 'From a fixed list', default: 'Safe default until checked', user: 'Set by you' },
    addSite: 'Add a site',
    /** The rule dialog (Add a site, Always use…). The four choices reuse `routes` (word and line). */
    rule: {
      addTitle: 'Add a site',
      useTitle: 'Always use a path for {site}',
      siteLabel: 'Site',
      siteHelp: 'Only the name, for example example.com. A pasted address is cleaned up for you.',
      pathLabel: 'Always use this path',
      add: 'Add site',
      save: 'Save',
      saving: 'Saving…',
      added: '{site} will always use {path}.',
      changed: '{site} will now always use {path}.',
      removed: 'Your rule for {site} is gone. The app decides again.',
    },
    ruleErrors: {
      empty: 'Enter a site name, for example example.com.',
      address: 'Enter the site name, not an IP address.',
      notName: 'That is not a plain site name. Enter only the name, for example example.com.',
      limit: 'You have reached the limit of 500 rules. Remove one first.',
      notFound: 'There is no rule for this site any more.',
      rejected: 'The engine did not accept this rule.',
    },
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
      lead: 'Needed to open Blocked sites. The link is kept private and never shown again.',
      ready: 'Ready',
      missing: 'Missing',
      active: 'Active',
      none: 'No servers yet. Add the link your provider gave you.',
      addressHidden: 'Address hidden',
      showAddress: 'Show address',
      hideAddress: 'Hide address',
      count: '{n} of 20 servers',
      switchHint: 'Switching while On restarts the connection once, about a second.',
      busyHint: 'The app is starting or stopping. Server changes are available again in a moment.',
      switching: 'Switching to {name}. The connection restarts once.',
      nowActive: '{name} is now in use.',
      added: '{name} added. Make it active to use it.',
      removed: '{name} removed.',
      add: {
        title: 'Add a detour server',
        inputLabel: 'Server link or JSON',
        inputHelp: 'Paste the link from your provider (vless, vmess, trojan, ss, hy2, tuic) or one sing-box server object in JSON. It is checked on this computer and never shown again.',
        nameLabel: 'Name (optional)',
        nameHelp: 'Up to 60 characters. Without a name the app picks one.',
        pasteFirst: 'Paste the link or JSON from your provider first.',
        testing: 'Checking…',
        adding: 'Adding…',
        testOk: 'Looks fine: a {protocol} server. This check runs on this computer only; the connection itself is not tried.',
        testInvalid: 'This does not look like a valid server. Check that you pasted the whole link.',
        testUnsupported: 'This kind of server is not supported.',
      },
      remove: {
        title: 'Remove {name}?',
        body: 'It is deleted from this computer and cannot be shown again. To use it later, add the link again.',
        activeTitle: 'Stop first',
        activeBody: 'This server is in use. Stop the app first (Stop on Home), then remove it.',
      },
      proxy: 'Extra proxy (optional)',
      proxyHint: 'Sites on the "Via proxy" path go through it. Not available yet.',
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

/** First run (docs/UI.md §6). Only the detour-server card on Home is built in the MVP. */
export const firstRun = {
  /** Home card while no server exists (built in the MVP; the Welcome and Permission screens are not). */
  detourTitle: 'Add a detour server to start',
  detour: 'To open blocked sites the app needs the server link from your provider.',
  skip: 'Skip for now',
  /** Home banner after "Skip for now". */
  skippedBanner: 'Blocked sites stay unavailable until you add a detour server.',
  /** A server is saved but none is active (a new server is never activated by itself). */
  pickTitle: 'Pick the server to use',
  pickText: 'A server is saved, but none is in use yet.',
  permissionWindows: 'When you press Start, Windows will ask for permission. This is needed to route your traffic.',
  permissionLinux: 'When you press Start, your system will ask for your password. This is needed to route your traffic.',
  startNow: 'Start now',
} as const;

/** Process names in the log viewer. */
export const processes = { splitter: 'splitter', 'sing-box': 'sing-box', desync: 'desync engine' } as const;
