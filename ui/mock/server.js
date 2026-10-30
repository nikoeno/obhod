'use strict';
// Mock engine for the Obhod desktop UI MVP.
// Simulates the planned M12 control API (/api/v1, see internal/pc/PLAN.md)
// and serves ui/web/ statically. No real actions, no real addresses.
// Run: node ui/mock/server.js   (PORT env, default 8787; binds 127.0.0.1 only)

const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');

const HOST = '127.0.0.1';
const PORT = parsePort(process.env.PORT);
const WEB = path.resolve(__dirname, '..', 'web');
const VERSION = { ui: '0.1.0-mvp', splitter: '0.12.0-mock', singBox: '1.11.0 (mock)', desync: 'winws2 (mock)', api: 'v1' };

function parsePort(v) {
  if (v === undefined || v === '') return 8787;
  const n = Number(v);
  if (!Number.isInteger(n) || n < 0 || n > 65535) {
    console.error(`PORT must be 0..65535, got ${JSON.stringify(v)}`);
    process.exit(1);
  }
  return n;
}

// ---------- fake data ----------

const NETWORKS = ['Home Wi-Fi (Example ISP)', 'Office LAN (Example Telecom)', 'Phone hotspot (Example Mobile)'];

const SEED = {
  direct: ['yandex.ru', 'vk.com', 'mail.ru', 'gosuslugi.ru', 'ozon.ru', 'wildberries.ru', 'sberbank.ru', 'avito.ru',
    'github.com', 'wikipedia.org', 'stackoverflow.com', 'microsoft.com', 'apple.com', 'steampowered.com', 'kinopoisk.ru',
    'habr.com', 'dzen.ru', 'tbank.ru', 'hh.ru', '2gis.ru'],
  tunnel: ['instagram.com', 'facebook.com', 'x.com', 'linkedin.com', 'medium.com', 'soundcloud.com', 'patreon.com', 'proton.me', 'signal.org'],
  desync: ['youtube.com', 'googlevideo.com', 'ytimg.com', 'discord.com', 'discord.gg', 'discordapp.net', 'twitch.tv'],
  proxy: ['spotify.com', 'netflix.com'],
  block: ['doubleclick.net'],
};
const MAIN_ROUTES = ['direct', 'tunnel', 'desync'];

const now = () => Date.now();
const iso = (t) => new Date(t).toISOString();
const rnd = (a, b) => a + Math.floor(Math.random() * (b - a + 1));
const pick = (arr) => arr[Math.floor(Math.random() * arr.length)];

const domains = new Map(); // domain -> {route, since, trafficToday, history:[{From,To,At}], lastCheck}
function seedDomains() {
  domains.clear();
  const t = now();
  for (const [route, list] of Object.entries(SEED)) {
    for (const d of list) {
      const since = t - rnd(5, 600) * 60 * 1000;
      const base = route === 'direct' ? rnd(2, 400) : route === 'desync' ? rnd(50, 900) : route === 'block' ? 0 : rnd(1, 120);
      domains.set(d, { route, since, trafficToday: base * 1024 * 1024 + rnd(0, 1023) * 1024, history: [], lastCheck: t - rnd(1, 9) * 60 * 1000 });
    }
  }
}
seedDomains();

const mock = { scenario: 'normal', warnings: true, extras: true, learning: false };
const eng = {
  state: 'disconnected',
  error: null,
  checking: false,
  network: NETWORKS[0],
  networks: [{ label: NETWORKS[0], since: iso(now() - 40 * 60 * 1000) }],
  lastCheck: now() - 5 * 60 * 1000,
  nextCheck: now() + 25 * 60 * 1000,
  recent: [],
  upload: 0,
  download: 0,
  connections: 0,
  restarts: { splitter: 0, 'sing-box': 0, desync: 0 },
  shutdown: false,
};
const timers = new Set();
function later(ms, fn) {
  const t = setTimeout(() => { timers.delete(t); fn(); }, ms);
  timers.add(t);
  return t;
}
function cancelTimers() { for (const t of timers) clearTimeout(t); timers.clear(); }

// Seed a few past changes so Activity is not empty.
(function seedRecent() {
  const t = now();
  const samples = [['youtube.com', 'direct', 'desync', 14], ['instagram.com', 'direct', 'tunnel', 32], ['medium.com', 'desync', 'tunnel', 51]];
  for (const [d, from, to, ago] of samples) {
    const c = { Domain: d, From: from, To: to, At: iso(t - ago * 60 * 1000) };
    eng.recent.push(c);
    const rec = domains.get(d);
    if (rec) { rec.history.push({ From: from, To: to, At: c.At }); rec.since = t - ago * 60 * 1000; }
  }
})();

function visibleDomains() {
  if (mock.learning) return [];
  const out = [];
  for (const [domain, r] of domains) {
    if (!mock.extras && (r.route === 'proxy' || r.route === 'block')) continue;
    out.push([domain, r]);
  }
  return out;
}

function warnings() {
  const w = [];
  if (!mock.warnings) return w;
  w.push('IPv6 is turned off on this network, so a few sites may open more slowly.');
  if (eng.state === 'connected') w.push('Blocked sites are slower than usual: the detour server answers with a delay.');
  return w;
}

function processes() {
  const on = eng.state === 'connected' || eng.state === 'disconnecting';
  const starting = eng.state === 'connecting';
  return [
    { Name: 'splitter', State: 'running', Restarts: eng.restarts.splitter },
    { Name: 'sing-box', State: on ? 'running' : starting ? 'starting' : 'stopped', Restarts: eng.restarts['sing-box'] },
    { Name: 'desync', State: on ? 'running' : starting ? 'starting' : 'stopped', Restarts: eng.restarts.desync },
  ];
}

function snapshot() {
  const Routes = {};
  const Traffic = {};
  for (const [, r] of visibleDomains()) {
    Routes[r.route] = (Routes[r.route] || 0) + 1;
    Traffic[r.route] = (Traffic[r.route] || 0) + r.trafficToday;
  }
  const total = Object.values(Traffic).reduce((a, b) => a + b, 0);
  const Savings = total > 0 ? 1 - (Traffic.tunnel || 0) / total : 0;
  return {
    Version: VERSION.splitter,
    State: eng.state,
    Network: mock.learning ? '' : eng.network,
    LastCheck: iso(eng.lastCheck),
    NextCheck: iso(eng.nextCheck),
    Routes,
    Traffic,
    Savings: Math.round(Savings * 1000) / 1000,
    Processes: processes(),
    Warnings: warnings(),
    Recent: mock.learning ? [] : eng.recent.slice(-30),
  };
}

function statePayload() {
  return { state: eng.state, error: eng.error, checking: eng.checking, snapshot: snapshot(), networks: eng.networks };
}

// ---------- SSE ----------

const clients = new Set();
let eventId = 0;
function emit(type, data) {
  const msg = `id: ${++eventId}\nevent: ${type}\ndata: ${JSON.stringify(data)}\n\n`;
  for (const res of clients) res.write(msg);
}
setInterval(() => { for (const res of clients) res.write(': ping\n\n'); }, 15000).unref();

function setState(state, error) {
  eng.state = state;
  if (error !== undefined) eng.error = error;
  emit('state', statePayload());
}

// ---------- logs ----------

const logs = { splitter: [], 'sing-box': [], desync: [] };
function log(name, level, msg) {
  const ring = logs[name];
  ring.push(`${new Date().toISOString()} ${level.padEnd(5)} ${msg}`);
  if (ring.length > 300) ring.splice(0, ring.length - 300);
}
log('splitter', 'INFO', `splitter ${VERSION.splitter} started (mock), control API on 127.0.0.1`);
log('splitter', 'INFO', `network identified: "${eng.network}"`);
log('splitter', 'INFO', `route table loaded: ${domains.size} domains`);

// ---------- actions ----------

function err(code, error, extra) { return Object.assign({ error, code }, extra || {}); }

const SETUP_COMMANDS = [
  'sudo nft add table inet splitter',
  'sudo nft flush table inet splitter',
  "sudo nft add chain inet splitter post '{ type filter hook postrouting priority 101; }'",
  'sudo nft add rule inet splitter post oifname "eth0" tcp dport { 80, 443 } meta mark and 0x40000000 == 0 ct original packets 1-12 queue num 200 bypass',
  'sudo ip rule add fwmark 0x1 lookup main priority 5',
];

function connect() {
  if (eng.state === 'connected' || eng.state === 'connecting') return [200, { ok: true, state: eng.state }];
  if (eng.state === 'disconnecting') return [409, err('busy', 'The app is still stopping. Try again in a moment.')];
  cancelTimers();
  setState('connecting', null);
  log('splitter', 'INFO', 'connect requested; asking for elevation');
  const scenario = mock.scenario;
  later(1500, () => {
    if (scenario === 'permission-denied') {
      log('splitter', 'WARN', 'elevation was cancelled by the user');
      return setState('disconnected', err('permission_denied', 'Permission was not given.'));
    }
    if (scenario === 'needs-setup') {
      log('splitter', 'ERROR', 'nft table "inet splitter" is missing; refusing to start (exit 2)');
      return setState('disconnected', err('needs_setup', 'One-time setup is needed on this computer.', { commands: SETUP_COMMANDS }));
    }
    log('sing-box', 'INFO', 'sing-box started, TUN up');
    log('sing-box', 'INFO', 'outbound tunnel: handshake ok');
    log('desync', 'INFO', 'desync engine started (mock)');
    log('splitter', 'INFO', 'connected');
    eng.connectedAt = now();
    setState('connected', null);
    if (scenario === 'crash') {
      later(rnd(4000, 6000), () => {
        if (eng.state !== 'connected') return;
        eng.restarts['sing-box'] += 3;
        log('sing-box', 'ERROR', 'tun: read: device not ready');
        log('splitter', 'ERROR', 'sing-box exited 3 times in 30s; giving up');
        log('splitter', 'INFO', 'disconnected after a failure');
        setState('disconnected', err('crashed', 'The connection stopped unexpectedly.'));
      });
    }
  });
  return [202, { ok: true, state: 'connecting' }];
}

function disconnect() {
  if (eng.state === 'disconnected' || eng.state === 'disconnecting') return [200, { ok: true, state: eng.state }];
  cancelTimers();
  setState('disconnecting', null);
  log('splitter', 'INFO', 'disconnect requested');
  later(800, () => {
    log('sing-box', 'INFO', 'sing-box stopped');
    log('desync', 'INFO', 'desync engine stopped');
    log('splitter', 'INFO', 'disconnected');
    eng.connections = 0;
    setState('disconnected', null);
  });
  return [202, { ok: true, state: 'disconnecting' }];
}

function checkNow(body) {
  let targets = null;
  if (body && body.domains !== undefined) {
    if (!Array.isArray(body.domains) || body.domains.some((d) => typeof d !== 'string')) {
      return [400, err('bad_request', 'domains must be a list of names.')];
    }
    targets = body.domains.filter((d) => domains.has(d));
    if (targets.length === 0) return [404, err('unknown_domain', 'None of these sites are in the table.')];
  }
  if (eng.checking) return [202, { ok: true, checking: true }];
  eng.checking = true;
  emit('check', { running: true, domains: targets });
  log('splitter', 'INFO', targets ? `check-now: ${targets.length} site(s)` : 'check-now: all sites');
  later(2000, () => {
    const pool = (targets || visibleDomains().map(([d]) => d)).filter((d) => MAIN_ROUTES.includes(domains.get(d).route));
    let moved;
    if (targets) moved = pool.filter(() => Math.random() < 0.6);
    else moved = shuffle(pool).slice(0, rnd(1, 2));
    const t = now();
    const changes = [];
    for (const d of moved) {
      const r = domains.get(d);
      const to = pick(MAIN_ROUTES.filter((x) => x !== r.route));
      const c = { Domain: d, From: r.route, To: to, At: iso(t) };
      r.history.push({ From: r.route, To: to, At: c.At });
      r.route = to;
      r.since = t;
      changes.push(c);
      eng.recent.push(c);
      log('splitter', 'INFO', `route change: ${d} ${c.From} -> ${to}`);
    }
    for (const d of targets || visibleDomains().map(([x]) => x)) domains.get(d).lastCheck = t;
    if (eng.recent.length > 100) eng.recent.splice(0, eng.recent.length - 100);
    eng.lastCheck = t;
    eng.nextCheck = t + 30 * 60 * 1000;
    eng.checking = false;
    if (changes.length) emit('routes', { changes });
    emit('check', { running: false, domains: targets, changed: changes.length });
    emit('state', statePayload());
  });
  return [202, { ok: true, checking: true }];
}

function shuffle(a) {
  const b = a.slice();
  for (let i = b.length - 1; i > 0; i--) { const j = Math.floor(Math.random() * (i + 1)); [b[i], b[j]] = [b[j], b[i]]; }
  return b;
}

// Traffic tick every 2 s while connected.
setInterval(() => {
  if (eng.state !== 'connected') return;
  const byRoute = {};
  const vis = visibleDomains();
  for (const [, r] of vis) {
    if (r.route === 'block') continue;
    const w = r.route === 'desync' ? 3 : r.route === 'direct' ? 2 : 1;
    if (Math.random() < 0.35) {
      const add = rnd(5, 400) * 1024 * w;
      r.trafficToday += add;
      byRoute[r.route] = (byRoute[r.route] || 0) + add;
    }
  }
  const sum = Object.values(byRoute).reduce((a, b) => a + b, 0);
  const up = Math.round(sum * 0.08);
  eng.upload += up;
  eng.download += sum - up;
  eng.connections = Math.max(3, Math.min(180, eng.connections + rnd(-6, 8) || 20));
  const snap = snapshot();
  emit('traffic', {
    uploadTotal: eng.upload, downloadTotal: eng.download, connections: eng.connections,
    delta: byRoute, Traffic: snap.Traffic, Savings: snap.Savings, At: iso(now()),
  });
}, 2000).unref();

// ---------- HTTP ----------

const MIME = { '.html': 'text/html; charset=utf-8', '.css': 'text/css; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.svg': 'image/svg+xml', '.json': 'application/json', '.ico': 'image/x-icon' };

function baseHeaders(res) {
  res.setHeader('Content-Security-Policy', "default-src 'self'");
  res.setHeader('X-Content-Type-Options', 'nosniff');
  res.setHeader('Referrer-Policy', 'no-referrer');
  res.setHeader('Cache-Control', 'no-store');
}
function json(res, status, body) {
  res.statusCode = status;
  res.setHeader('Content-Type', 'application/json; charset=utf-8');
  res.end(JSON.stringify(body));
}
function hostOK(req) {
  const h = String(req.headers.host || '');
  const m = /^(127\.0\.0\.1|localhost|\[::1\])(?::(\d+))?$/i.exec(h);
  return !!m && (!m[2] || Number(m[2]) === server.address().port);
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    let size = 0;
    const chunks = [];
    req.on('data', (c) => {
      size += c.length;
      if (size > 64 * 1024) { reject(Object.assign(new Error('too large'), { status: 413 })); req.destroy(); return; }
      chunks.push(c);
    });
    req.on('end', () => {
      const s = Buffer.concat(chunks).toString('utf8').trim();
      if (!s) return resolve({});
      try { resolve(JSON.parse(s)); } catch { reject(Object.assign(new Error('bad json'), { status: 400 })); }
    });
    req.on('error', reject);
  });
}

function serveStatic(req, res, pathname) {
  if (req.method !== 'GET' && req.method !== 'HEAD') { res.setHeader('Allow', 'GET'); return json(res, 405, err('method_not_allowed', 'Method not allowed.')); }
  let rel = decodeURIComponent(pathname);
  if (rel === '/' || rel === '') rel = '/index.html';
  const file = path.resolve(WEB, '.' + rel);
  if (file !== WEB && !file.startsWith(WEB + path.sep)) return json(res, 403, err('forbidden', 'Forbidden.'));
  fs.readFile(file, (e, data) => {
    if (e) return json(res, 404, err('not_found', 'Not found.'));
    res.statusCode = 200;
    res.setHeader('Content-Type', MIME[path.extname(file).toLowerCase()] || 'application/octet-stream');
    res.end(req.method === 'HEAD' ? undefined : data);
  });
}

const SCENARIOS = ['normal', 'permission-denied', 'needs-setup', 'crash', 'no-engine'];

function applyMock(body) {
  if (body.scenario !== undefined) {
    if (!SCENARIOS.includes(body.scenario)) return [400, err('bad_request', `scenario must be one of ${SCENARIOS.join(', ')}`)];
    mock.scenario = body.scenario;
    if (mock.scenario === 'no-engine') {
      cancelTimers();
      eng.state = 'disconnected';
      eng.checking = false;
      for (const c of clients) c.end();
      clients.clear();
    }
  }
  for (const k of ['warnings', 'extras', 'learning']) if (typeof body[k] === 'boolean') mock[k] = body[k];
  if (body.network === 'next') {
    const i = NETWORKS.indexOf(eng.network);
    eng.network = NETWORKS[(i + 1) % NETWORKS.length];
    eng.networks.push({ label: eng.network, since: iso(now()) });
    log('splitter', 'INFO', `network changed: "${eng.network}"`);
  }
  if (body.reset === true) { seedDomains(); eng.recent = []; eng.upload = eng.download = 0; }
  if (mock.scenario !== 'no-engine') emit('state', statePayload());
  return [200, { ok: true, mock: { ...mock } }];
}

async function handleApi(req, res, url) {
  const p = url.pathname.slice('/api/v1/'.length);
  const method = req.method;

  // Mock-only controls stay reachable in every scenario.
  if (p === 'mock/scenario') {
    if (method === 'GET') {
      const q = Object.fromEntries(url.searchParams);
      const body = {};
      if (q.scenario) body.scenario = q.scenario;
      for (const k of ['warnings', 'extras', 'learning']) if (q[k] === 'true' || q[k] === 'false') body[k] = q[k] === 'true';
      if (q.network) body.network = q.network;
      if (Object.keys(body).length === 0) return json(res, 200, { mock: { ...mock }, scenarios: SCENARIOS });
      const [s, b] = applyMock(body);
      return json(res, s, b);
    }
    if (method === 'POST') { const [s, b] = applyMock(await readBody(req)); return json(res, s, b); }
    res.setHeader('Allow', 'GET, POST');
    return json(res, 405, err('method_not_allowed', 'Method not allowed.'));
  }
  if (p === 'mock/restart-engine' && method === 'POST') {
    // Stands in for the Tauri side restarting the splitter sidecar.
    cancelTimers();
    mock.scenario = 'normal';
    eng.state = 'disconnected';
    eng.error = null;
    eng.checking = false;
    eng.restarts.splitter += 1;
    log('splitter', 'INFO', 'splitter restarted by the UI (mock)');
    emit('state', statePayload());
    return json(res, 200, { ok: true });
  }

  if (mock.scenario === 'no-engine' || eng.shutdown) {
    return json(res, 503, err('engine_unreachable', 'The engine is not running.'));
  }

  const GET = {
    state: () => [200, statePayload()],
    routes: () => [200, visibleDomains().map(([domain, r]) => ({
      domain, route: r.route, since: iso(r.since), trafficToday: r.trafficToday,
      lastCheck: iso(r.lastCheck), history: r.history.slice(-10),
    })).sort((a, b) => a.domain.localeCompare(b.domain))],
    processes: () => [200, processes()],
    version: () => [200, VERSION],
    logs: () => {
      const name = url.searchParams.get('name');
      if (!Object.hasOwn(logs, name || '')) return [400, err('bad_request', 'name must be splitter, sing-box or desync.')];
      return [200, { name, lines: logs[name].slice(-200) }];
    },
  };
  const POST = { connect, disconnect, 'check-now': checkNow, shutdown: () => {
    log('splitter', 'INFO', 'shutdown requested by the UI (mock: acknowledged only)');
    return [200, { ok: true }];
  } };

  if (p === 'events') {
    if (method !== 'GET') { res.setHeader('Allow', 'GET'); return json(res, 405, err('method_not_allowed', 'Method not allowed.')); }
    res.writeHead(200, { 'Content-Type': 'text/event-stream; charset=utf-8', Connection: 'keep-alive' });
    res.write('retry: 2000\n\n');
    res.write(`event: state\ndata: ${JSON.stringify(statePayload())}\n\n`);
    clients.add(res);
    req.on('close', () => clients.delete(res));
    return;
  }
  if (Object.hasOwn(GET, p)) {
    if (method !== 'GET') { res.setHeader('Allow', 'GET'); return json(res, 405, err('method_not_allowed', 'Method not allowed.')); }
    const [s, b] = GET[p]();
    return json(res, s, b);
  }
  if (Object.hasOwn(POST, p)) {
    if (method !== 'POST') { res.setHeader('Allow', 'POST'); return json(res, 405, err('method_not_allowed', 'Method not allowed.')); }
    const body = await readBody(req);
    const [s, b] = POST[p](body);
    return json(res, s, b);
  }
  return json(res, 404, err('not_found', 'Unknown endpoint.'));
}

const server = http.createServer(async (req, res) => {
  baseHeaders(res);
  if (!hostOK(req)) return json(res, 403, err('forbidden', 'Forbidden host.'));
  const url = new URL(req.url, 'http://127.0.0.1');
  try {
    // ?scenario=... on any request switches the simulation (handy for quick tests).
    const qs = url.searchParams.get('scenario');
    if (qs && SCENARIOS.includes(qs) && qs !== mock.scenario && !url.pathname.startsWith('/api/v1/mock/')) applyMock({ scenario: qs });
    if (url.pathname.startsWith('/api/v1/')) return await handleApi(req, res, url);
    return serveStatic(req, res, url.pathname);
  } catch (e) {
    if (!res.headersSent) return json(res, e.status || 500, err(e.status === 400 ? 'bad_request' : e.status === 413 ? 'too_large' : 'internal', e.status ? 'Bad request.' : 'Internal error.'));
    res.end();
  }
});

server.listen(PORT, HOST, () => {
  const { port } = server.address();
  console.log(`Obhod UI mock running: http://127.0.0.1:${port}/`);
  console.log('Dev panel in the UI: Ctrl+Shift+D. Scenarios: ' + SCENARIOS.join(', '));
});

function stop() { cancelTimers(); for (const c of clients) c.end(); server.close(() => process.exit(0)); setTimeout(() => process.exit(0), 500).unref(); }
process.on('SIGINT', stop);
process.on('SIGTERM', stop);
