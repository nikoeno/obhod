// App state mirrored from the engine. The UI never invents connection states:
// everything here comes from api.js responses and events.
import * as api from './api.js';
import { toast } from './dom.js';
import { routeWord } from './words.js';

const SERIES_LEN = 30;

export const S = {
  loaded: false,
  engineDown: false,
  state: 'disconnected',
  error: null, // {error, code, commands?} from the engine, cleared on the next Start
  checking: false,
  checkingDomains: null,
  snapshot: null,
  networks: [],
  routes: [],
  traffic: { uploadTotal: 0, downloadTotal: 0, connections: 0 },
  series: { direct: [], tunnel: [], desync: [], proxy: [] }, // bytes per 2 s tick, this session
  quit: false,
};

const listeners = new Set();
export function onChange(fn) { listeners.add(fn); return () => listeners.delete(fn); }
function notify(topic, extra) { for (const fn of listeners) fn(topic, extra); }

function applyState(p) {
  const prevState = S.state;
  const prevErr = S.error && S.error.code;
  S.state = p.state;
  S.error = p.error || null;
  S.checking = !!p.checking;
  S.snapshot = p.snapshot;
  S.networks = p.networks || [];
  S.engineDown = false;
  S.loaded = true;
  const newError = S.error && (S.error.code !== prevErr || prevState !== S.state) ? S.error : null;
  notify('state', { newError, prevState });
}

function setEngineDown() {
  if (S.engineDown && S.loaded) return;
  S.engineDown = true;
  S.loaded = true;
  S.checking = false;
  notify('state', {});
}

export async function refresh() {
  try {
    const [st, routes] = await Promise.all([api.getState(), api.getRoutes()]);
    S.routes = routes;
    applyState(st);
    notify('routes', {});
    return true;
  } catch (e) {
    if (e.code === 'engine_unreachable') setEngineDown();
    return false;
  }
}

async function refreshRoutes() {
  try { S.routes = await api.getRoutes(); notify('routes', {}); } catch { /* next state event fixes it */ }
}

// ---------- live events ----------

let unsub = null;
let ticks = 0;
let retryTimer = null;

export function startLive() {
  if (unsub) unsub();
  unsub = api.subscribe({
    state: applyState,
    routes: (d) => { notify('changes', d); refreshRoutes(); },
    traffic: (t) => {
      S.traffic = { uploadTotal: t.uploadTotal, downloadTotal: t.downloadTotal, connections: t.connections };
      if (S.snapshot) { S.snapshot.Traffic = t.Traffic; S.snapshot.Savings = t.Savings; }
      for (const r of Object.keys(S.series)) {
        S.series[r].push(t.delta[r] || 0);
        if (S.series[r].length > SERIES_LEN) S.series[r].shift();
      }
      notify('traffic', t);
      if (++ticks % 3 === 0) refreshRoutes(); // per-site "today" numbers
    },
    check: (c) => {
      S.checking = c.running;
      S.checkingDomains = c.running ? c.domains : null;
      if (!c.running && c.domains && c.domains.length === 1) {
        const d = c.domains[0];
        const r = S.routes.find((x) => x.domain === d);
        if (!c.changed) toast(`${d} checked: no change${r ? ` (still ${routeWord(r.route)})` : ''}.`);
      } else if (!c.running) {
        toast(c.changed ? `Check finished: ${c.changed} site${c.changed > 1 ? 's' : ''} moved.` : 'Check finished: no changes.');
      }
      notify('check', c);
    },
    error: () => {
      // The stream dropped. If the engine is gone, show it and keep probing.
      if (unsub) { unsub(); unsub = null; }
      clearTimeout(retryTimer);
      retryTimer = setTimeout(async () => {
        if (S.quit) return;
        if (await refresh()) startLive(); else retryLater();
      }, 1000);
    },
  });
}

function retryLater() {
  clearTimeout(retryTimer);
  retryTimer = setTimeout(async () => {
    if (S.quit) return;
    if (await refresh()) startLive(); else retryLater();
  }, 3000);
}

// ---------- actions ----------

function actionFailed(e) {
  if (e.code === 'engine_unreachable') { setEngineDown(); return; }
  toast(e.message || 'Something went wrong.');
}

export async function toggleConnection() {
  if (S.engineDown) return;
  try {
    if (S.state === 'connected') await api.disconnect();
    else if (S.state === 'disconnected') await api.connect();
  } catch (e) { actionFailed(e); }
}

export async function checkNow(domains) {
  if (S.engineDown) return;
  try {
    S.checking = true;
    S.checkingDomains = domains || null;
    notify('check', { running: true, domains });
    await api.checkNow(domains);
  } catch (e) { S.checking = false; notify('check', { running: false }); actionFailed(e); }
}

export async function restartEngine() {
  try {
    await api.restartEngine();
    toast('Engine restarted.');
    if (await refresh()) startLive();
  } catch (e) { actionFailed(e); }
}

/** Advanced settings: stop and start again so the engine picks up the change. */
export async function restartConnection() {
  if (S.state !== 'connected') return false;
  await api.disconnect();
  await waitFor((s) => s === 'disconnected', 5000);
  await api.connect();
  return true;
}

function waitFor(pred, ms) {
  return new Promise((resolve) => {
    if (pred(S.state)) return resolve(true);
    const off = onChange((topic) => { if (topic === 'state' && pred(S.state)) { off(); clearTimeout(t); resolve(true); } });
    const t = setTimeout(() => { off(); resolve(false); }, ms);
  });
}

export async function quit() {
  try { await api.shutdown(); } catch { /* quitting anyway */ }
  S.quit = true;
  if (unsub) { unsub(); unsub = null; }
  clearTimeout(retryTimer);
  notify('quit', {});
}

/** Sum of the visible route counts. */
export function routeCount(r) { return (S.snapshot && S.snapshot.Routes && S.snapshot.Routes[r]) || 0; }
export function routeTraffic(r) { return (S.snapshot && S.snapshot.Traffic && S.snapshot.Traffic[r]) || 0; }
