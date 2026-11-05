// Detour-server wording and rules (docs/UI.md 5.4, 6, 10). All sentences come from design/copy.ts.

import { pages, serverErrors } from '../../design/copy';
import type { ConnState, Server, ServerTest } from '../backend/types';
import { fill } from './format';

const d = pages.settings.detour;

export const MAX_SERVERS = 20;

export const PROTOCOL_NAMES: Record<string, string> = {
  vless: 'VLESS',
  vmess: 'VMess',
  trojan: 'Trojan',
  shadowsocks: 'Shadowsocks',
  hysteria2: 'Hysteria2',
  tuic: 'TUIC',
};

export const protocolName = (p: string | undefined): string => (p && PROTOCOL_NAMES[p]) || p || 'Unknown';

export type ServerCtx = 'add' | 'remove' | 'activate';

/**
 * Plain sentence for a failed server call. `invalid_input` and `unsupported_protocol` carry the
 * engine's own fixed sentence (never a value from the input), so those are shown as they come.
 */
export function serverErrorText(e: { code?: string; message?: string } | null | undefined, ctx: ServerCtx): string {
  const code = e?.code;
  switch (code) {
    case 'invalid_input':
      return e?.message || serverErrors.invalidFallback;
    case 'unsupported_protocol':
      return e?.message || serverErrors.unsupportedFallback;
    case 'busy':
      return ctx === 'remove' ? serverErrors.busyRemove : serverErrors.busy;
    case 'bad_request':
      return ctx === 'add' ? serverErrors.badName : 'The engine did not accept this request.';
    case 'duplicate':
      return serverErrors.duplicate;
    case 'limit_reached':
      return serverErrors.limit_reached;
    case 'too_large':
      return serverErrors.too_large;
    case 'not_found':
      return serverErrors.not_found;
    case 'engine_unreachable':
      return 'The engine is not running.';
    default:
      return 'Something went wrong.';
  }
}

/** `POST servers/test` answers only a code, so the sentence is the UI's. */
export function testResultText(r: ServerTest): string {
  if (r.ok) return fill(d.add.testOk, { protocol: protocolName(r.protocol) });
  if (r.code === 'unsupported_protocol') return d.add.testUnsupported;
  return d.add.testInvalid;
}

export const serverText = {
  added: (name: string) => fill(d.added, { name }),
  nowActive: (name: string) => fill(d.nowActive, { name }),
  switching: (name: string) => fill(d.switching, { name }),
  removed: (name: string) => fill(d.removed, { name }),
  removeTitle: (name: string) => fill(d.remove.title, { name }),
  count: (n: number) => fill(d.count, { n }),
};

/** Server buttons are disabled while the engine is down, starting or stopping (the engine answers `busy`). */
export function serversLocked(engineDown: boolean, state: ConnState): boolean {
  return engineDown || state === 'connecting' || state === 'disconnecting';
}

/** The active server cannot be removed while connected; the confirmation then says so. */
export function removeNeedsStop(server: Server, state: ConnState): boolean {
  return server.active && state === 'connected';
}

/** The muted line under the Add button. */
export function serverNote(opts: { engineDown: boolean; state: ConnState; count: number }): string {
  if (opts.count >= MAX_SERVERS) return serverErrors.limit_reached;
  if (serversLocked(opts.engineDown, opts.state) && !opts.engineDown) return d.busyHint;
  if (opts.state === 'connected' && opts.count > 1) return d.switchHint;
  return '';
}

/** IPv6 literals are shown in brackets. */
export function hostPort(s: Pick<Server, 'host' | 'port'>): string {
  const h = s.host.includes(':') && !s.host.startsWith('[') ? `[${s.host}]` : s.host;
  return `${h}:${s.port}`;
}
