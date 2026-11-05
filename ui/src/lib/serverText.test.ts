import { describe, expect, it } from 'vitest';
import type { Server } from '../backend/types';
import { hostPort, protocolName, removeNeedsStop, serverErrorText, serverNote, serversLocked, testResultText } from './serverText';

const srv = (o: Partial<Server> = {}): Server => ({ id: '0000000000000001', name: 'S', protocol: 'vless', engine: 'singbox', host: '192.0.2.1', port: 443, active: false, addedAt: '', ...o });

describe('serverErrorText', () => {
  it('shows the engine sentence for invalid_input and unsupported_protocol', () => {
    expect(serverErrorText({ code: 'invalid_input', message: 'The link has no port.' }, 'add')).toBe('The link has no port.');
    expect(serverErrorText({ code: 'unsupported_protocol', message: 'xhttp is not supported.' }, 'add')).toBe('xhttp is not supported.');
    expect(serverErrorText({ code: 'invalid_input' }, 'add')).toBe('The link or JSON is not valid.');
    expect(serverErrorText({ code: 'unsupported_protocol' }, 'add')).toBe('This kind of server is not supported.');
  });
  it('uses the UI sentences for the rest', () => {
    expect(serverErrorText({ code: 'duplicate' }, 'add')).toBe('This server is already in the list.');
    expect(serverErrorText({ code: 'limit_reached' }, 'add')).toBe('The list is full (20 servers). Remove one first.');
    expect(serverErrorText({ code: 'too_large' }, 'add')).toBe('The link or JSON is too large.');
    expect(serverErrorText({ code: 'not_found' }, 'activate')).toBe('This server is no longer in the list.');
    expect(serverErrorText({ code: 'busy' }, 'add')).toBe('The engine is busy connecting or disconnecting. Try again in a moment.');
    expect(serverErrorText({ code: 'busy' }, 'remove')).toBe('This server cannot be removed now. Stop the app first, or wait until it has finished starting or stopping.');
    expect(serverErrorText({ code: 'bad_request' }, 'add')).toBe('The name must be 1 to 60 characters.');
    expect(serverErrorText({ code: 'bad_request' }, 'remove')).toBe('The engine did not accept this request.');
    expect(serverErrorText({ code: 'engine_unreachable' }, 'add')).toBe('The engine is not running.');
    expect(serverErrorText({ code: 'anything' }, 'add')).toBe('Something went wrong.');
    expect(serverErrorText(null, 'add')).toBe('Something went wrong.');
  });
});

describe('testResultText', () => {
  it('words the three outcomes', () => {
    expect(testResultText({ ok: true, protocol: 'hysteria2', code: '' })).toContain('a Hysteria2 server');
    expect(testResultText({ ok: false, protocol: '', code: 'unsupported_protocol' })).toBe('This kind of server is not supported.');
    expect(testResultText({ ok: false, protocol: '', code: 'invalid_input' })).toContain('does not look like a valid server');
  });
});

describe('server rules', () => {
  it('locks the buttons while the engine is busy or gone', () => {
    expect(serversLocked(false, 'disconnected')).toBe(false);
    expect(serversLocked(false, 'connected')).toBe(false);
    expect(serversLocked(false, 'connecting')).toBe(true);
    expect(serversLocked(false, 'disconnecting')).toBe(true);
    expect(serversLocked(true, 'disconnected')).toBe(true);
  });
  it('needs a stop only for the active server while connected', () => {
    expect(removeNeedsStop(srv({ active: true }), 'connected')).toBe(true);
    expect(removeNeedsStop(srv({ active: true }), 'disconnected')).toBe(false);
    expect(removeNeedsStop(srv(), 'connected')).toBe(false);
  });
  it('chooses the note under the Add button', () => {
    expect(serverNote({ engineDown: false, state: 'connecting', count: 1 })).toContain('starting or stopping');
    expect(serverNote({ engineDown: false, state: 'connected', count: 2 })).toContain('restarts the connection once');
    expect(serverNote({ engineDown: false, state: 'connected', count: 1 })).toBe('');
    expect(serverNote({ engineDown: true, state: 'disconnected', count: 1 })).toBe('');
    expect(serverNote({ engineDown: false, state: 'disconnected', count: 20 })).toContain('full');
  });
  it('formats names and addresses', () => {
    expect(protocolName('vless')).toBe('VLESS');
    expect(protocolName('weird')).toBe('weird');
    expect(protocolName('')).toBe('Unknown');
    expect(hostPort(srv())).toBe('192.0.2.1:443');
    expect(hostPort(srv({ host: '2001:db8::1' }))).toBe('[2001:db8::1]:443');
  });
});
