// App state mirrored from the engine. The UI never invents connection states: everything here
// comes from backend calls and engine events (docs/UI.md section 4).
//
// `createStore(backend, deps)` is a factory so tests can run it against the fake backend or a
// mocked invoke layer; the app creates one store and provides it with `provideStore`.

import { inject, reactive, type App, type InjectionKey } from 'vue';
import { BackendError, type Activated, type Backend, type CheckEvent, type ConnState, type EngineEvent, type EngineErrorBody, type EngineState, type NetworkSeen, type Route, type RouteInfo, type Server, type ServerTest, type SiteAction, type SiteRule, type Snapshot, type TestRequest, type Traffic } from './backend/types';
import { mergeChanges } from './lib/activityModel';
import { errorInfo, type ErrorInfo } from './lib/errors';
import { routes as routeCopy } from '../design/copy';

export const SERIES_LEN = 30;
export const SERIES_ROUTES = ['direct', 'tunnel', 'desync', 'proxy'] as const;

export interface StoreState {
  loaded: boolean;
  /** The engine process is not running / not reachable. */
  engineDown: boolean;
  /** Why starting the engine failed (e.g. permission_denied after a cancelled UAC prompt). */
  startError: BackendError | null;
  state: ConnState;
  /** `state.error` from the engine; cleared by the engine on the next Start. */
  error: EngineErrorBody | null;
  checking: boolean;
  checkingDomains: string[] | null;
  snapshot: Snapshot | null;
  networks: NetworkSeen[];
  routes: RouteInfo[];
  servers: Server[];
  serversLoaded: boolean;
  /** The user's per-site rules (`GET /sites`). */
  sites: SiteRule[];
  traffic: { uploadTotal: number; downloadTotal: number; connections: number };
  series: Record<(typeof SERIES_ROUTES)[number], number[]>;
  quit: boolean;
}

export interface StoreDeps {
  /** Shows a confirmation toast. */
  toast: (text: string) => void;
  /** Milliseconds between state reads while a server switch is pending (default 2000). Tests use a small value. */
  pollMs?: number;
}

export function newState(): StoreState {
  return {
    loaded: false,
    engineDown: false,
    startError: null,
    state: 'disconnected',
    error: null,
    checking: false,
    checkingDomains: null,
    snapshot: null,
    networks: [],
    routes: [],
    servers: [],
    serversLoaded: false,
    sites: [],
    traffic: { uploadTotal: 0, downloadTotal: 0, connections: 0 },
    series: { direct: [], tunnel: [], desync: [], proxy: [] },
    quit: false,
  };
}

const routeWord = (r: string): string => routeCopy[r as Route]?.word ?? r;

export type Store = ReturnType<typeof createStore>;

export function createStore(backend: Backend, deps: StoreDeps) {
  const S = reactive<StoreState>(newState());
  const errorListeners = new Set<(info: ErrorInfo) => void>();
  let unlisten: (() => void) | null = null;
  let ticks = 0;

  /** Called when a new error appears in `state.error` (after a failed Start, a crash, a failed switch). */
  function onNewError(fn: (info: ErrorInfo) => void): () => void {
    errorListeners.add(fn);
    return () => errorListeners.delete(fn);
  }

  function applyState(p: EngineState): void {
    const prevState = S.state;
    const prevCode = S.error?.code;
    const prevTunnel = S.snapshot?.Tunnel;
    S.state = p.state;
    S.error = p.error ?? null;
    S.checking = !!p.checking;
    S.snapshot = p.snapshot;
    S.networks = p.networks ?? [];
    S.engineDown = false;
    S.loaded = true;
    if (S.error && (S.error.code !== prevCode || prevState !== S.state)) {
      const info = errorInfo(S.error, S.state);
      if (info) for (const fn of errorListeners) fn(info);
    }
    // The `degraded` flags of the table follow `Tunnel` (a server was added or the first one switched on): no `routes` event says so.
    if (S.loaded && prevTunnel !== p.snapshot?.Tunnel) void refreshRoutes();
    // There is no server event: a failed Start (tunnel_missing) or a switch may have changed the list.
    if (prevState !== S.state) void refreshServers();
  }

  function setEngineDown(): void {
    if (S.engineDown && S.loaded) return;
    S.engineDown = true;
    S.loaded = true;
    S.checking = false;
  }

  /** Reads everything once. False when the engine could not be reached. */
  async function refresh(): Promise<boolean> {
    try {
      const [st, routes, servers, sites] = await Promise.all([backend.getState(), backend.getRoutes(), backend.listServers().catch(() => null), backend.listSites().catch(() => null)]);
      S.routes = routes;
      if (sites) S.sites = sites;
      if (servers) {
        S.servers = servers;
        S.serversLoaded = true;
      }
      applyState(st);
      return true;
    } catch (e) {
      if (e instanceof BackendError && e.code === 'engine_unreachable') setEngineDown();
      return false;
    }
  }

  /** Refetches the server list. Returns it, or null if the call failed. */
  async function refreshServers(): Promise<Server[] | null> {
    try {
      S.servers = await backend.listServers();
      S.serversLoaded = true;
      return S.servers;
    } catch (e) {
      if (e instanceof BackendError && e.code === 'engine_unreachable') setEngineDown();
      return null;
    }
  }

  /** Refetches the rules and the table: a rule that only changes the source of a row sends no `routes` event. */
  async function refreshSites(): Promise<void> {
    const [sites, routes] = await Promise.all([backend.listSites().catch(() => null), backend.getRoutes().catch(() => null)]);
    if (sites) S.sites = sites;
    if (routes) S.routes = routes;
  }

  async function refreshRoutes(): Promise<void> {
    try {
      S.routes = await backend.getRoutes();
    } catch {
      /* the next state event fixes it */
    }
  }

  function onTraffic(t: Traffic): void {
    S.traffic = { uploadTotal: t.uploadTotal, downloadTotal: t.downloadTotal, connections: t.connections };
    if (S.snapshot) {
      S.snapshot.Traffic = t.Traffic;
      S.snapshot.Savings = t.Savings;
    }
    for (const r of SERIES_ROUTES) {
      const s = S.series[r];
      s.push(t.delta?.[r] ?? 0);
      if (s.length > SERIES_LEN) s.shift();
    }
    if (++ticks % 3 === 0) void refreshRoutes();
  }

  function onCheck(c: CheckEvent): void {
    S.checking = c.running;
    S.checkingDomains = c.running ? c.domains : null;
    if (c.running) return;
    if (c.domains && c.domains.length === 1) {
      const d = c.domains[0]!;
      if (!c.changed) {
        const r = S.routes.find((x) => x.domain === d);
        deps.toast(`${d} checked: no change${r ? ` (still ${routeWord(r.route)})` : ''}.`);
      }
    } else {
      deps.toast(c.changed ? `Check finished: ${c.changed} site${c.changed > 1 ? 's' : ''} moved.` : 'Check finished: no changes.');
    }
  }

  function onEvent(ev: EngineEvent): void {
    switch (ev.kind) {
      case 'state':
        applyState(ev.data);
        break;
      case 'routes':
        // The table is refetched; the changes themselves join the recent list at once (a state event may follow later).
        if (S.snapshot && ev.data.changes?.length) S.snapshot.Recent = mergeChanges(S.snapshot.Recent ?? [], ev.data.changes);
        void refreshRoutes();
        break;
      case 'traffic':
        onTraffic(ev.data);
        break;
      case 'check':
        onCheck(ev.data);
        break;
      case 'shutdown':
        // The last event before the engine closes the stream.
        if (!S.quit) setEngineDown();
        break;
      case 'stream-open':
        // The engine keeps no replay buffer: the first state event is the truth; the table is refetched.
        void refreshRoutes();
        break;
      case 'stream-lost':
        // Find out whether the engine is gone; the Rust side keeps reconnecting by itself.
        void refresh();
        break;
    }
  }

  /** Subscribes to events, starts the engine and reads the first state. False if the engine did not start. */
  async function init(): Promise<boolean> {
    S.quit = false;
    unlisten?.();
    unlisten = await backend.onEvent(onEvent);
    try {
      await backend.engineStart();
    } catch (e) {
      S.startError = e instanceof BackendError ? e : new BackendError('internal', 'Something went wrong.');
      setEngineDown();
      return false;
    }
    S.startError = null;
    return refresh();
  }

  function dispose(): void {
    unlisten?.();
    unlisten = null;
  }

  // ---------- actions ----------

  function actionFailed(e: unknown): void {
    if (e instanceof BackendError) {
      if (e.code === 'engine_unreachable') {
        setEngineDown();
        return;
      }
      deps.toast(e.message || 'Something went wrong.');
      return;
    }
    deps.toast('Something went wrong.');
  }

  /** Stops the engine and starts a new one. `quiet` skips the confirmation toast. */
  async function restartEngine(quiet = false): Promise<boolean> {
    try {
      await backend.engineRestart();
      S.startError = null;
      if (!quiet) deps.toast('Engine restarted.');
      return refresh();
    } catch (e) {
      S.startError = e instanceof BackendError ? e : new BackendError('internal', 'Something went wrong.');
      setEngineDown();
      return false;
    }
  }

  /** Start / Stop. With the engine down (e.g. permission was refused), Start launches the engine first. */
  async function toggleConnection(): Promise<void> {
    if (S.quit) return;
    try {
      if (S.engineDown) {
        // Only a refused permission is retried from the button; other failures use "Restart engine".
        if (S.startError?.code !== 'permission_denied') return;
        if (!(await restartEngine(true))) return;
        await backend.connect();
        return;
      }
      if (S.state === 'connected') await backend.disconnect();
      else if (S.state === 'disconnected') await backend.connect();
    } catch (e) {
      actionFailed(e);
    }
  }

  async function checkNow(domains?: string[]): Promise<void> {
    if (S.engineDown) return;
    try {
      S.checking = true;
      S.checkingDomains = domains ?? null;
      await backend.checkNow(domains);
    } catch (e) {
      S.checking = false;
      actionFailed(e);
    }
  }

  async function guarded<T>(p: Promise<T>): Promise<T> {
    try {
      return await p;
    } catch (e) {
      if (e instanceof BackendError && e.code === 'engine_unreachable') setEngineDown();
      throw e;
    }
  }

  // Server calls throw the BackendError; the caller shows serverErrorText(e, ctx) where the user is looking.

  /** Adds a server (the engine never activates it). */
  async function addServer(input: string, name?: string): Promise<Server> {
    const s = await guarded(backend.addServer(input, name));
    await refreshServers();
    return s;
  }

  /** Makes a server the active one. `reloaded` is true while connected (the tunnel restarts once). */
  async function activateServer(id: string): Promise<Pick<Activated, 'reloaded'>> {
    const r = await guarded(backend.activateServer(id));
    await refreshServers();
    if (r.reloaded) void watchTunnel();
    else await refreshTunnel();
    return { reloaded: !!r.reloaded };
  }

  async function removeServer(id: string): Promise<void> {
    await guarded(backend.deleteServer(id));
    await refreshServers();
    await refreshTunnel();
  }

  /** Reads the state and the table again: `Tunnel` and the `degraded` flags change with the server list. */
  async function refreshTunnel(): Promise<void> {
    try {
      const [st, routes] = await Promise.all([backend.getState(), backend.getRoutes()]);
      S.routes = routes;
      applyState(st);
    } catch (e) {
      if (e instanceof BackendError && e.code === 'engine_unreachable') setEngineDown();
    }
  }

  /**
   * After a switch while connected the engine restarts sing-box (up to 30 s) and only then reports
   * `Tunnel: "ready"`. Reads the state now and every `pollMs` until it flips, the app stops, or about 45 s pass.
   */
  async function watchTunnel(): Promise<void> {
    const every = deps.pollMs ?? 2000;
    const deadline = Date.now() + 45000;
    for (;;) {
      await refreshTunnel();
      const switching = S.state === 'connecting' || S.state === 'connected';
      if (S.engineDown || !switching || S.error || S.snapshot?.Tunnel !== 'none' || Date.now() > deadline || S.quit) return;
      await new Promise((r) => setTimeout(r, every));
    }
  }

  function testServer(req: TestRequest): Promise<ServerTest> {
    return guarded(backend.testServer(req));
  }

  // Site calls throw the BackendError; the dialog shows siteErrorText(e) where the user is looking.

  /** Adds or changes the rule of a site, then reads the rules and the table again. */
  async function setSite(domain: string, action: SiteAction): Promise<SiteRule> {
    const rule = await guarded(backend.setSite(domain, action));
    await refreshSites();
    return rule;
  }

  /** Removes the rule of a site (the app decides again). A rule that is already gone refreshes the lists, then throws `not_found`. */
  async function removeSite(domain: string): Promise<void> {
    try {
      await guarded(backend.deleteSite(domain));
    } catch (e) {
      if (e instanceof BackendError && e.code === 'not_found') await refreshSites();
      throw e;
    }
    await refreshSites();
  }

  /** Advanced settings: stop and start again so the engine picks up the change. */
  async function restartConnection(): Promise<boolean> {
    if (S.state !== 'connected') return false;
    await backend.disconnect();
    await waitFor((s) => s === 'disconnected', 5000);
    await backend.connect();
    return true;
  }

  function waitFor(pred: (s: ConnState) => boolean, ms: number): Promise<boolean> {
    return new Promise((resolve) => {
      const started = Date.now();
      const tick = (): void => {
        if (pred(S.state)) resolve(true);
        else if (Date.now() - started > ms) resolve(false);
        else setTimeout(tick, 50);
      };
      tick();
    });
  }

  async function quit(): Promise<void> {
    S.quit = true; // first, so the engine's final shutdown event is not shown as "engine not running"
    try {
      await backend.shutdown();
    } catch {
      /* quitting anyway */
    }
    dispose();
  }

  const routeCount = (r: Route): number => S.snapshot?.Routes?.[r] ?? 0;
  const routeTraffic = (r: Route): number => S.snapshot?.Traffic?.[r] ?? 0;
  const activeServer = (): Server | null => S.servers.find((s) => s.active) ?? null;

  return {
    S,
    backend,
    init,
    dispose,
    refresh,
    refreshServers,
    refreshSites,
    onNewError,
    toggleConnection,
    checkNow,
    restartEngine,
    restartConnection,
    addServer,
    activateServer,
    removeServer,
    testServer,
    setSite,
    removeSite,
    quit,
    routeCount,
    routeTraffic,
    activeServer,
    // exposed for tests
    onEvent,
  };
}

const key: InjectionKey<Store> = Symbol('obhod-store');

export function provideStore(app: App, store: Store): void {
  app.provide(key, store);
}

export function useStore(): Store {
  const s = inject(key);
  if (!s) throw new Error('store not provided');
  return s;
}
