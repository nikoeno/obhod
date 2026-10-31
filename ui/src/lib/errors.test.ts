import { describe, expect, it } from 'vitest';
import { errors, switchFailed, unknownError } from '../../design/copy';
import { errorInfo, explainWarning, isLearning } from './errors';

describe('errorInfo', () => {
  it('maps every contract code to its sentence and one action', () => {
    const expected: Record<string, string> = {
      permission_denied: 'retry',
      needs_setup: 'setup',
      tunnel_missing: 'detour',
      crashed: 'log',
      connect_failed: 'log',
      engine_unreachable: 'restart-engine',
    };
    for (const [code, action] of Object.entries(expected)) {
      const i = errorInfo({ code }, 'disconnected')!;
      expect(i.sentence).toBe(errors[code]!.sentence);
      expect(i.action).toBe(action);
    }
  });

  it('keeps the commands of needs_setup', () => {
    const i = errorInfo({ code: 'needs_setup', error: 'x', commands: ['a', 'b'] }, 'disconnected')!;
    expect(i.commands).toEqual(['a', 'b']);
    expect(i.action).toBe('setup');
  });

  it('treats connect_failed while connected as a failed server switch', () => {
    const i = errorInfo({ code: 'connect_failed' }, 'connected')!;
    expect(i.sentence).toBe(switchFailed);
    expect(i.action).toBe('detour');
    expect(errorInfo({ code: 'connect_failed' }, 'disconnected')!.action).toBe('log');
  });

  it('falls back to the engine sentence, then to a generic one', () => {
    expect(errorInfo({ code: 'weird', error: 'Engine words.' }, 'disconnected')!.sentence).toBe('Engine words.');
    expect(errorInfo({ code: 'weird', message: 'Rust words.' }, 'disconnected')!.sentence).toBe('Rust words.');
    expect(errorInfo({ code: 'weird' }, 'disconnected')!.sentence).toBe(unknownError);
    expect(errorInfo({ code: 'weird' }, 'disconnected')!.action).toBe('retry');
  });

  it('is null without an error', () => {
    expect(errorInfo(null, 'disconnected')).toBeNull();
    expect(errorInfo(undefined, 'connected')).toBeNull();
  });
});

describe('helpers', () => {
  it('explains known warnings and has a default', () => {
    expect(explainWarning('IPv6 is not available').why).toContain('IPv6');
    expect(explainWarning("sing-box's Clash API is not answering").todo).toContain('Help');
  });
  it('knows when the network is still unknown', () => {
    expect(isLearning('unknown network')).toBe(true);
    expect(isLearning('')).toBe(true);
    expect(isLearning('Home Wi-Fi')).toBe(false);
  });
});
