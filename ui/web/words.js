// User words for engine names (docs/UI.md §2) and error sentences (§10).

export const ROUTES = {
  direct: { word: 'Everyday', long: 'Everyday sites', line: 'Straight through, full speed', dot: 'r-direct' },
  tunnel: { word: 'Blocked', long: 'Blocked sites', line: 'A safe detour, only when needed', dot: 'r-tunnel' },
  desync: { word: 'Picky', long: 'Picky sites', line: 'A quiet fix, right on your device', dot: 'r-desync' },
  proxy: { word: 'Via proxy', long: 'Via proxy', line: 'Through your extra proxy', dot: 'r-proxy' },
  block: { word: 'Blocked by you', long: 'Blocked by you', line: 'Never opened', dot: 'r-block' },
};
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
  crashed: { sentence: 'The connection stopped unexpectedly.', action: 'log' },
  engine_unreachable: { sentence: 'The engine is not running.', action: 'restart-engine' },
};

export function errorInfo(err) {
  if (!err) return null;
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
