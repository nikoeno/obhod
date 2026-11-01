// DEV AND TEST ONLY. An in-memory stand-in for the Tauri backend, so `npm run dev` in a plain
// browser and vitest work without Rust or an engine. It follows the real contract
// (api/desktop/API.md): payload shapes, error codes, 202 vs 200 semantics, server rules.
//
// It is only reachable through `import.meta.env.DEV` in src/backend/index.ts and is not part of a
// production build (checked after `npm run build`). All data is fake: documentation addresses and
// example.com. Pasted server text is checked and thrown away; it is never stored or echoed.

import {
  BackendError,
  type Ack, type Activated, type Backend, type Change, type ConnState, type DevControls, type DevFlag, type EngineErrorBody,
  type EngineEvent, type EngineState, type LogName, type Logs, type ProcessInfo, type Route, type RouteInfo, type Scenario, type Server,
  type ServerTest, type SiteAction, type SiteRule, type Snapshot, type TestRequest, type Version,
} from '../backend/types';

export interface FakeOptions {
  /** Multiplies every simulated delay. 0 = as fast as the event loop allows (tests). */
  speed?: number;
  /** Emit a traffic event every 2 s while connected. Off in tests. */
  traffic?: boolean;
  scenario?: Scenario;
}

const ZERO_TIME = '0001-01-01T00:00:00Z';
/** A sample of the Details box for the dev-only "engine unreachable" scenario. */
const FAKE_START_DETAILS = [
  'stage: the engine exited before it was ready',
  'exit code: 1',
  'engine: splitter.exe (found)',
  'engine output:',
  'splitter: config: open …/app.yaml: Access is denied.',
  'exit code: 1',
].join('\n');

const NEEDS_SETUP: EngineErrorBody = {
  error: 'One-time setup is needed on this computer.',
  code: 'needs_setup',
  commands: ['nft add table inet splitter', 'ip rule add fwmark 0x40000000/0x40000000 lookup main priority 8999'],
};

const DOMAINS: Array<[string, Route]> = [
  ['news.example', 'direct'], ['mail.example', 'direct'], ['shop.example', 'direct'], ['maps.example', 'direct'], ['docs.example', 'direct'],
  ['video.example', 'desync'], ['music.example', 'desync'], ['chat.example', 'desync'],
  ['social.example', 'tunnel'], ['wiki.example', 'tunnel'], ['photos.example', 'tunnel'],
];
const EXTRA_DOMAINS: Array<[string, Route]> = [['ads.example', 'block'], ['tracker.example', 'block'], ['office.example', 'proxy']];

const SCHEMES: Record<string, string> = { vless: 'vless', vmess: 'vmess', trojan: 'trojan', ss: 'shadowsocks', hy2: 'hysteria2', hysteria2: 'hysteria2', tuic: 'tuic' };
const MAX_INPUT = 32 * 1024;
const MAX_SITE_RULES = 500;
const SITE_ACTIONS: readonly string[] = ['direct', 'desync', 'tunnel', 'block'];

/** The engine's `route.Normalize` plus the leading `*.` / `.` it accepts; null when it would answer 422. */
function normalizeSite(raw: string): string | null {
  const d = raw.trim().toLowerCase().replace(/\.$/, '').replace(/^\*?\./, '');
  if (!d || /[*:/?#\s]/.test(d) || d.includes('://')) return null;
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(d)) return null;
  const labels = d.split('.');
  for (const l of labels) {
    if (!l || l.length > 63 || l.startsWith('-') || l.endsWith('-') || !/^[\p{L}\p{N}_-]+$/u.test(l)) return null;
  }
  // The real engine stores non-ASCII names as punycode; the fake keeps them as typed.
  return d.length > 253 ? null : d;
}
const SITE_INVALID = 'That is not a plain site name. Enter only the name, for example example.com.';
const MAX_SERVERS = 20;

interface Parsed { protocol: string; host: string; port: number; key: string; name: string }
class InputError extends Error {
  constructor(readonly code: 'invalid_input' | 'unsupported_protocol', message: string) {
    super(message);
  }
}

/** Shape check only (scheme, host, port); the real engine does much more. Sentences are fixed and never quote the input. */
function parseInput(raw: string): Parsed {
  const text = raw.trim();
  if (text.startsWith('{')) {
    let o: unknown;
    try {
      o = JSON.parse(text);
    } catch {
      throw new InputError('invalid_input', 'The JSON is not valid.');
    }
    const r = o as { type?: string; server?: string; server_port?: number; tag?: string };
    if (!r || typeof r !== 'object' || typeof r.type !== 'string') throw new InputError('invalid_input', 'The JSON has no type.');
    if (!(r.type in SCHEMES) && r.type !== 'shadowsocks' && r.type !== 'hysteria2') throw new InputError('unsupported_protocol', 'This protocol is not supported.');
    if (!r.server) throw new InputError('invalid_input', 'The JSON has no server address.');
    if (!r.server_port) throw new InputError('invalid_input', 'The JSON has no port.');
    return { protocol: SCHEMES[r.type] ?? r.type, host: r.server, port: r.server_port, key: `${r.type}|${r.server}|${r.server_port}`, name: '' };
  }
  const m = /^([a-z0-9]+):\/\/(.*)$/i.exec(text);
  if (!m) throw new InputError('invalid_input', 'This is not a server link.');
  const scheme = m[1]!.toLowerCase();
  if (!(scheme in SCHEMES)) throw new InputError('unsupported_protocol', 'This protocol is not supported.');
  if (/[?&]type=(xhttp|splithttp|kcp|quic)\b/i.test(text)) throw new InputError('unsupported_protocol', 'This transport is not supported.');
  const hashAt = m[2]!.indexOf('#');
  const body = hashAt >= 0 ? m[2]!.slice(0, hashAt) : m[2]!;
  const fragment = hashAt >= 0 ? decodeURIComponent(m[2]!.slice(hashAt + 1)) : '';
  const hostPart = body.split('?')[0]!.split('/')[0]!;
  const at = hostPart.lastIndexOf('@');
  if (at < 0) throw new InputError('invalid_input', 'The link has no user or key.');
  const hp = /^(\[[^\]]+\]|[^:]+):(\d+)$/.exec(hostPart.slice(at + 1));
  if (!hp) throw new InputError('invalid_input', 'The link has no port.');
  const port = Number(hp[2]);
  if (port < 1 || port > 65535) throw new InputError('invalid_input', 'The port is not valid.');
  return { protocol: SCHEMES[scheme]!, host: hp[1]!.replace(/^\[|\]$/g, ''), port, key: `${scheme}|${hostPart}`, name: fragment.slice(0, 60) };
}

function idFrom(n: number): string {
  return n.toString(16).padStart(16, '0');
}

export function createFakeBackend(opts: FakeOptions = {}): Backend {
  const speed = opts.speed ?? 1;
  const trafficOn = opts.traffic ?? speed > 0;
  let scenario: Scenario = opts.scenario ?? 'normal';
  const flags: Record<DevFlag, boolean> = { warnings: false, extras: false, learning: false, reloadFails: false };

  let state: ConnState = 'disconnected';
  let error: EngineErrorBody | null = null;
  let checking = false;
  let engineRunning = scenario !== 'engine-unreachable';
  let network = 'Home Wi-Fi (Example ISP)';
  let networks = [
    { label: 'Office network', since: new Date(Date.now() - 3 * 3600000).toISOString() },
    { label: network, since: new Date(Date.now() - 40 * 60000).toISOString() },
  ];
  let routes: RouteInfo[] = [];
  let servers: Server[] = [];
  let sites: SiteRule[] = [];
  /** What a ruled row was before the rule (null: the rule added the row). Restored when the rule is deleted. */
  const before = new Map<string, Pick<RouteInfo, 'route' | 'source' | 'since'> | null>();
  let recent: Change[] = [];
  let seq = 1;
  let sent = { direct: 0, tunnel: 0, desync: 0, proxy: 0 } as Record<string, number>;
  let lastCheck = new Date().toISOString();
  const handlers = new Set<(e: EngineEvent) => void>();
  const timers = new Set<ReturnType<typeof setTimeout>>();
  let ticker: ReturnType<typeof setInterval> | null = null;
  const stamp = (min: number): string => new Date(Date.now() - min * 60000).toISOString().slice(11, 19);
  /** Plausible log lines that follow the scenario. No host names, addresses or secrets. */
  function logLines(name: LogName): string[] {
    const crashed = error?.code === 'crashed';
    switch (name) {
      case 'splitter':
        return [
          `${stamp(9)} info  app: control API ready (fake)`,
          `${stamp(8)} info  checker: network identified`,
          `${stamp(8)} info  checker: first pass finished, ${routes.length} sites`,
          ...(state === 'connected' || crashed ? [`${stamp(6)} info  pc: connected`] : []),
          ...(crashed ? [`${stamp(1)} error pc: sing-box exited unexpectedly`] : []),
          ...(error?.code === 'needs_setup' ? [`${stamp(1)} error pc: nft table inet splitter is missing`] : []),
          ...(error?.code === 'tunnel_missing' ? [`${stamp(1)} error pc: no active detour server`] : []),
        ];
      case 'sing-box':
        return state === 'connected' || crashed
          ? [`${stamp(6)} info  sing-box: started (fake)`, `${stamp(6)} info  outbound/tunnel: ready`, ...(crashed ? [`${stamp(1)} fatal sing-box: stopped (fake)`] : [])]
          : [];
      default:
        return state === 'connected' || crashed ? [`${stamp(6)} info  desync: started (fake)`, `${stamp(6)} info  desync: 3 profiles loaded`] : [];
    }
  }

  function later(fn: () => void, ms: number): void {
    const t = setTimeout(() => {
      timers.delete(t);
      fn();
    }, ms * speed);
    timers.add(t);
  }

  const ago = (min: number): string => new Date(Date.now() - min * 60000).toISOString();

  /** A site that moved before: `since` is when it got its path, `history` how it got there. */
  const MOVED: Record<string, { minutesAgo: number; from: Route; source?: string }> = {
    'video.example': { minutesAgo: 2 * 24 * 60, from: 'direct' },
    'social.example': { minutesAgo: 190, from: 'direct' },
    'music.example': { minutesAgo: 45, from: 'direct' },
    'wiki.example': { minutesAgo: 8, from: 'desync' },
    // Not checked on this network yet: the engine's safe default.
    'chat.example': { minutesAgo: 3, from: 'direct', source: 'default' },
  };

  /** Puts a rule into the table. Returns the change when the route moved (or the row is new). */
  function applyRule(rule: SiteRule, record: boolean): Change | null {
    const at = new Date().toISOString();
    const row = routes.find((r) => r.domain === rule.domain);
    if (!row) {
      before.set(rule.domain, null);
      routes.push({ domain: rule.domain, route: rule.action, source: 'user', since: at, lastCheck: lastCheck, history: [] });
      return record ? { Domain: rule.domain, From: '', To: rule.action, At: at } : null;
    }
    if (!before.has(rule.domain)) before.set(rule.domain, { route: row.route, source: row.source, since: row.since });
    const from = row.route;
    row.source = 'user';
    if (from === rule.action) return null;
    row.route = rule.action;
    row.since = at;
    const ch: Change = { Domain: rule.domain, From: from, To: rule.action, At: at };
    if (record) row.history.push(ch);
    return record ? ch : null;
  }

  function seed(): void {
    const list = [...DOMAINS, ...(flags.extras ? EXTRA_DOMAINS : [])];
    routes = flags.learning
      ? []
      : list.map(([domain, route], i) => {
          const m = MOVED[domain];
          const at = m ? ago(m.minutesAgo) : ago(60 * 24 * (1 + (i % 4)));
          const history: Change[] = m ? [{ Domain: domain, From: m.from, To: route, At: at }] : [];
          const source = m?.source ?? (route === 'block' || route === 'proxy' ? 'static' : 'checked');
          return { domain, route, source, since: at, lastCheck: ago(5), history };
        });
    recent = routes
      .flatMap((r) => r.history)
      .filter((c) => Date.now() - Date.parse(c.At) < 24 * 60 * 60000)
      .sort((a, b) => Date.parse(a.At) - Date.parse(b.At));
    before.clear();
    for (const rule of sites) applyRule(rule, false);
    if (scenario === 'no-server') servers = [];
    else if (!servers.length) {
      servers = [
        { id: idFrom(1), name: 'Server 1', protocol: 'vless', engine: 'singbox', host: '192.0.2.10', port: 443, active: true, addedAt: new Date().toISOString() },
        { id: idFrom(2), name: 'Backup', protocol: 'trojan', engine: 'singbox', host: '198.51.100.7', port: 8443, active: false, addedAt: new Date().toISOString() },
      ];
      seq = 3;
    }
  }
  seed();

  function emit(e: EngineEvent): void {
    for (const h of [...handlers]) h(e);
  }

  function processes(): ProcessInfo[] {
    const up = state === 'connected';
    const crashed = error?.code === 'crashed';
    return [
      { Name: 'splitter', State: 'running', Restarts: 0 },
      { Name: 'sing-box', State: up ? 'running' : crashed ? 'failed' : 'stopped', Restarts: crashed ? 3 : 0 },
      { Name: 'desync', State: up ? 'running' : 'stopped', Restarts: 0 },
    ];
  }

  function snapshot(): Snapshot {
    const counts: Partial<Record<Route, number>> = {};
    for (const r of routes) counts[r.route] = (counts[r.route] ?? 0) + 1;
    const total = Object.values(sent).reduce((a, b) => a + b, 0);
    return {
      Version: 'splitter fake',
      State: state,
      Network: flags.learning ? 'unknown network' : network,
      LastCheck: flags.learning ? ZERO_TIME : lastCheck,
      NextCheck: flags.learning ? ZERO_TIME : new Date(Date.now() + 12 * 60000).toISOString(),
      Routes: counts,
      Traffic: { ...sent },
      Savings: total > 0 ? (sent.direct! + sent.desync!) / total : 0,
      Processes: processes(),
      Warnings: flags.warnings ? ['IPv6 is not available on this network', 'The detour server is slower than usual'] : [],
      Recent: recent.slice(-20),
    };
  }

  function stateBody(): EngineState {
    return { state, error, checking, snapshot: snapshot(), networks };
  }

  function emitState(): void {
    emit({ kind: 'state', data: stateBody() });
  }

  function gone(): never {
    throw new BackendError('engine_unreachable', 'The engine is not running.');
  }
  function live(): void {
    if (!engineRunning || scenario === 'engine-unreachable') gone();
  }

  function startTraffic(): void {
    if (!trafficOn || ticker) return;
    ticker = setInterval(() => {
      if (state !== 'connected') return;
      const delta = { direct: 40000 + Math.floor(Math.random() * 90000), tunnel: 8000 + Math.floor(Math.random() * 30000), desync: 20000 + Math.floor(Math.random() * 50000), proxy: 0 };
      for (const k of Object.keys(delta)) sent[k] = (sent[k] ?? 0) + delta[k as keyof typeof delta];
      const total = Object.values(sent).reduce((a, b) => a + b, 0);
      emit({
        kind: 'traffic',
        data: { uploadTotal: total / 8, downloadTotal: total, connections: 14, delta, Traffic: { ...sent }, Savings: (sent.direct! + sent.desync!) / total, At: new Date().toISOString() },
      });
    }, 2000);
  }
  function stopTraffic(): void {
    if (ticker) clearInterval(ticker);
    ticker = null;
  }

  function fail(e: EngineErrorBody): void {
    state = 'disconnected';
    error = e;
    stopTraffic();
    emitState();
  }

  const activeServer = (): Server | undefined => servers.find((s) => s.active);
  const busyNow = (): boolean => state === 'connecting' || state === 'disconnecting';

  function reset(): void {
    for (const t of timers) clearTimeout(t);
    timers.clear();
    stopTraffic();
    state = 'disconnected';
    error = null;
    checking = false;
    sent = { direct: 0, tunnel: 0, desync: 0, proxy: 0 };
    recent = [];
    servers = [];
    sites = [];
    seed();
  }

  const dev: DevControls = {
    scenario: () => scenario,
    setScenario(s) {
      const wasDown = scenario === 'engine-unreachable';
      scenario = s;
      engineRunning = s !== 'engine-unreachable';
      if (s === 'no-server') servers = [];
      else if (!servers.length) seed();
      if (s === 'engine-unreachable') {
        for (const t of timers) clearTimeout(t);
        timers.clear();
        stopTraffic();
        state = 'disconnected';
        emit({ kind: 'stream-lost', data: {} });
      } else if (wasDown) {
        emit({ kind: 'stream-open', data: {} });
        emitState();
      }
    },
    setFlag(key, value) {
      flags[key] = value;
      if (key === 'extras' || key === 'learning') {
        seed();
        emitState();
        emit({ kind: 'routes', data: { changes: [] } });
      }
      if (key === 'warnings') emitState();
    },
    flags: () => ({ ...flags }),
  };

  const backend: Backend = {
    dev,
    async uiVersion() {
      return 'dev';
    },
    async engineStart() {
      if (scenario === 'engine-unreachable') {
        throw new BackendError('engine_unreachable', 'The engine stopped before it was ready.', undefined, FAKE_START_DETAILS);
      }
      engineRunning = true;
      emit({ kind: 'stream-open', data: {} });
      emitState();
    },
    async engineRestart() {
      reset();
      scenario = 'normal';
      engineRunning = true;
      seed();
      emit({ kind: 'stream-open', data: {} });
      emitState();
    },
    async getState() {
      live();
      return stateBody();
    },
    async getRoutes() {
      live();
      return routes.map((r) => ({ ...r }));
    },
    async getProcesses(): Promise<ProcessInfo[]> {
      live();
      return snapshot().Processes;
    },
    async getLogs(name: LogName): Promise<Logs> {
      live();
      return { name, lines: logLines(name) };
    },
    async getVersion(): Promise<Version> {
      live();
      return { splitter: 'splitter fake', singBox: 'sing-box 1.0.0 (fake)', desync: flags.learning ? '' : 'zapret2 1.0 (fake)', api: 'v1' };
    },
    async connect(): Promise<Ack> {
      live();
      if (state === 'disconnecting') throw new BackendError('busy', 'The engine is busy.');
      if (state !== 'disconnected') return { ok: true, state };
      error = null;
      state = 'connecting';
      emitState();
      later(() => {
        if (state !== 'connecting') return;
        if (scenario === 'permission-denied') return fail({ error: 'Permission was not given.', code: 'permission_denied' });
        if (scenario === 'needs-setup') return fail(NEEDS_SETUP);
        if (!activeServer()) return fail({ error: 'The detour server is not set up or cannot be reached.', code: 'tunnel_missing' });
        state = 'connected';
        emitState();
        startTraffic();
        if (scenario === 'crash') {
          later(() => {
            if (state === 'connected') fail({ error: 'The connection stopped unexpectedly.', code: 'crashed' });
          }, 3000);
        }
      }, 1500);
      return { ok: true, state: 'connecting' };
    },
    async disconnect(): Promise<Ack> {
      live();
      if (state === 'disconnected' || state === 'disconnecting') return { ok: true, state };
      state = 'disconnecting';
      stopTraffic();
      emitState();
      later(() => {
        state = 'disconnected';
        emitState();
      }, 800);
      return { ok: true, state: 'disconnecting' };
    },
    async checkNow(domains?: string[]): Promise<Ack> {
      live();
      if (domains?.length && !domains.some((d) => routes.some((r) => r.domain === d))) {
        throw new BackendError('unknown_domain', 'None of these sites is known yet.');
      }
      checking = true;
      emit({ kind: 'check', data: { running: true, domains: domains ?? null, changed: 0 } });
      emitState();
      later(() => {
        const movable = routes.filter((r) => r.source !== 'user' && (r.route === 'direct' || r.route === 'desync'));
        const pick = movable.length && !domains ? movable[0]! : undefined;
        const changes: Change[] = [];
        if (pick) {
          const to: Route = pick.route === 'direct' ? 'desync' : 'direct';
          const at = new Date().toISOString();
          const ch = { Domain: pick.domain, From: pick.route, To: to, At: at };
          pick.history.push(ch);
          pick.route = to;
          pick.since = at;
          changes.push(ch);
          recent.push(ch);
        }
        lastCheck = new Date().toISOString();
        checking = false;
        if (changes.length) emit({ kind: 'routes', data: { changes } });
        emit({ kind: 'check', data: { running: false, domains: domains ?? null, changed: changes.length } });
        emitState();
      }, 2000);
      return { ok: true, checking: true };
    },
    async shutdown() {
      reset();
      engineRunning = false;
      emit({ kind: 'shutdown', data: {} });
    },
    async listServers() {
      live();
      return servers.map((s) => ({ ...s }));
    },
    async addServer(input: string, name?: string): Promise<Server> {
      live();
      if (!input.trim()) throw new BackendError('bad_request', 'The request needs an input.');
      if (input.length > MAX_INPUT) throw new BackendError('too_large', 'The link or JSON is too large.');
      if (name !== undefined && name !== '' && (name.length > 60 || /[\u0000-\u001f]/.test(name))) throw new BackendError('bad_request', 'The name must be 1 to 60 characters.');
      if (busyNow()) throw new BackendError('busy', 'The engine is busy connecting or disconnecting, or the server is in use. Try again in a moment.');
      let p: Parsed;
      try {
        p = parseInput(input);
      } catch (e) {
        if (e instanceof InputError) throw new BackendError(e.code, e.message);
        throw e;
      }
      if (servers.length >= MAX_SERVERS) throw new BackendError('limit_reached', 'The list is full.');
      if (servers.some((s) => `${s.protocol}|${s.host}|${s.port}` === `${p.protocol}|${p.host}|${p.port}`)) throw new BackendError('duplicate', 'This server is already in the list.');
      const s: Server = {
        id: idFrom(seq++),
        name: name || p.name || `Server ${servers.length + 1}`,
        protocol: p.protocol,
        engine: 'singbox',
        host: p.host,
        port: p.port,
        active: false,
        addedAt: new Date().toISOString(),
      };
      servers.push(s);
      return { ...s };
    },
    async activateServer(id: string): Promise<Activated> {
      live();
      const s = servers.find((x) => x.id === id);
      if (!s) throw new BackendError('not_found', 'Not found.');
      if (busyNow()) throw new BackendError('busy', 'The engine is busy connecting or disconnecting, or the server is in use. Try again in a moment.');
      if (s.active) return { ok: true, active: id, reloaded: false };
      if (state !== 'connected') {
        for (const x of servers) x.active = x.id === id;
        return { ok: true, active: id, reloaded: false };
      }
      error = null;
      state = 'connecting';
      emitState();
      later(() => {
        if (flags.reloadFails) {
          // The old server stays; the error is the failed switch while the state stays connected.
          state = 'connected';
          error = { error: 'The new server could not be used. The previous server is still in place.', code: 'connect_failed' };
        } else {
          for (const x of servers) x.active = x.id === id;
          state = 'connected';
        }
        emitState();
      }, 1200);
      return { ok: true, active: id, reloaded: true };
    },
    async deleteServer(id: string): Promise<Ack> {
      live();
      const s = servers.find((x) => x.id === id);
      if (!s) throw new BackendError('not_found', 'Not found.');
      if (busyNow() || (s.active && state === 'connected')) throw new BackendError('busy', 'The engine is busy connecting or disconnecting, or the server is in use. Try again in a moment.');
      servers = servers.filter((x) => x.id !== id);
      return { ok: true };
    },
    async testServer(req: TestRequest): Promise<ServerTest> {
      live();
      if ((req.id === undefined) === (req.input === undefined)) throw new BackendError('bad_request', 'Send either an id or an input.');
      if (req.id !== undefined) {
        const s = servers.find((x) => x.id === req.id);
        if (!s) throw new BackendError('not_found', 'Not found.');
        return { ok: true, protocol: s.protocol, code: '' };
      }
      if (req.input.length > MAX_INPUT) throw new BackendError('too_large', 'The link or JSON is too large.');
      try {
        const p = parseInput(req.input);
        return { ok: true, protocol: p.protocol, code: '' };
      } catch (e) {
        if (e instanceof InputError) return { ok: false, protocol: '', code: e.code };
        throw e;
      }
    },
    async listSites(): Promise<SiteRule[]> {
      live();
      return sites.map((r) => ({ ...r }));
    },
    async setSite(domain: string, action: SiteAction): Promise<SiteRule> {
      live();
      if (!domain || !action) throw new BackendError('bad_request', 'domain must be a site name such as example.com.');
      const d = normalizeSite(domain);
      if (d === null) throw new BackendError('invalid_input', SITE_INVALID);
      if (!SITE_ACTIONS.includes(action)) throw new BackendError('invalid_input', 'The action must be direct, desync, tunnel or block.');
      const old = sites.find((r) => r.domain === d);
      if (!old && sites.length >= MAX_SITE_RULES) throw new BackendError('limit_reached', 'The list of site rules is full. Remove a rule first.');
      const rule: SiteRule = { domain: d, action, addedAt: old?.addedAt ?? new Date().toISOString() };
      sites = [...sites.filter((r) => r.domain !== d), rule].sort((a, b) => (a.domain < b.domain ? -1 : 1));
      const ch = applyRule(rule, true);
      if (ch) {
        recent.push(ch);
        emit({ kind: 'routes', data: { changes: [ch] } });
      }
      return { ...rule };
    },
    async deleteSite(domain: string): Promise<Ack> {
      live();
      const d = normalizeSite(domain);
      if (d === null) throw new BackendError('invalid_input', SITE_INVALID);
      if (!sites.some((r) => r.domain === d)) throw new BackendError('not_found', 'There is no rule for that site.');
      sites = sites.filter((r) => r.domain !== d);
      const prev = before.get(d);
      before.delete(d);
      const row = routes.find((r) => r.domain === d);
      if (row) {
        const from = row.route;
        if (prev === null || prev === undefined) {
          routes = routes.filter((r) => r.domain !== d);
          emit({ kind: 'routes', data: { changes: [{ Domain: d, From: from, To: '', At: new Date().toISOString() }] } });
        } else {
          row.source = prev.source;
          if (from !== prev.route) {
            const ch: Change = { Domain: d, From: from, To: prev.route, At: new Date().toISOString() };
            row.route = prev.route;
            row.since = ch.At;
            row.history.push(ch);
            recent.push(ch);
            emit({ kind: 'routes', data: { changes: [ch] } });
          }
        }
      }
      return { ok: true };
    },
    async onEvent(handler) {
      handlers.add(handler);
      return () => {
        handlers.delete(handler);
      };
    },
  };
  return backend;
}
