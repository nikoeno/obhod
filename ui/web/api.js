// The only module that talks to the engine. In the Tauri app every function
// here becomes an `invoke(...)` call (ADR 0003: the Rust side is the only API
// client and holds the token); the rest of the UI does not change.

const BASE = '/api/v1';

/** The UI knows its own version; the engine's GET /version has none. */
export const UI_VERSION = '0.2.0-mvp';

export class ApiError extends Error {
  constructor(status, code, message, extra) {
    super(message);
    this.status = status;
    this.code = code;
    Object.assign(this, extra || {});
  }
}

async function call(method, path, body) {
  let res;
  try {
    res = await fetch(BASE + path, {
      method,
      headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
      cache: 'no-store',
    });
  } catch {
    throw new ApiError(0, 'engine_unreachable', 'The engine is not running.');
  }
  let data = null;
  try { data = await res.json(); } catch { /* empty or not JSON */ }
  if (!res.ok) {
    const { error, code, ...extra } = data || {};
    throw new ApiError(res.status, code || (res.status === 503 ? 'engine_unreachable' : 'unknown'), error || `HTTP ${res.status}`, extra);
  }
  return data;
}

export const getState = () => call('GET', '/state');
export const getRoutes = () => call('GET', '/routes');
export const getProcesses = () => call('GET', '/processes');
export const getLogs = (name) => call('GET', '/logs?name=' + encodeURIComponent(name));
export const getVersion = () => call('GET', '/version');
export const connect = () => call('POST', '/connect');
export const disconnect = () => call('POST', '/disconnect');
export const checkNow = (domains) => call('POST', '/check-now', domains && domains.length ? { domains } : {});
export const shutdown = () => call('POST', '/shutdown');

// Server management (api/desktop/API.md, milestone 4). The engine never returns a secret; host and port come back in full.
export const getServers = () => call('GET', '/servers');
export const addServer = (input, name) => call('POST', '/servers', name ? { input, name } : { input });
export const activateServer = (id) => call('POST', '/servers/' + encodeURIComponent(id) + '/activate');
export const deleteServer = (id) => call('DELETE', '/servers/' + encodeURIComponent(id));
/** Exactly one of { id } or { input }. Offline: parse plus dry run, nothing is sent to the server. Answers { ok, protocol, code }. */
export const testServer = (body) => call('POST', '/servers/test', body);

// In Tauri: restart the splitter sidecar. In the mock: a mock-only endpoint.
export const restartEngine = () => call('POST', '/mock/restart-engine', {});

// Dev-only (mock): switch the simulated scenario and data flags.
export const getMock = () => call('GET', '/mock/scenario');
export const setMock = (opts) => call('POST', '/mock/scenario', opts);

/**
 * Live updates. handlers: { state, routes, traffic, check, shutdown, open, error }.
 * Returns an unsubscribe function. Reconnects on its own (EventSource retry).
 */
export function subscribe(handlers) {
  const es = new EventSource(BASE + '/events');
  for (const type of ['state', 'routes', 'traffic', 'check', 'shutdown']) {
    es.addEventListener(type, (ev) => {
      let data;
      try { data = JSON.parse(ev.data); } catch { return; }
      if (handlers[type]) handlers[type](data);
    });
  }
  es.onopen = () => handlers.open && handlers.open();
  es.onerror = () => handlers.error && handlers.error();
  return () => es.close();
}
