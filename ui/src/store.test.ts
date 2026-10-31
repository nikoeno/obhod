import { describe, expect, it } from 'vitest';
import { BackendError, type EngineEvent } from './backend/types';
import { flush, makeStore, until } from './test/helpers';

describe('store with the fake backend', () => {
  it('starts the engine, loads state, routes and servers', async () => {
    const { store } = makeStore();
    expect(await store.init()).toBe(true);
    const S = store.S;
    expect(S.loaded).toBe(true);
    expect(S.engineDown).toBe(false);
    expect(S.state).toBe('disconnected');
    expect(S.routes.length).toBeGreaterThan(5);
    expect(S.servers).toHaveLength(2);
    expect(store.activeServer()?.name).toBe('Server 1');
    expect(store.routeCount('direct')).toBeGreaterThan(0);
  });

  it('Start goes disconnected -> connecting -> connected and Stop goes back', async () => {
    const { store } = makeStore();
    await store.init();
    await store.toggleConnection();
    expect(store.S.state).toBe('connecting');
    await until(() => store.S.state === 'connected');
    await store.toggleConnection();
    await until(() => store.S.state === 'disconnected');
    expect(store.S.error).toBeNull();
  });

  it('permission_denied after Start shows up as state.error and notifies once', async () => {
    const { store, backend } = makeStore({ scenario: 'permission-denied' });
    const seen: string[] = [];
    store.onNewError((i) => seen.push(i.code));
    await store.init();
    await store.toggleConnection();
    await until(() => store.S.error?.code === 'permission_denied');
    expect(store.S.state).toBe('disconnected');
    expect(seen).toEqual(['permission_denied']);
    // Trying again clears the error on the next Start (the engine does that).
    backend.dev!.setScenario('normal');
    await store.toggleConnection();
    expect(store.S.error).toBeNull();
    await until(() => store.S.state === 'connected');
  });

  it('needs_setup carries its commands to the error listener', async () => {
    const { store } = makeStore({ scenario: 'needs-setup' });
    const got: Array<string[] | undefined> = [];
    store.onNewError((i) => got.push(i.commands));
    await store.init();
    await store.toggleConnection();
    await until(() => got.length === 1);
    expect(got[0]).toEqual(expect.arrayContaining([expect.stringContaining('nft add table inet splitter')]));
  });

  it('no server: Start fails with tunnel_missing and the list is empty', async () => {
    const { store } = makeStore({ scenario: 'no-server' });
    await store.init();
    expect(store.S.servers).toEqual([]);
    expect(store.S.serversLoaded).toBe(true);
    await store.toggleConnection();
    await until(() => store.S.error?.code === 'tunnel_missing');
  });

  it('a crash while connected drops to disconnected with crashed', async () => {
    const { store } = makeStore({ scenario: 'crash' });
    await store.init();
    await store.toggleConnection();
    await until(() => store.S.error?.code === 'crashed');
    expect(store.S.state).toBe('disconnected');
  });

  it('engine unreachable: engineDown, Start does nothing, Restart engine recovers', async () => {
    const { store } = makeStore({ scenario: 'engine-unreachable' });
    expect(await store.init()).toBe(false);
    expect(store.S.engineDown).toBe(true);
    expect(store.S.startError?.code).toBe('engine_unreachable');
    await store.toggleConnection(); // not a refused permission: nothing happens
    expect(store.S.state).toBe('disconnected');
    expect(await store.restartEngine()).toBe(true);
    expect(store.S.engineDown).toBe(false);
    expect(store.S.startError).toBeNull();
  });

  it('losing the stream probes the engine and marks it down when it is gone', async () => {
    const { store, backend } = makeStore();
    await store.init();
    backend.dev!.setScenario('engine-unreachable'); // emits stream-lost
    await until(() => store.S.engineDown);
    backend.dev!.setScenario('normal'); // emits stream-open + state
    await until(() => !store.S.engineDown);
  });

  it('a refused permission at engine start is retried from the Start button', async () => {
    const { store, backend } = makeStore();
    const restart = backend.engineRestart.bind(backend);
    backend.engineStart = async () => {
      throw new BackendError('permission_denied', 'Permission was not given.');
    };
    expect(await store.init()).toBe(false);
    expect(store.S.engineDown).toBe(true);
    expect(store.S.startError?.code).toBe('permission_denied');
    backend.engineRestart = restart; // the user accepts the prompt this time
    await store.toggleConnection();
    expect(store.S.engineDown).toBe(false);
    expect(store.S.startError).toBeNull();
    await until(() => store.S.state === 'connected');
  });

  it('check events set the checking flag and toast the result', async () => {
    const { store, toasts } = makeStore();
    await store.init();
    await store.checkNow();
    expect(store.S.checking).toBe(true);
    await until(() => !store.S.checking);
    expect(toasts.some((t) => t.startsWith('Check finished'))).toBe(true);
  });

  it('traffic events update the snapshot and keep a bounded series', async () => {
    const { store } = makeStore();
    await store.init();
    for (let i = 0; i < 40; i++) {
      const ev: EngineEvent = {
        kind: 'traffic',
        data: { uploadTotal: i, downloadTotal: i * 2, connections: 3, delta: { direct: 5 }, Traffic: { direct: 100 + i }, Savings: 0.5, At: '2026-11-01T00:00:00Z' },
      };
      store.onEvent(ev);
    }
    expect(store.S.series.direct).toHaveLength(30);
    expect(store.S.snapshot?.Traffic.direct).toBe(139);
    expect(store.S.traffic.connections).toBe(3);
  });

  it('the engine shutdown event marks it down unless we are quitting', async () => {
    const { store } = makeStore();
    await store.init();
    store.onEvent({ kind: 'shutdown', data: {} });
    expect(store.S.engineDown).toBe(true);
  });

  it('quit asks the backend to shut down and shows the quit state, not "engine not running"', async () => {
    const { store } = makeStore();
    await store.init();
    await store.quit();
    expect(store.S.quit).toBe(true);
    await flush();
    expect(store.S.engineDown).toBe(false);
  });
});

describe('server management in the store', () => {
  const LINK = 'vless://00000000-0000-4000-8000-000000000001@example.com:443?security=tls#Demo';

  it('adds a server without activating it, refreshes the list, then activates it', async () => {
    const { store } = makeStore();
    await store.init();
    const s = await store.addServer(LINK);
    expect(s.name).toBe('Demo');
    expect(s.active).toBe(false);
    expect(store.S.servers).toHaveLength(3);
    expect(store.activeServer()?.name).toBe('Server 1');
    const r = await store.activateServer(s.id);
    expect(r.reloaded).toBe(false);
    expect(store.activeServer()?.id).toBe(s.id);
  });

  it('activating while connected reloads and flickers through connecting', async () => {
    const { store } = makeStore();
    await store.init();
    await store.toggleConnection();
    await until(() => store.S.state === 'connected');
    const other = store.S.servers.find((s) => !s.active)!;
    const r = await store.activateServer(other.id);
    expect(r.reloaded).toBe(true);
    expect(store.S.state).toBe('connecting');
    await until(() => store.S.state === 'connected');
    expect(store.activeServer()?.id).toBe(other.id);
  });

  it('a failed switch keeps the old server and sets connect_failed while connected', async () => {
    const { store, backend } = makeStore();
    await store.init();
    await store.toggleConnection();
    await until(() => store.S.state === 'connected');
    backend.dev!.setFlag('reloadFails', true);
    const old = store.activeServer()!;
    const other = store.S.servers.find((s) => !s.active)!;
    await store.activateServer(other.id);
    await until(() => store.S.error?.code === 'connect_failed');
    expect(store.S.state).toBe('connected');
    expect(store.activeServer()?.id).toBe(old.id);
  });

  it('removing the active server while connected is busy; other servers can go', async () => {
    const { store } = makeStore();
    await store.init();
    await store.toggleConnection();
    await until(() => store.S.state === 'connected');
    await expect(store.removeServer(store.activeServer()!.id)).rejects.toMatchObject({ code: 'busy' });
    const other = store.S.servers.find((s) => !s.active)!;
    await store.removeServer(other.id);
    expect(store.S.servers).toHaveLength(1);
  });

  it('rejects bad input with the engine codes and never echoes the input', async () => {
    const { store } = makeStore();
    await store.init();
    const secret = 'MARKER-SECRET-9876';
    for (const [input, code] of [
      ['', 'bad_request'],
      [`vless://${secret}@example.com`, 'invalid_input'],
      [`gopher://${secret}@example.com:70`, 'unsupported_protocol'],
      [`vless://${secret}@example.com:443?type=xhttp`, 'unsupported_protocol'],
      ['x'.repeat(40000), 'too_large'],
    ] as const) {
      const e = await store.addServer(input).catch((x: BackendError) => x);
      expect(e).toBeInstanceOf(BackendError);
      expect((e as BackendError).code).toBe(code);
      expect((e as BackendError).message).not.toContain(secret);
    }
    await store.addServer(LINK);
    await expect(store.addServer(LINK)).rejects.toMatchObject({ code: 'duplicate' });
  });

  it('stops at 20 servers', async () => {
    const { store } = makeStore();
    await store.init();
    for (let i = store.S.servers.length; i < 20; i++) await store.addServer(`trojan://pw@192.0.2.${i + 1}:443`);
    await expect(store.addServer('trojan://pw@192.0.2.200:443')).rejects.toMatchObject({ code: 'limit_reached' });
  });

  it('test is offline and adds nothing', async () => {
    const { store } = makeStore();
    await store.init();
    expect(await store.testServer({ input: LINK })).toEqual({ ok: true, protocol: 'vless', code: '' });
    expect(await store.testServer({ input: 'hello' })).toMatchObject({ ok: false, code: 'invalid_input' });
    expect(store.S.servers).toHaveLength(2);
  });

  it('server calls while the engine is gone mark it down', async () => {
    const { store, backend } = makeStore();
    await store.init();
    backend.dev!.setScenario('engine-unreachable');
    await expect(store.addServer(LINK)).rejects.toMatchObject({ code: 'engine_unreachable' });
    expect(store.S.engineDown).toBe(true);
  });
});
