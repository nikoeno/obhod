// The production backend: Tauri commands and one event. This is the only file that imports
// @tauri-apps/api. No credentials appear here: they live in the Rust process.

import { invoke } from '@tauri-apps/api/core';
import { listen } from '@tauri-apps/api/event';
import {
  BackendError,
  type Ack, type Activated, type Backend, type EngineEvent, type EngineState, type LogName, type Logs,
  type ProcessInfo, type RouteInfo, type Server, type ServerTest, type TestRequest, type Version,
} from './types';

/** Turns whatever `invoke` rejects with into a `BackendError`. Rust sends `{code, message, commands?}`. */
export function toBackendError(e: unknown): BackendError {
  if (e instanceof BackendError) return e;
  if (e && typeof e === 'object') {
    const o = e as { code?: unknown; message?: unknown; commands?: unknown };
    if (typeof o.code === 'string') {
      const commands = Array.isArray(o.commands) ? o.commands.filter((c): c is string => typeof c === 'string') : undefined;
      return new BackendError(o.code, typeof o.message === 'string' ? o.message : '', commands);
    }
  }
  return new BackendError('internal', 'Something went wrong.');
}

export type Run = <T>(cmd: string, args?: Record<string, unknown>) => Promise<T>;

const defaultRun: Run = async <T>(cmd: string, args?: Record<string, unknown>): Promise<T> => {
  try {
    return await invoke<T>(cmd, args);
  } catch (e) {
    throw toBackendError(e);
  }
};

/** `run` and `listenFn` are injectable so tests can use a mocked invoke layer. */
export function createTauriBackend(run: Run = defaultRun, listenFn: typeof listen = listen): Backend {
  return {
    uiVersion: () => run<string>('ui_version'),
    engineStart: () => run<void>('engine_start'),
    engineRestart: () => run<void>('engine_restart'),
    getState: () => run<EngineState>('api_state'),
    getRoutes: () => run<RouteInfo[]>('api_routes'),
    getProcesses: () => run<ProcessInfo[]>('api_processes'),
    getLogs: (name: LogName) => run<Logs>('api_logs', { name }),
    getVersion: () => run<Version>('api_version'),
    connect: () => run<Ack>('api_connect'),
    disconnect: () => run<Ack>('api_disconnect'),
    checkNow: (domains?: string[]) => run<Ack>('api_check_now', { domains: domains && domains.length ? domains : null }),
    shutdown: () => run<void>('api_shutdown'),
    listServers: () => run<Server[]>('servers_list'),
    addServer: (input: string, name?: string) => run<Server>('servers_add', { input, name: name ? name : null }),
    activateServer: (id: string) => run<Activated>('servers_activate', { id }),
    deleteServer: (id: string) => run<Ack>('servers_delete', { id }),
    testServer: (request: TestRequest) => run<ServerTest>('servers_test', { request }),
    onEvent: (handler) => listenFn<EngineEvent>('engine-event', (ev) => handler(ev.payload)),
  };
}
