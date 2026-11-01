// The contract between the Vue app and the Rust side (src-tauri/src/commands.rs). The webview only
// ever sees these shapes; the engine's credentials and HTTP details stay in Rust.
// Payload field names follow api/desktop/API.md (the Go names of status.Snapshot are kept as they are).

import type { Route } from '../../design/tokens';
import type { EngineState as ConnState } from '../../design/copy';

export type { Route, ConnState };

export interface Change { Domain: string; From: string; To: string; At: string }
export interface ProcessInfo { Name: string; State: string; Restarts: number }

export interface Snapshot {
  Version: string;
  State: ConnState;
  Network: string;
  LastCheck: string;
  NextCheck: string;
  Routes: Partial<Record<Route, number>>;
  Traffic: Partial<Record<Route, number>>;
  Savings: number;
  Processes: ProcessInfo[];
  Warnings: string[];
  Recent: Change[];
  /**
   * `ready` when a detour server is in use, `none` when there is none (degraded mode: sites on the
   * Blocked path are served directly). Added by the engine 2026-11-01 (decision 34); absent from an
   * older engine, which the UI reads as `ready`.
   */
  Tunnel?: TunnelState;
}

export type TunnelState = 'ready' | 'none';

/** `state.error` as the engine sends it. */
export interface EngineErrorBody { error: string; code: string; commands?: string[] }

export interface NetworkSeen { label: string; since: string }

export interface EngineState {
  state: ConnState;
  error: EngineErrorBody | null;
  checking: boolean;
  snapshot: Snapshot;
  networks: NetworkSeen[];
}

export interface RouteInfo {
  domain: string;
  route: Route;
  source: string;
  since: string;
  lastCheck: string;
  history: Change[];
  /** True for a Blocked-path site while there is no detour server: it is served directly. Absent from an older engine. */
  degraded?: boolean;
}

export interface Version { splitter: string; singBox: string; desync: string; api: string }
export type LogName = 'splitter' | 'sing-box' | 'desync';
export interface Logs { name: string; lines: string[] }

export interface Server {
  id: string;
  name: string;
  protocol: string;
  engine: string;
  host: string;
  port: number;
  active: boolean;
  addedAt: string;
}
export interface ServerTest { ok: boolean; protocol: string; code: '' | 'invalid_input' | 'unsupported_protocol' }
export interface Activated { ok: boolean; active: string; reloaded: boolean }
export interface Ack { ok: boolean; state?: ConnState; checking?: boolean }
/** Exactly one of the two. */
export type TestRequest = { id: string; input?: undefined } | { input: string; id?: undefined };

/** The four things a rule can do. "No rule" is made by deleting the rule. */
export type SiteAction = 'direct' | 'desync' | 'tunnel' | 'block';
export interface SiteRule { domain: string; action: SiteAction; addedAt: string }

export interface Traffic {
  uploadTotal: number;
  downloadTotal: number;
  connections: number;
  delta: Partial<Record<Route, number>>;
  Traffic: Partial<Record<Route, number>>;
  Savings: number;
  At: string;
}
export interface CheckEvent { running: boolean; domains: string[] | null; changed: number }

/** What arrives on the `engine-event` Tauri event. `stream-*` are local: the event stream came back or broke. */
export type EngineEvent =
  | { kind: 'state'; data: EngineState }
  | { kind: 'routes'; data: { changes: Change[] } }
  | { kind: 'check'; data: CheckEvent }
  | { kind: 'traffic'; data: Traffic }
  | { kind: 'shutdown'; data: Record<string, never> }
  | { kind: 'stream-open'; data: Record<string, never> }
  | { kind: 'stream-lost'; data: { code?: string } };

/** A failed call. `code` is a contract code, or one the Rust side adds (`permission_denied`, `engine_unreachable`). */
export class BackendError extends Error {
  readonly code: string;
  readonly commands?: string[];
  /** Only for a failed engine start: sanitized facts and the engine's own error text (shown under "Details"). */
  readonly details?: string;
  constructor(code: string, message: string, commands?: string[], details?: string) {
    super(message);
    this.name = 'BackendError';
    this.code = code;
    this.commands = commands;
    this.details = details;
  }
}

/** Scenarios of the dev-only fake backend. */
export type Scenario = 'normal' | 'no-server' | 'tunnel-missing' | 'permission-denied' | 'needs-setup' | 'crash' | 'engine-unreachable';
export type DevFlag = 'warnings' | 'extras' | 'learning' | 'reloadFails';

/** Dev-only controls; only the fake backend provides them. */
export interface DevControls {
  scenario(): Scenario;
  setScenario(s: Scenario): void;
  setFlag(key: DevFlag, value: boolean): void;
  flags(): Record<DevFlag, boolean>;
}

/**
 * Everything the UI can ask of the engine. The production implementation is Tauri commands; the
 * dev/test implementation is `src/dev/fakeBackend.ts`. Failed calls reject with `BackendError`.
 */
export interface Backend {
  uiVersion(): Promise<string>;
  /** Starts the engine (UAC / pkexec) unless it runs. Rejects with `permission_denied` if refused. */
  engineStart(): Promise<void>;
  engineRestart(): Promise<void>;
  getState(): Promise<EngineState>;
  getRoutes(): Promise<RouteInfo[]>;
  getProcesses(): Promise<ProcessInfo[]>;
  getLogs(name: LogName): Promise<Logs>;
  getVersion(): Promise<Version>;
  connect(): Promise<Ack>;
  disconnect(): Promise<Ack>;
  checkNow(domains?: string[]): Promise<Ack>;
  /** Stops the engine and quits the app. */
  shutdown(): Promise<void>;
  listServers(): Promise<Server[]>;
  addServer(input: string, name?: string): Promise<Server>;
  activateServer(id: string): Promise<Activated>;
  deleteServer(id: string): Promise<Ack>;
  testServer(request: TestRequest): Promise<ServerTest>;
  /** The user's per-site rules, sorted by domain. */
  listSites(): Promise<SiteRule[]>;
  /** Adds or changes a rule. The domain must already be a plain name (the UI cleans pasted input). */
  setSite(domain: string, action: SiteAction): Promise<SiteRule>;
  /** Removes a rule (back to automatic). Rejects with `not_found` when there is none. */
  deleteSite(domain: string): Promise<Ack>;
  /** Subscribes to engine events; resolves to an unsubscribe function. */
  onEvent(handler: (event: EngineEvent) => void): Promise<() => void>;
  dev?: DevControls;
}
