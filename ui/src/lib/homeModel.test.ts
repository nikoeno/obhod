import { describe, expect, it } from 'vitest';
import type { Server } from '../backend/types';
import { buttonModel, setupCard, startDetails, subAction, type HomeInput } from './homeModel';

const base: HomeInput = { engineDown: false, startError: null, state: 'disconnected', error: null, snapshot: { Savings: 0.78 } };
const srv = (o: Partial<Server> = {}): Server => ({ id: '0000000000000001', name: 'S', protocol: 'vless', engine: 'singbox', host: 'h', port: 1, active: false, addedAt: '', ...o });

describe('buttonModel', () => {
  it('follows the state table of docs/UI.md section 4', () => {
    expect(buttonModel(base)).toMatchObject({ look: 'idle', label: 'Start', disabled: false, sub: 'Off. Your traffic goes as usual.' });
    expect(buttonModel({ ...base, state: 'connecting' })).toMatchObject({ look: 'busy', label: 'Starting…', disabled: true });
    expect(buttonModel({ ...base, state: 'connected' })).toMatchObject({ look: 'on', label: 'Stop', disabled: false, sub: 'On. 78% of your traffic stays off the detour.' });
    expect(buttonModel({ ...base, state: 'disconnecting' })).toMatchObject({ look: 'busy-dim', label: 'Stopping…', disabled: true, sub: '' });
  });

  it('names the state in the accessible name', () => {
    expect(buttonModel(base).name).toBe('Start, currently off');
    expect(buttonModel({ ...base, state: 'connected' }).name).toBe('Stop, currently on');
  });

  it('shows Try again with the sentence after a failure', () => {
    const m = buttonModel({ ...base, error: { code: 'permission_denied', error: 'Permission was not given.' } });
    expect(m).toMatchObject({ look: 'idle', label: 'Try again', sub: 'Permission was not given.', disabled: false });
    expect(m.name).toBe('Try again, currently off: Permission was not given.');
  });

  it('disables the button without an engine, except after a refused permission', () => {
    expect(buttonModel({ ...base, engineDown: true })).toMatchObject({ look: 'off', disabled: true, sub: 'The engine is not running.' });
    const denied = buttonModel({ ...base, engineDown: true, startError: { code: 'permission_denied' } });
    expect(denied).toMatchObject({ look: 'idle', label: 'Try again', disabled: false, sub: 'Permission was not given.' });
  });
});

describe('subAction', () => {
  it('offers exactly one action per error', () => {
    const act = (code: string, extra: Partial<HomeInput> = {}) => {
      const input = { ...base, error: { code, error: 'x' }, ...extra };
      return subAction(buttonModel(input), input.engineDown, input.startError);
    };
    expect(act('needs_setup')).toBe('setup');
    expect(act('crashed')).toBe('log');
    expect(act('connect_failed')).toBe('log');
    expect(act('tunnel_missing')).toBe('detour');
    expect(act('permission_denied')).toBeNull();
    expect(act('port_busy')).toBe('restart-engine');
  });

  it('offers Restart engine when the engine is down', () => {
    const m = buttonModel({ ...base, engineDown: true });
    expect(subAction(m, true, null)).toBe('restart-engine');
    expect(subAction(m, true, { code: 'engine_unreachable' })).toBe('restart-engine');
    const denied = buttonModel({ ...base, engineDown: true, startError: { code: 'permission_denied' } });
    expect(subAction(denied, true, { code: 'permission_denied' })).toBeNull();
  });
});

describe('setupCard (first run)', () => {
  const o = { engineDown: false, serversLoaded: true, servers: [] as Server[], skipped: false };
  it('asks to add a server, then to pick one, then goes away', () => {
    expect(setupCard(o)).toBe('add');
    expect(setupCard({ ...o, skipped: true })).toBe('skipped');
    expect(setupCard({ ...o, servers: [srv()] })).toBe('pick');
    expect(setupCard({ ...o, servers: [srv({ active: true })] })).toBe('none');
  });
  it('shows nothing while loading or without an engine', () => {
    expect(setupCard({ ...o, serversLoaded: false })).toBe('none');
    expect(setupCard({ ...o, engineDown: true })).toBe('none');
  });
});

describe('startDetails', () => {
  it('shows the attached text only while the engine is down', () => {
    expect(startDetails(true, { details: 'stage: x\nexit code: 1\n' })).toBe('stage: x\nexit code: 1');
    expect(startDetails(false, { details: 'stage: x' })).toBeNull();
  });
  it('has nothing to show without details (a cancelled prompt) or without an error', () => {
    expect(startDetails(true, {})).toBeNull();
    expect(startDetails(true, { details: '  \n' })).toBeNull();
    expect(startDetails(true, null)).toBeNull();
  });
  it('does not change the sentence or the action of the failure', () => {
    const startError = { code: 'engine_unreachable', message: 'The engine stopped before it was ready.', details: 'stage: x' };
    const m = buttonModel({ ...base, engineDown: true, startError });
    expect(m.sub).toBe('The engine is not running.');
    expect(subAction(m, true, startError)).toBe('restart-engine');
  });
});
