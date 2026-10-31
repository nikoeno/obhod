import { describe, expect, it } from 'vitest';
import { createTauriBackend, toBackendError, type Run } from './tauri';
import { BackendError } from './types';

type Call = { cmd: string; args?: Record<string, unknown> };

function mocked(answers: Record<string, unknown> = {}) {
  const calls: Call[] = [];
  const run: Run = async <T>(cmd: string, args?: Record<string, unknown>) => {
    calls.push({ cmd, args });
    const a = answers[cmd];
    if (a instanceof Error) throw a;
    return a as T;
  };
  return { calls, backend: createTauriBackend(run) };
}

describe('the mocked invoke layer', () => {
  it('maps every method to its Rust command with camelCase arguments', async () => {
    const { calls, backend } = mocked();
    await backend.uiVersion();
    await backend.engineStart();
    await backend.engineRestart();
    await backend.getState();
    await backend.getRoutes();
    await backend.getProcesses();
    await backend.getLogs('sing-box');
    await backend.getVersion();
    await backend.connect();
    await backend.disconnect();
    await backend.checkNow();
    await backend.checkNow(['a.example']);
    await backend.shutdown();
    await backend.listServers();
    await backend.addServer('vless://x', 'Mine');
    await backend.addServer('vless://x');
    await backend.activateServer('0000000000000001');
    await backend.deleteServer('0000000000000001');
    await backend.testServer({ input: 'vless://x' });
    await backend.testServer({ id: '0000000000000001' });
    expect(calls).toEqual([
      { cmd: 'ui_version' },
      { cmd: 'engine_start' },
      { cmd: 'engine_restart' },
      { cmd: 'api_state' },
      { cmd: 'api_routes' },
      { cmd: 'api_processes' },
      { cmd: 'api_logs', args: { name: 'sing-box' } },
      { cmd: 'api_version' },
      { cmd: 'api_connect' },
      { cmd: 'api_disconnect' },
      { cmd: 'api_check_now', args: { domains: null } },
      { cmd: 'api_check_now', args: { domains: ['a.example'] } },
      { cmd: 'api_shutdown' },
      { cmd: 'servers_list' },
      { cmd: 'servers_add', args: { input: 'vless://x', name: 'Mine' } },
      { cmd: 'servers_add', args: { input: 'vless://x', name: null } },
      { cmd: 'servers_activate', args: { id: '0000000000000001' } },
      { cmd: 'servers_delete', args: { id: '0000000000000001' } },
      { cmd: 'servers_test', args: { request: { input: 'vless://x' } } },
      { cmd: 'servers_test', args: { request: { id: '0000000000000001' } } },
    ]);
  });

  it('never sends a token or any authorization argument', async () => {
    const { calls, backend } = mocked();
    await backend.getState();
    await backend.addServer('vless://x', 'n');
    expect(JSON.stringify(calls).toLowerCase()).not.toMatch(/token|authorization|bearer/);
  });

  it('returns what the command returns', async () => {
    const { backend } = mocked({ api_version: { splitter: 'splitter 1', singBox: '', desync: '', api: 'v1' } });
    expect((await backend.getVersion()).splitter).toBe('splitter 1');
  });
});

describe('toBackendError', () => {
  it('keeps code, message and commands from the Rust error', () => {
    const e = toBackendError({ code: 'needs_setup', message: 'Setup.', commands: ['a', 3, 'b'] });
    expect(e).toBeInstanceOf(BackendError);
    expect(e).toMatchObject({ code: 'needs_setup', message: 'Setup.', commands: ['a', 'b'] });
  });
  it('turns anything else into a generic internal error without leaking it', () => {
    for (const odd of ['boom with secret', 42, null, undefined, { nope: true }, new Error('inner detail')]) {
      const e = toBackendError(odd);
      expect(e.code).toBe('internal');
      expect(e.message).toBe('Something went wrong.');
    }
  });
  it('passes a BackendError through', () => {
    const e = new BackendError('busy', 'x');
    expect(toBackendError(e)).toBe(e);
  });
});

describe('engine events', () => {
  it('listens on engine-event and hands over the payload only', async () => {
    let handler: ((ev: { payload: unknown }) => void) | undefined;
    let name = '';
    let unlistened = false;
    const listenFn = (async (n: string, h: (ev: { payload: unknown }) => void) => {
      name = n;
      handler = h;
      return () => {
        unlistened = true;
      };
    }) as never;
    const backend = createTauriBackend(async () => undefined as never, listenFn);
    const got: unknown[] = [];
    const off = await backend.onEvent((e) => got.push(e));
    expect(name).toBe('engine-event');
    handler!({ payload: { kind: 'state', data: { state: 'connected' } } });
    expect(got).toEqual([{ kind: 'state', data: { state: 'connected' } }]);
    off();
    expect(unlistened).toBe(true);
  });
});
