import { describe, expect, it } from 'vitest';
import { BackendError } from '../backend/types';
import { flush, makeStore } from '../test/helpers';
import { createAddServerModel } from './addServerModel';

const LINK = 'trojan://secret-pass@198.51.100.20:443#Mine';

async function setup() {
  const { store, toasts } = makeStore();
  await store.init();
  const model = createAddServerModel(store, (t) => toasts.push(t));
  return { store, toasts, model };
}

describe('Add server dialog logic', () => {
  it('asks to paste first and sends nothing', async () => {
    const { model, store } = await setup();
    await model.add();
    expect(model.msg).toEqual({ text: 'Paste the link or JSON from your provider first.', kind: 'err' });
    await model.test();
    expect(model.msg.kind).toBe('err');
    expect(store.S.servers).toHaveLength(2);
  });

  it('Test is offline: says what it is, adds nothing, keeps the text', async () => {
    const { model, store } = await setup();
    model.input = LINK;
    await model.test();
    expect(model.msg.kind).toBe('ok');
    expect(model.msg.text).toBe('Looks fine: a Trojan server. This check runs on this computer only; the connection itself is not tried.');
    expect(model.input).toBe(LINK);
    expect(store.S.servers).toHaveLength(2);
  });

  it('Test reports invalid and unsupported inputs in the UI words', async () => {
    const { model } = await setup();
    model.input = 'not a link';
    await model.test();
    expect(model.msg).toEqual({ kind: 'err', text: 'This does not look like a valid server. Check that you pasted the whole link.' });
    model.input = 'gopher://x@example.com:70';
    await model.test();
    expect(model.msg.text).toBe('This kind of server is not supported.');
  });

  it('Add stores the server inactive, clears the pasted text and toasts', async () => {
    const { model, store, toasts } = await setup();
    model.input = LINK;
    model.name = '  Home server ';
    const s = await model.add();
    expect(s?.name).toBe('Home server');
    expect(s?.active).toBe(false);
    expect(model.input).toBe('');
    expect(model.name).toBe('');
    expect(toasts).toContain('Home server added. Make it active to use it.');
    expect(store.S.servers).toHaveLength(3);
  });

  it("Add shows the engine's own sentence inline and keeps the dialog data", async () => {
    const { model } = await setup();
    model.input = 'trojan://pw@198.51.100.20';
    expect(await model.add()).toBeNull();
    expect(model.msg).toEqual({ text: 'The link has no port.', kind: 'err' });
    expect(model.input).toBe('trojan://pw@198.51.100.20');
    model.edited();
    expect(model.msg.kind).toBe('');
  });

  it('maps duplicate, too_large and bad name to the UI sentences', async () => {
    const { model } = await setup();
    model.input = LINK;
    await model.add();
    model.input = LINK;
    await model.add();
    expect(model.msg.text).toBe('This server is already in the list.');
    model.input = 'x'.repeat(40000);
    await model.add();
    expect(model.msg.text).toBe('The link or JSON is too large.');
    model.input = 'trojan://pw@198.51.100.21:443';
    model.name = 'n'.repeat(61);
    await model.add();
    expect(model.msg.text).toBe('The name must be 1 to 60 characters.');
  });

  it('ignores a second click while busy', async () => {
    const { model, store } = await setup();
    model.input = LINK;
    const a = model.add();
    const b = model.add();
    await Promise.all([a, b]);
    await flush();
    expect(store.S.servers).toHaveLength(3);
  });

  it('never keeps the secret after clear()', async () => {
    const { model } = await setup();
    model.input = LINK;
    model.name = 'x';
    model.clear();
    expect(JSON.stringify(model)).not.toContain('secret-pass');
  });

  it('engine errors with no sentence fall back to a generic one', async () => {
    const { store, toasts } = makeStore();
    await store.init();
    store.testServer = async () => {
      throw new BackendError('internal', '');
    };
    const model = createAddServerModel(store, (t) => toasts.push(t));
    model.input = LINK;
    await model.test();
    expect(model.msg).toEqual({ text: 'Something went wrong.', kind: 'err' });
  });
});
