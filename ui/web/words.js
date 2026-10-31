// User words for engine names (docs/UI.md §2) and error sentences (§10).

export const ROUTES = {
  direct: { word: 'Everyday', long: 'Everyday sites', line: 'Straight through, full speed', dot: 'r-direct' },
  tunnel: { word: 'Blocked', long: 'Blocked sites', line: 'A safe detour, only when needed', dot: 'r-tunnel' },
  desync: { word: 'Picky', long: 'Picky sites', line: 'A quiet fix, right on your device', dot: 'r-desync' },
  proxy: { word: 'Via proxy', long: 'Via proxy', line: 'Through your extra proxy', dot: 'r-proxy' },
  block: { word: 'Blocked by you', long: 'Blocked by you', line: 'Never opened', dot: 'r-block' },
};
/** Route.source from GET /routes: how the engine decided (route.Source). */
export const SOURCES = { checked: 'Checked on this network', static: 'From a fixed list', default: 'Safe default until checked' };
export const sourceWord = (s) => SOURCES[s] || '–';
/** The engine says "unknown network" until it has identified the network. */
export const isLearning = (label) => !label || label === 'unknown network';
export const networkLabel = (label) => (isLearning(label) ? 'Still learning this network' : label);
export const MAIN = ['direct', 'tunnel', 'desync'];
export const EXTRA = ['proxy', 'block'];
export const routeWord = (r) => (ROUTES[r] ? ROUTES[r].word : r);

/** action: what the one suggested action is. */
export const ERRORS = {
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

/** Shown when switching servers while On could not be applied (state.error connect_failed while connected). */
export const SWITCH_FAILED = 'The new server could not be used. The previous server is still in place.';

/** state: the connection state the error arrived with. A connect_failed while connected means a failed server switch. */
export function errorInfo(err, state) {
  if (!err) return null;
  if (err.code === 'connect_failed' && state === 'connected') return { code: err.code, sentence: SWITCH_FAILED, action: 'detour' };
  const known = ERRORS[err.code];
  return { code: err.code, sentence: known ? known.sentence : (err.error || err.message || 'Something went wrong.'), action: known ? known.action : 'retry', commands: err.commands };
}

// Plain explanation and suggestion for warning texts (Activity → Warnings).
export function explainWarning(text) {
  const t = text.toLowerCase();
  if (t.includes('ipv6')) return { why: 'Your network does not offer IPv6, so the app uses IPv4 only. Nothing is broken.', todo: 'No action needed. If sites feel slow, use Check sites again.' };
  if (t.includes('detour server') || t.includes('slower')) return { why: 'The detour server takes longer than usual to answer. Only Blocked sites are affected.', todo: 'Wait a few minutes, or check Settings → Detour server.' };
  return { why: 'The engine reported this while running.', todo: 'If it keeps showing, open Help → Show engine log.' };
}

// ---------- detour servers (docs/UI.md §5.4, §6, §10) ----------

export const PROTOCOL_NAMES = { vless: 'VLESS', vmess: 'VMess', trojan: 'Trojan', shadowsocks: 'Shadowsocks', hysteria2: 'Hysteria2', tuic: 'TUIC' };
export const protocolName = (p) => PROTOCOL_NAMES[p] || p || 'Unknown';
export const MAX_SERVERS = 20;

export const SERVER_TEXT = {
  lead: 'Needed to open Blocked sites. The link is kept private and never shown again.',
  ready: 'Ready',
  missing: 'Missing',
  inUse: 'Active',
  none: 'No servers yet. Add the link your provider gave you.',
  hidden: 'Address hidden',
  showAddress: 'Show address',
  hideAddress: 'Hide address',
  makeActive: 'Make active',
  remove: 'Remove',
  add: 'Add server',
  addTitle: 'Add a detour server',
  inputLabel: 'Server link or JSON',
  inputHelp: 'Paste the link from your provider (vless, vmess, trojan, ss, hy2, tuic) or one sing-box server object in JSON. It is checked on this computer and never shown again.',
  nameLabel: 'Name (optional)',
  nameHelp: 'Up to 60 characters. Without a name the app picks one.',
  test: 'Test',
  cancel: 'Cancel',
  pasteFirst: 'Paste the link or JSON from your provider first.',
  testing: 'Checking…',
  adding: 'Adding…',
  testOk: (p) => `Looks fine: a ${protocolName(p)} server. This check runs on this computer only; the connection itself is not tried.`,
  testInvalid: 'This does not look like a valid server. Check that you pasted the whole link.',
  testUnsupported: 'This kind of server is not supported.',
  added: (name) => `${name} added. Make it active to use it.`,
  nowActive: (name) => `${name} is now in use.`,
  switching: (name) => `Switching to ${name}. The connection restarts once.`,
  removed: (name) => `${name} removed.`,
  switchHint: 'Switching while On restarts the connection once, about a second.',
  busyHint: 'The app is starting or stopping. Server changes are available again in a moment.',
  listFull: 'The list is full (20 servers). Remove one first.',
  removeTitle: (name) => `Remove ${name}?`,
  removeBody: 'It is deleted from this computer and cannot be shown again. To use it later, add the link again.',
  removeActiveTitle: 'Stop first',
  removeActiveBody: 'This server is in use. Stop the app first (Stop on Home), then remove it.',
  proxyTitle: 'Extra proxy (optional)',
  proxyHint: 'Sites on the "Via proxy" path go through it. Not available yet.',
  count: (n) => `${n} of ${MAX_SERVERS} servers`,
};

export const FIRST_RUN = {
  title: 'Add a detour server to start',
  text: 'To open blocked sites the app needs the server link from your provider.',
  skip: 'Skip for now',
  pickTitle: 'Pick the server to use',
  pickText: 'A server is saved, but none is in use yet.',
  skippedBanner: 'Blocked sites stay unavailable until you add a detour server.',
  skippedAction: 'Add server',
};

const BY_CODE = {
  duplicate: 'This server is already in the list.',
  limit_reached: SERVER_TEXT.listFull,
  too_large: 'The link or JSON is too large.',
  not_found: 'This server is no longer in the list.',
  engine_unreachable: 'The engine is not running.',
};

/**
 * Plain sentence for a failed server call. invalid_input and unsupported_protocol carry the engine's own
 * fixed sentence (never a value from the input), so those are shown as they come.
 * ctx: 'add' | 'remove' | 'activate'.
 */
export function serverErrorText(e, ctx) {
  const code = e && e.code;
  if (code === 'invalid_input') return e.message || 'The link or JSON is not valid.';
  if (code === 'unsupported_protocol') return e.message || 'This kind of server is not supported.';
  if (code === 'busy') {
    return ctx === 'remove'
      ? 'This server cannot be removed now. Stop the app first, or wait until it has finished starting or stopping.'
      : 'The engine is busy connecting or disconnecting. Try again in a moment.';
  }
  if (code === 'bad_request') return ctx === 'add' ? 'The name must be 1 to 60 characters.' : 'The engine did not accept this request.';
  if (BY_CODE[code]) return BY_CODE[code];
  return 'Something went wrong.';
}

/** Result of POST servers/test: only a code comes back, so the sentence is the UI's. */
export function testResultText(r) {
  if (r.ok) return SERVER_TEXT.testOk(r.protocol);
  if (r.code === 'unsupported_protocol') return SERVER_TEXT.testUnsupported;
  return SERVER_TEXT.testInvalid;
}
