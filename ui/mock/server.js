'use strict';
// Mock engine for the Obhod desktop UI MVP.
// Simulates the implemented control API (/api/v1, see api/desktop/API.md, M12 and
// the "Server management" section, milestone 4) and serves ui/web/ statically.
// No real actions, no real addresses, no real keys. The mock keeps only
// name/protocol/host/port of a server; the pasted link or JSON is discarded.
// Run: node ui/mock/server.js   (PORT env, default 8787; binds 127.0.0.1 only)

const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');

const HOST = '127.0.0.1';
const PORT = parsePort(process.env.PORT);
const WEB = path.resolve(__dirname, '..', 'web');
// Same shape as the real GET /version. singBox and desync are "" (decision 5); the UI knows its own version.
const VERSION = { splitter: 'splitter 0.12.0-mock', singBox: '', desync: '', api: 'v1' };

const MAX_BODY = 64 * 1024;
const MAX_INPUT = 32 * 1024;
const MAX_SERVERS = 20;
const MAX_NAME = 60;
const MAX_STREAMS = 4;
const ZERO_TIME = '0001-01-01T00:00:00Z';

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

// domain -> {route, since, traffic (internal only, never sent per domain), history:[{From,To,At}]}
const domains = new Map();
function seedDomains() {
  domains.clear();
  const t = now();
  for (const [route, list] of Object.entries(SEED)) {
    for (const d of list) {
      const since = t - rnd(5, 600) * 60 * 1000;
      const base = route === 'direct' ? rnd(2, 400) : route === 'desync' ? rnd(50, 900) : route === 'block' ? 0 : rnd(1, 120);
      domains.set(d, { route, since, traffic: base * 1024 * 1024 + rnd(0, 1023) * 1024, history: [] });
    }
  }
}
seedDomains();

const mock = { scenario: 'normal', warnings: true, extras: true, learning: false, reloadFails: false };
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
  const on = eng.state === 'connected';
  const starting = eng.state === 'connecting';
  const stopping = eng.state === 'disconnecting';
  const st = on ? 'running' : starting ? 'starting' : stopping ? 'stopping' : 'stopped';
  return [
    { Name: 'splitter', State: 'running', Restarts: eng.restarts.splitter },
    { Name: 'sing-box', State: st, Restarts: eng.restarts['sing-box'] },
    { Name: 'desync', State: st, Restarts: eng.restarts.desync },
  ];
}

function snapshot() {
  const Routes = {};
  const Traffic = {};
  for (const [, r] of visibleDomains()) {
    Routes[r.route] = (Routes[r.route] || 0) + 1;
    Traffic[r.route] = (Traffic[r.route] || 0) + r.traffic;
  }
  const total = Object.values(Traffic).reduce((a, b) => a + b, 0);
  const Savings = total > 0 ? 1 - (Traffic.tunnel || 0) / total : 0;
  return {
    Version: VERSION.splitter,
    State: eng.state,
    Network: mock.learning ? 'unknown network' : eng.network,
    LastCheck: mock.learning ? ZERO_TIME : iso(eng.lastCheck),
    NextCheck: mock.learning ? ZERO_TIME : iso(eng.nextCheck),
    Routes,
    Traffic,
    Savings: Math.round(Savings * 1000) / 1000,
    Processes: processes(),
    Warnings: warnings(),
    Recent: mock.learning ? [] : eng.recent.slice(-20),
  };
}

function statePayload() {
  return { state: eng.state, error: eng.error, checking: eng.checking, snapshot: snapshot(), networks: eng.networks.slice(-10) };
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

// ---------- logs (never contain server input) ----------

const logs = { splitter: [], 'sing-box': [], desync: [] };
function log(name, level, msg) {
  const ring = logs[name];
  ring.push(`${new Date().toISOString()} ${level.padEnd(5)} ${msg}`);
  if (ring.length > 200) ring.splice(0, ring.length - 200);
}
log('splitter', 'INFO', `${VERSION.splitter} started (mock), control API on 127.0.0.1`);
log('splitter', 'INFO', `network identified: "${eng.network}"`);
log('splitter', 'INFO', `route table loaded: ${domains.size} domains`);

// ---------- errors ----------

class ApiErr extends Error {
  constructor(status, code, message, extra) { super(message); this.status = status; this.code = code; this.extra = extra; }
}
function err(code, error, extra) { return Object.assign({ error, code }, extra || {}); }
const fail = (status, code, message) => { throw new ApiErr(status, code, message); };
const STATUS = { invalid_input: 422, unsupported_protocol: 422, duplicate: 409, limit_reached: 409, too_large: 413, not_found: 404, busy: 409 };
// A parse failure of the server input; the message is always a fixed sentence, never a value.
class InputFail extends Error {
  constructor(code, message) { super(message); this.code = code; this.protocol = ''; }
}
const bad = (m) => { throw new InputFail('invalid_input', m); };
const unsupported = (m) => { throw new InputFail('unsupported_protocol', m); };

// ---------- servers (name, protocol, host, port only) ----------

const SALT = crypto.randomBytes(16); // duplicate detection hashes the credential; the hash never leaves memory
const credHash = (protocol, host, port, cred) =>
  crypto.createHash('sha256').update(SALT).update(`${protocol}|${host.toLowerCase()}|${port}|${cred}`).digest('hex');

let servers = []; // {id, name, protocol, host, port, addedAt, key}
let activeId = '';
const newId = () => crypto.randomBytes(8).toString('hex');
const ID_RE = /^[0-9a-f]{16}$/;

function seedServers() {
  const t = now();
  servers = [
    { id: newId(), name: 'Example server A', protocol: 'vless', host: 'example.com', port: 443, addedAt: t - 3 * 86400000, key: credHash('vless', 'example.com', 443, 'seed-a') },
    { id: newId(), name: 'Example server B', protocol: 'trojan', host: '198.51.100.7', port: 443, addedAt: t - 86400000, key: credHash('trojan', '198.51.100.7', 443, 'seed-b') },
  ];
  activeId = servers[0].id;
}
seedServers();

const serverBody = (s) => ({
  id: s.id, name: s.name, protocol: s.protocol, engine: 'singbox', host: s.host, port: s.port,
  active: s.id === activeId, addedAt: iso(s.addedAt),
});

const PROTOCOLS = new Set(['vless', 'vmess', 'trojan', 'shadowsocks', 'hysteria2', 'tuic']);
const SCHEME_PROTOCOL = { vless: 'vless', vmess: 'vmess', trojan: 'trojan', ss: 'shadowsocks', hy2: 'hysteria2', hysteria2: 'hysteria2', tuic: 'tuic' };
const SS_CIPHERS = new Set(['aes-128-gcm', 'aes-256-gcm', 'chacha20-ietf-poly1305', 'xchacha20-ietf-poly1305',
  '2022-blake3-aes-128-gcm', '2022-blake3-aes-256-gcm', '2022-blake3-chacha20-poly1305']);
const FORBIDDEN_KEYS = new Set(['detour', 'bind_interface', 'inet4_bind_address', 'inet6_bind_address', 'routing_mark', 'netns',
  'network_strategy', 'network_type', 'fallback_network_type']);
const BAD_TRANSPORTS = new Set(['xhttp', 'splithttp', 'kcp', 'mkcp', 'quic']); // Xray-only or quic
const OK_TRANSPORTS = new Set(['', 'tcp', 'ws', 'grpc', 'http', 'h2', 'httpupgrade']);
const UUID_RE = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const HOST_RE = /^(?=.{1,253}$)[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$/;
const TRANSPORT_MSG = 'This transport is not supported by the built-in engine.';

const validHost = (h) => (h.startsWith('[') && h.endsWith(']') ? h.includes(':') && /^[0-9a-fA-F:.]+$/.test(h.slice(1, -1)) : HOST_RE.test(h));
const isControl = (ch) => { const c = ch.codePointAt(0); return c < 0x20 || (c >= 0x7f && c <= 0x9f) || c === 0x2028 || c === 0x2029; };
const printable = (s) => ![...s].some(isControl);
const runes = (s) => [...s];
const validName = (s) => { const n = runes(s).length; return n >= 1 && n <= MAX_NAME && s.trim() !== '' && printable(s); };
const safeDecode = (s) => { try { return decodeURIComponent(s); } catch { return s; } };

function b64decode(s) {
  const t = s.replace(/-/g, '+').replace(/_/g, '/').replace(/=+$/, '');
  if (!/^[A-Za-z0-9+/]*$/.test(t) || t.length % 4 === 1) return null;
  return Buffer.from(t, 'base64').toString('utf8');
}

function splitHostPort(s, portOptional) {
  let host;
  let portStr;
  const brackets = 'The link\'s server address is malformed (an IPv6 address needs brackets).';
  if (s.startsWith('[')) {
    const i = s.indexOf(']');
    if (i < 0) bad('The link\'s server address is malformed.');
    host = s.slice(0, i + 1);
    const rest = s.slice(i + 1);
    if (rest && !rest.startsWith(':')) bad('The link\'s server address is malformed.');
    portStr = rest ? rest.slice(1) : undefined;
  } else {
    const n = (s.match(/:/g) || []).length;
    if (n > 1) bad(brackets);
    if (n === 1) [host, portStr] = s.split(':'); else host = s;
  }
  if (!host) bad('The link has no server address.');
  if (!validHost(host)) bad('The link\'s server address is not valid.');
  if (portStr === undefined || portStr === '') {
    if (portOptional) return { host, port: 443 };
    bad('The link has no port.');
  }
  let first = portStr;
  if (portOptional) {
    first = portStr.split(',')[0];
    if (first.includes('-')) {
      const [a, b] = first.split('-');
      if (!/^\d+$/.test(a) || !/^\d+$/.test(b) || Number(a) < 1 || Number(b) > 65535 || Number(a) > Number(b)) bad('The link\'s port range is not valid.');
      first = a;
    }
  }
  if (!/^\d+$/.test(first) || Number(first) < 1 || Number(first) > 65535) bad('The link\'s port is not a number from 1 to 65535.');
  return { host, port: Number(first) };
}

function urlParts(rest) {
  let q = '';
  const qi = rest.indexOf('?');
  if (qi >= 0) { q = rest.slice(qi + 1); rest = rest.slice(0, qi); }
  rest = rest.replace(/\/+$/, '');
  const ai = rest.lastIndexOf('@');
  return { user: ai >= 0 ? rest.slice(0, ai) : null, hp: ai >= 0 ? rest.slice(ai + 1) : rest, q: new URLSearchParams(q) };
}

function checkTransport(q) {
  const t = (q.get('type') || '').toLowerCase();
  if (BAD_TRANSPORTS.has(t) || !OK_TRANSPORTS.has(t)) unsupported(TRANSPORT_MSG);
}
function checkSecurity(q, dflt) {
  const sec = (q.get('security') || dflt).toLowerCase();
  if (!['none', 'tls', 'reality'].includes(sec)) unsupported('The link\'s security option is not supported.');
  if (sec === 'reality') {
    if (!q.get('pbk')) bad('The link uses Reality but has no public key.');
    if (!q.get('sni')) bad('The link uses Reality but has no server name.');
  }
}

function parseLink(s) {
  const m = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/([\s\S]*)$/.exec(s);
  if (!m) bad('This is not a server link or a JSON object.');
  const scheme = m[1].toLowerCase();
  let rest = m[2];
  const protocol = SCHEME_PROTOCOL[scheme];
  if (!protocol) unsupported('This kind of link is not supported.');
  let name = '';
  const hi = rest.indexOf('#');
  if (hi >= 0) {
    const frag = safeDecode(rest.slice(hi + 1)).trim();
    rest = rest.slice(0, hi);
    if (printable(frag)) name = runes(frag).slice(0, MAX_NAME).join('');
  }
  try {
    if (scheme === 'vmess') { const v = parseVmess(rest); return { ...v, name: name || v.name }; }
    if (scheme === 'ss') return { ...parseSS(rest), name };
    const { user, hp, q } = urlParts(rest);
    const u = user === null ? null : safeDecode(user);
    let cred;
    let portOptional = false;
    if (scheme === 'vless') {
      if (!u) bad('The link has no user ID.');
      if (!UUID_RE.test(u)) bad('The link\'s user ID is not a UUID.');
      const enc = (q.get('encryption') || 'none').toLowerCase();
      if (enc !== 'none') unsupported('VLESS encryption is not supported.');
      const flow = q.get('flow') || '';
      if (flow && flow !== 'xtls-rprx-vision') unsupported('The link\'s flow option is not supported.');
      checkSecurity(q, 'none');
      checkTransport(q);
      cred = u;
    } else if (scheme === 'trojan') {
      if (!u) bad('The link has no password.');
      checkSecurity(q, 'tls');
      checkTransport(q);
      cred = u;
    } else if (scheme === 'tuic') {
      if (!u) bad('The link has no user ID.');
      const [id, ...pw] = u.split(':');
      if (!id) bad('The link has no user ID.');
      if (!UUID_RE.test(id)) bad('The link\'s user ID is not a UUID.');
      if (!pw.join(':')) bad('The link has no password.');
      if (q.has('congestion_control') && !['cubic', 'new_reno', 'bbr'].includes(q.get('congestion_control'))) unsupported('The link\'s congestion control is not supported.');
      if (q.has('udp_relay_mode') && !['native', 'quic'].includes(q.get('udp_relay_mode'))) unsupported('The link\'s UDP relay mode is not supported.');
      cred = u;
    } else { // hy2, hysteria2
      portOptional = true;
      if (!u) bad('The link has no password.');
      if (q.has('pinSHA256')) bad('Certificate pinning (pinSHA256) is not supported.');
      if (q.has('obfs')) {
        if (q.get('obfs') !== 'salamander') unsupported('The link\'s obfuscation type is not supported.');
        if (!q.get('obfs-password')) bad('The link uses obfuscation but has no obfuscation password.');
      }
      cred = u;
    }
    const { host, port } = splitHostPort(hp, portOptional);
    return { protocol, host, port, name, cred };
  } catch (e) {
    if (e instanceof InputFail && !e.protocol) e.protocol = protocol;
    throw e;
  }
}

function parseSS(rest) {
  const { user, hp, q } = urlParts(rest);
  let method;
  let password;
  let hostport = hp;
  if (user === null) { // legacy: base64(method:password@host:port)
    const dec = b64decode(rest.split('?')[0].replace(/\/+$/, ''));
    if (dec === null) bad('The link is not valid base64.');
    const ai = dec.lastIndexOf('@');
    const ci = dec.indexOf(':');
    if (ai < 0 || ci < 0 || ci > ai) bad('The link has no cipher or no password.');
    method = dec.slice(0, ci);
    password = dec.slice(ci + 1, ai);
    hostport = dec.slice(ai + 1);
  } else {
    const u = safeDecode(user);
    let ui = u;
    if (!u.includes(':')) {
      ui = b64decode(u);
      if (ui === null || !ui.includes(':')) bad('The link\'s cipher and password are not valid base64.');
    }
    const ci = ui.indexOf(':');
    method = ui.slice(0, ci);
    password = ui.slice(ci + 1);
  }
  if (!method || !password) bad('The link has no cipher or no password.');
  if (!SS_CIPHERS.has(method.toLowerCase())) unsupported('The link\'s cipher is not supported.');
  const plugin = (q.get('plugin') || '').split(';')[0];
  if (plugin && !['obfs-local', 'v2ray-plugin'].includes(plugin)) unsupported('The link\'s plugin is not supported.');
  const { host, port } = splitHostPort(hostport, false);
  return { protocol: 'shadowsocks', host, port, cred: `${method}:${password}` };
}

function parseVmess(rest) {
  if (rest.includes('@')) { // URL form vmess://uuid@host:port?...
    const { user, hp, q } = urlParts(rest);
    const u = safeDecode(user || '');
    if (!u) bad('The link has no user ID.');
    if (!UUID_RE.test(u)) bad('The link\'s user ID is not a UUID.');
    checkTransport(q);
    const { host, port } = splitHostPort(hp, false);
    return { protocol: 'vmess', host, port, cred: u };
  }
  const dec = b64decode(rest.replace(/\/+$/, ''));
  if (dec === null) bad('The link is not valid base64.');
  let j;
  try { j = JSON.parse(dec); } catch { bad('The link does not contain a valid VMess description.'); }
  if (!j || typeof j !== 'object' || Array.isArray(j)) bad('The link does not contain a valid VMess description.');
  if (!j.add) bad('The link has no server address.');
  if (j.port === undefined || j.port === '') bad('The link has no port.');
  if (!j.id) bad('The link has no user ID.');
  if (!UUID_RE.test(String(j.id))) bad('The link\'s user ID is not a UUID.');
  if (j.aid !== undefined && j.aid !== '' && !/^\d+$/.test(String(j.aid))) bad('The link\'s alterId is not a number.');
  const net = String(j.net || 'tcp').toLowerCase();
  if (BAD_TRANSPORTS.has(net) || !OK_TRANSPORTS.has(net)) unsupported(TRANSPORT_MSG);
  const { host, port } = splitHostPort(`${String(j.add).includes(':') ? `[${j.add}]` : j.add}:${j.port}`, false);
  const ps = typeof j.ps === 'string' && printable(j.ps) ? runes(j.ps.trim()).slice(0, MAX_NAME).join('') : '';
  return { protocol: 'vmess', host, port, cred: String(j.id), name: ps };
}

function parseJSONInput(s) {
  let o;
  try { o = JSON.parse(s); } catch (e) {
    bad(/after JSON/.test(String(e.message)) ? 'Paste exactly one JSON object.' : 'The JSON is not valid.');
  }
  if (!o || typeof o !== 'object' || Array.isArray(o)) bad('The JSON must be one object.');
  if (typeof o.type !== 'string' || !o.type) bad('The JSON has no "type" field.');
  if (!PROTOCOLS.has(o.type)) unsupported('This protocol is not supported.');
  try {
    const tr = o.transport && typeof o.transport === 'object' ? String(o.transport.type || '').toLowerCase() : '';
    if (BAD_TRANSPORTS.has(tr)) unsupported(TRANSPORT_MSG);
    let count = 0;
    (function walk(v, depth) {
      if (depth > 6) bad('The JSON is nested too deeply.');
      if (Array.isArray(v)) { for (const x of v) { if (++count > 500) bad('The JSON has too many fields.'); if (x && typeof x === 'object') walk(x, depth + 1); } return; }
      for (const [k, x] of Object.entries(v)) {
        if (++count > 500) bad('The JSON has too many fields.');
        if (FORBIDDEN_KEYS.has(k)) bad('The JSON sets how traffic leaves this computer (detour or bind options), which is not allowed.');
        if (x && typeof x === 'object') walk(x, depth + 1);
      }
    })(o, 1);
    if (typeof o.server !== 'string' || !o.server) bad('The server has no address.');
    if (!validHost(o.server)) bad('The server address is not valid.');
    if (!Number.isInteger(o.server_port) || o.server_port < 1 || o.server_port > 65535) bad('The server has no valid port (1 to 65535).');
    const needsUUID = ['vless', 'vmess', 'tuic'].includes(o.type);
    const cred = needsUUID ? o.uuid : o.password;
    if (typeof cred !== 'string' || !cred) bad('The server settings are not valid.');
    if (needsUUID && !UUID_RE.test(cred)) bad('The server settings are not valid.');
    if (o.type === 'shadowsocks') {
      if (typeof o.method !== 'string' || !o.method) bad('The server settings are not valid.');
      if (!SS_CIPHERS.has(o.method.toLowerCase())) unsupported('The link\'s cipher is not supported.');
    }
    return { protocol: o.type, host: o.server, port: o.server_port, name: '', cred: `${o.method || ''}:${cred}` };
  } catch (e) {
    if (e instanceof InputFail && !e.protocol) e.protocol = o.type;
    throw e;
  }
}

/** Parses a share link or one raw JSON object. Returns {protocol, host, port, name, cred} or throws InputFail. */
function parseInput(input) {
  if (Buffer.byteLength(input, 'utf8') > MAX_INPUT) fail(413, 'too_large', 'The link or JSON is too large.');
  const s = input.trim();
  if (!s) bad('The input is empty.');
  if (s.startsWith('{') || s.startsWith('[')) return parseJSONInput(s);
  if (/[\r\n]/.test(s)) bad('Paste one link at a time.');
  return parseLink(s);
}

function inputFailBody(e) {
  return err(e.code, e.message);
}

function defaultName(taken) {
  for (let n = servers.length + 1; ; n++) {
    const c = `Server ${n}`;
    if (!taken.has(c)) return c;
  }
}

function busyNow() { return eng.state === 'connecting' || eng.state === 'disconnecting'; }
const BUSY_MSG = 'The engine is busy connecting or disconnecting, or the server is in use. Try again in a moment.';

function listServers() { return [200, servers.map(serverBody)]; }

function addServer(body) {
  if (typeof body.input !== 'string' || body.input === '') fail(400, 'bad_request', 'input must be a server link or a JSON object.');
  if (body.name !== undefined && typeof body.name !== 'string') fail(400, 'bad_request', 'name must be 1 to 60 printable characters.');
  if (body.name && !validName(body.name)) fail(400, 'bad_request', 'name must be 1 to 60 printable characters.');
  if (busyNow()) fail(409, 'busy', BUSY_MSG);
  let p;
  try { p = parseInput(body.input); } catch (e) {
    if (e instanceof InputFail) return [422, inputFailBody(e)];
    throw e;
  }
  const key = credHash(p.protocol, p.host, p.port, p.cred);
  if (servers.some((s) => s.key === key)) return [409, err('duplicate', 'This server is already in the list.')];
  if (servers.length >= MAX_SERVERS) return [409, err('limit_reached', 'The list is full (20 servers). Remove one first.')];
  const taken = new Set(servers.map((s) => s.name));
  const name = body.name || (p.name && validName(p.name) ? p.name : '') || defaultName(taken);
  // Only non-secret facts are kept. p.cred and the input are dropped here.
  const s = { id: newId(), name, protocol: p.protocol, host: p.host, port: p.port, addedAt: now(), key };
  servers.push(s);
  log('splitter', 'INFO', `server added: ${s.id} (${s.protocol}); not activated`);
  return [201, serverBody(s)];
}

function activateServer(id) {
  const s = ID_RE.test(id) && servers.find((x) => x.id === id);
  if (!s) fail(404, 'not_found', 'There is no such server.');
  if (busyNow()) fail(409, 'busy', BUSY_MSG);
  if (activeId === id) return [200, { ok: true, active: id, reloaded: false }];
  if (eng.state !== 'connected') {
    activeId = id;
    log('splitter', 'INFO', `server activated: ${id}`);
    return [200, { ok: true, active: id, reloaded: false }];
  }
  // Connected: the tunnel restarts once (short flicker); progress arrives as state events.
  const previous = activeId;
  activeId = id;
  log('splitter', 'INFO', `server ${id} activated while connected; reloading sing-box`);
  setState('connecting', null);
  later(1200, () => {
    if (mock.reloadFails) {
      activeId = previous;
      log('sing-box', 'ERROR', 'reload failed; the previous server is back in place');
      setState('connected', err('connect_failed', 'Could not connect. See the engine log for details.'));
    } else {
      log('sing-box', 'INFO', 'reloaded with the new server, handshake ok');
      setState('connected', null);
    }
  });
  return [202, { ok: true, active: id, reloaded: true }];
}

function deleteServer(id) {
  const i = ID_RE.test(id) ? servers.findIndex((x) => x.id === id) : -1;
  if (i < 0) fail(404, 'not_found', 'There is no such server.');
  if (busyNow() || (id === activeId && eng.state === 'connected')) fail(409, 'busy', BUSY_MSG);
  servers.splice(i, 1);
  if (id === activeId) { activeId = ''; log('splitter', 'INFO', 'active server removed; the next connect needs a server'); }
  log('splitter', 'INFO', `server removed: ${id}`);
  return [200, { ok: true }];
}

function testServer(body) {
  const hasId = typeof body.id === 'string' && body.id !== '';
  const hasInput = typeof body.input === 'string' && body.input !== '';
  if (hasId === hasInput || (body.id !== undefined && typeof body.id !== 'string') || (body.input !== undefined && typeof body.input !== 'string')) {
    fail(400, 'bad_request', 'Send either id or input.');
  }
  if (hasId) {
    const s = ID_RE.test(body.id) && servers.find((x) => x.id === body.id);
    if (!s) fail(404, 'not_found', 'There is no such server.');
    return [200, { ok: true, protocol: s.protocol, code: '' }];
  }
  try {
    const p = parseInput(body.input);
    return [200, { ok: true, protocol: p.protocol, code: '' }];
  } catch (e) {
    if (e instanceof InputFail) return [200, { ok: false, protocol: e.protocol || '', code: e.code }]; // offline test: no sentence, only the code
    throw e;
  }
}

// ---------- actions ----------

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
    if (!activeId) {
      log('splitter', 'ERROR', 'no tunnel server in use; add one in Settings');
      return setState('disconnected', err('tunnel_missing', 'The detour server is not set up or cannot be reached.'));
    }
    log('sing-box', 'INFO', 'sing-box started, TUN up');
    log('sing-box', 'INFO', 'outbound tunnel: handshake ok');
    log('desync', 'INFO', 'desync engine started (mock)');
    log('splitter', 'INFO', 'connected');
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
  const wasConnecting = eng.state === 'connecting';
  cancelTimers();
  setState('disconnecting', null);
  log('splitter', 'INFO', wasConnecting ? 'connect cancelled' : 'disconnect requested');
  later(800, () => {
    log('sing-box', 'INFO', 'sing-box stopped');
    log('desync', 'INFO', 'desync engine stopped');
    log('splitter', 'INFO', 'disconnected');
    eng.connections = 0;
    setState('disconnected', null);
  });
  return [202, { ok: true, state: 'disconnecting' }];
}

const DOMAIN_RE = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/;

function checkNow(body) {
  let targets = null;
  if (body && body.domains !== undefined) {
    if (!Array.isArray(body.domains) || body.domains.length > 100 || body.domains.some((d) => typeof d !== 'string')) {
      fail(400, 'bad_request', 'domains must be a list of at most 100 names.');
    }
    const names = body.domains.map((d) => d.trim().toLowerCase().replace(/\.$/, ''));
    if (names.some((d) => !DOMAIN_RE.test(d))) fail(400, 'bad_request', 'domains must be valid site names.');
    if (names.length) {
      targets = names.filter((d) => domains.has(d));
      if (targets.length === 0) return [404, err('unknown_domain', 'None of these sites are in the table.')];
    }
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
      if (r.history.length > 10) r.history.shift();
      r.route = to;
      r.since = t;
      changes.push(c);
      eng.recent.push(c);
      log('splitter', 'INFO', `route change: ${d} ${c.From} -> ${to}`);
    }
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
      r.traffic += add;
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
function notAllowed(res, allow) {
  res.setHeader('Allow', allow);
  return json(res, 405, err('method_not_allowed', 'Method not allowed.'));
}
function hostOK(req) {
  const h = String(req.headers.host || '');
  const m = /^(127\.0\.0\.1|localhost|\[::1\])(?::(\d+))?$/i.exec(h);
  return !!m && (!m[2] || Number(m[2]) === server.address().port);
}

/** Body is optional; if present it is JSON, an object, and (when `allowed` is given) has no unknown field. */
function readBody(req, allowed) {
  return new Promise((resolve, reject) => {
    let size = 0;
    let tooBig = false;
    const chunks = [];
    req.on('data', (c) => {
      size += c.length;
      if (size > MAX_BODY) { tooBig = true; chunks.length = 0; return; } // keep draining, answer 413 at the end
      if (!tooBig) chunks.push(c);
    });
    req.on('end', () => {
      if (tooBig) return reject(new ApiErr(413, 'too_large', 'The request is too large.'));
      const s = Buffer.concat(chunks).toString('utf8').trim();
      if (!s) return resolve({});
      if (!String(req.headers['content-type'] || '').toLowerCase().startsWith('application/json')) {
        return reject(new ApiErr(415, 'bad_request', 'Content-Type must be application/json.'));
      }
      let v;
      try { v = JSON.parse(s); } catch { return reject(new ApiErr(400, 'bad_request', 'The request body is not valid JSON.')); }
      if (!v || typeof v !== 'object' || Array.isArray(v)) return reject(new ApiErr(400, 'bad_request', 'The request body must be a JSON object.'));
      if (allowed) for (const k of Object.keys(v)) if (!allowed.includes(k)) return reject(new ApiErr(400, 'bad_request', 'The request has an unknown field.'));
      resolve(v);
    });
    req.on('error', reject);
  });
}

function serveStatic(req, res, pathname) {
  if (req.method !== 'GET' && req.method !== 'HEAD') return notAllowed(res, 'GET');
  let rel;
  try { rel = decodeURIComponent(pathname); } catch { return json(res, 400, err('bad_request', 'Bad request.')); }
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

const SCENARIOS = ['normal', 'permission-denied', 'needs-setup', 'crash', 'no-engine', 'no-server'];
const MOCK_FLAGS = ['warnings', 'extras', 'learning', 'reloadFails'];

function applyMock(body) {
  if (body.scenario !== undefined) {
    if (!SCENARIOS.includes(body.scenario)) return [400, err('bad_request', `scenario must be one of ${SCENARIOS.join(', ')}`)];
    const prev = mock.scenario;
    mock.scenario = body.scenario;
    if (mock.scenario === 'no-engine') {
      cancelTimers();
      eng.state = 'disconnected';
      eng.checking = false;
      for (const c of clients) c.end();
      clients.clear();
    }
    if (mock.scenario === 'no-server') {
      servers = [];
      activeId = '';
    } else if (prev === 'no-server') {
      seedServers();
    }
  }
  for (const k of MOCK_FLAGS) if (typeof body[k] === 'boolean') mock[k] = body[k];
  if (body.network === 'next') {
    const i = NETWORKS.indexOf(eng.network);
    eng.network = NETWORKS[(i + 1) % NETWORKS.length];
    eng.networks.push({ label: eng.network, since: iso(now()) });
    if (eng.networks.length > 10) eng.networks.shift();
    log('splitter', 'INFO', `network changed: "${eng.network}"`);
  }
  if (body.reset === true) {
    seedDomains();
    eng.recent = [];
    eng.upload = eng.download = 0;
    if (mock.scenario === 'no-server') { servers = []; activeId = ''; } else seedServers();
  }
  if (mock.scenario !== 'no-engine') emit('state', statePayload());
  return [200, { ok: true, mock: { ...mock } }];
}

const SERVER_ID_PATH = /^servers\/([^/]*)(\/activate)?$/;

async function handleApi(req, res, url) {
  const p = url.pathname.slice('/api/v1/'.length);
  const method = req.method;

  // Mock-only controls stay reachable in every scenario.
  if (p === 'mock/scenario') {
    if (method === 'GET') {
      const q = Object.fromEntries(url.searchParams);
      const body = {};
      if (q.scenario) body.scenario = q.scenario;
      for (const k of MOCK_FLAGS) if (q[k] === 'true' || q[k] === 'false') body[k] = q[k] === 'true';
      if (q.network) body.network = q.network;
      if (Object.keys(body).length === 0) return json(res, 200, { mock: { ...mock }, scenarios: SCENARIOS });
      const [s, b] = applyMock(body);
      return json(res, s, b);
    }
    if (method === 'POST') { const [s, b] = applyMock(await readBody(req)); return json(res, s, b); }
    return notAllowed(res, 'GET, POST');
  }
  if (p === 'mock/restart-engine' && method === 'POST') {
    // Stands in for the Tauri side restarting the splitter sidecar.
    cancelTimers();
    mock.scenario = mock.scenario === 'no-server' ? 'no-server' : 'normal';
    eng.shutdown = false;
    eng.state = 'disconnected';
    eng.error = null;
    eng.checking = false;
    eng.restarts.splitter += 1;
    log('splitter', 'INFO', 'splitter restarted by the UI (mock)');
    emit('state', statePayload());
    return json(res, 200, { ok: true });
  }

  if (p === 'shutdown' && eng.shutdown && method === 'POST') return json(res, 200, { ok: true });
  if (mock.scenario === 'no-engine' || eng.shutdown) {
    return json(res, 503, err('engine_unreachable', 'The engine is not running.'));
  }

  if (p === 'events') {
    if (method !== 'GET') return notAllowed(res, 'GET');
    if (clients.size >= MAX_STREAMS) return json(res, 429, err('busy', 'Too many open event streams.'));
    res.writeHead(200, { 'Content-Type': 'text/event-stream; charset=utf-8', Connection: 'keep-alive' });
    res.write('retry: 2000\n\n');
    res.write(`id: ${++eventId}\nevent: state\ndata: ${JSON.stringify(statePayload())}\n\n`);
    clients.add(res);
    req.on('close', () => clients.delete(res));
    return;
  }

  // Server management (milestone 4).
  if (p === 'servers') {
    if (method === 'GET') { const [s, b] = listServers(); return json(res, s, b); }
    if (method === 'POST') { const [s, b] = addServer(await readBody(req, ['input', 'name'])); return json(res, s, b); }
    return notAllowed(res, 'GET, POST');
  }
  if (p === 'servers/test') {
    if (method !== 'POST') return notAllowed(res, 'POST');
    const [s, b] = testServer(await readBody(req, ['id', 'input']));
    return json(res, s, b);
  }
  const sm = SERVER_ID_PATH.exec(p);
  if (sm) {
    const id = sm[1];
    if (sm[2]) {
      if (method !== 'POST') return notAllowed(res, 'POST');
      await readBody(req, []);
      const [s, b] = activateServer(id);
      return json(res, s, b);
    }
    if (method !== 'DELETE') return notAllowed(res, 'DELETE');
    const [s, b] = deleteServer(id);
    return json(res, s, b);
  }

  const GET = {
    state: () => [200, statePayload()],
    routes: () => [200, visibleDomains().map(([domain, r]) => ({
      domain, route: r.route, source: r.route === 'proxy' || r.route === 'block' ? 'static' : 'checked',
      since: iso(r.since), lastCheck: iso(eng.lastCheck), history: r.history.slice(-10),
    })).sort((a, b) => a.domain.localeCompare(b.domain))],
    processes: () => [200, processes()],
    version: () => [200, VERSION],
    logs: () => {
      const name = url.searchParams.get('name');
      if (!Object.hasOwn(logs, name || '')) return [400, err('bad_request', 'name must be splitter, sing-box or desync.')];
      return [200, { name, lines: logs[name].slice(-200) }];
    },
  };
  const POST = {
    connect: () => connect(),
    disconnect: () => disconnect(),
    'check-now': (b) => checkNow(b),
    shutdown: () => {
      log('splitter', 'INFO', 'shutdown requested by the UI');
      // Real behaviour: answer, send a final shutdown event, close the streams, answer 503 from then on.
      // The mock "restarts" two seconds later so reloading the page still works.
      setTimeout(() => {
        cancelTimers();
        eng.state = 'disconnected';
        eng.checking = false;
        eng.shutdown = true;
        emit('shutdown', {});
        for (const c of clients) c.end();
        clients.clear();
        setTimeout(() => { eng.shutdown = false; }, 2000).unref();
      }, 50).unref();
      return [200, { ok: true }];
    },
  };
  const BODY_FIELDS = { 'check-now': ['domains'] };

  if (Object.hasOwn(GET, p)) {
    if (method !== 'GET') return notAllowed(res, 'GET');
    const [s, b] = GET[p]();
    return json(res, s, b);
  }
  if (Object.hasOwn(POST, p)) {
    if (method !== 'POST') return notAllowed(res, 'POST');
    const body = await readBody(req, BODY_FIELDS[p] || []);
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
    if (res.headersSent) { res.end(); return; }
    if (e instanceof ApiErr) return json(res, e.status, err(e.code, e.message, e.extra));
    return json(res, 500, err('internal', 'Something went wrong inside the engine.'));
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
