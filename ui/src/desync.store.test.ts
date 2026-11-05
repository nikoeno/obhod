import { describe, expect, it } from 'vitest';
import { BackendError } from './backend/types';
import { makeStore, until } from './test/helpers';

describe('desync in the store', () => {
  it('reads the choice and the catalogue with the first state', async () => {
    const { store } = makeStore();
    await store.init();
    await until(() => store.S.desyncLoaded);
    expect(store.S.desync).toMatchObject({ mode: 'preset', preset: 'alt2-multisplit', searching: false });
    expect(store.S.desyncPresets).toHaveLength(21);
    expect(store.S.desyncFailed).toBe(false);
  });

  it('chooses a preset and automatic mode, and rejects an unknown id', async () => {
    const { store } = makeStore();
    await store.init();
    await until(() => store.S.desyncLoaded);
    await store.setDesync('alt3-hostfakesplit');
    expect(store.S.desync).toMatchObject({ mode: 'preset', preset: 'alt3-hostfakesplit' });
    await store.setDesync('auto');
    expect(store.S.desync!.mode).toBe('auto');
    await expect(store.setDesync('nope')).rejects.toMatchObject({ code: 'invalid_input' });
    expect(store.S.desync!.mode).toBe('auto');
  });

  it('search preconditions come back as their codes', async () => {
    const { store } = makeStore();
    await store.init();
    await until(() => store.S.desyncLoaded);
    await expect(store.searchDesync()).rejects.toMatchObject({ code: 'busy' }); // not connected
    await store.toggleConnection();
    await until(() => store.S.state === 'connected');
    await expect(store.searchDesync()).rejects.toMatchObject({ code: 'not_auto' });
  });

  it('a search ends with a result and verified sites, and can be run again', async () => {
    const { store } = makeStore();
    await store.init();
    await until(() => store.S.desyncLoaded);
    await store.toggleConnection();
    await until(() => store.S.state === 'connected');
    await store.setDesync('auto'); // while connected this starts a search for the waiting sites
    await until(() => store.S.desync?.lastSearch?.outcome === 'chosen' && !store.S.desync.searching);
    expect(store.S.desyncProgress).toBeNull();
    expect(store.S.desync!.preset).toBe(store.S.desync!.lastSearch!.chosen);
    await until(() => store.S.routes.some((r) => r.desync === 'verified'));
    expect(store.S.routes.some((r) => r.desync === 'pending')).toBe(false);
    await store.searchDesync();
    expect(store.S.desync!.searching).toBe(true);
    await until(() => !store.S.desync!.searching);
  });

  it('progress events set and clear the progress', async () => {
    const { store } = makeStore();
    await store.init();
    await until(() => store.S.desyncLoaded);
    store.onEvent({ kind: 'desync', data: { phase: 'searching', total: 6 } });
    expect(store.S.desyncProgress).toEqual({ index: 0, total: 6, preset: '' });
    expect(store.S.desync!.searching).toBe(true);
    store.onEvent({ kind: 'desync', data: { phase: 'trying', index: 3, total: 6, preset: 'alt-fakedsplit' } });
    expect(store.S.desyncProgress).toEqual({ index: 3, total: 6, preset: 'alt-fakedsplit' });
    store.onEvent({ kind: 'desync', data: { phase: 'done', outcome: 'none', chosen: '', fixed: 0, of: 3 } });
    expect(store.S.desyncProgress).toBeNull();
    expect(store.S.desync!.searching).toBe(false);
  });

  it('the engine going away is an engine-down state', async () => {
    const { store, backend } = makeStore();
    await store.init();
    await until(() => store.S.desyncLoaded);
    backend.dev!.setScenario('engine-unreachable');
    await expect(store.setDesync('auto')).rejects.toMatchObject({ code: 'engine_unreachable' });
    expect(store.S.engineDown).toBe(true);
  });

  it('a failed first read marks the tab for a retry, and a retry works', async () => {
    const { store, backend } = makeStore();
    const real = backend.getDesync.bind(backend);
    let fail = true;
    backend.getDesync = async () => {
      if (fail) throw new BackendError('internal', 'Something went wrong.');
      return real();
    };
    await store.init();
    await until(() => store.S.desyncFailed);
    expect(store.S.desyncLoaded).toBe(false);
    fail = false;
    expect(await store.refreshDesync()).toBe(true);
    expect(store.S.desyncFailed).toBe(false);
    expect(store.S.desyncLoaded).toBe(true);
  });
});
