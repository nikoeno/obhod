// Pure view models for Home: the big button (docs/UI.md section 4) and the first-run card
// (section 6). No Vue here, so they are unit tested directly.

import { bigButton, firstRun, pages } from '../../design/copy';
import type { ConnState, EngineErrorBody, Server, Snapshot } from '../backend/types';
import { errorInfo, type ErrorInfo } from './errors';
import { fill, pct } from './format';
import { cleanDetails } from './problems';

export type Look = 'idle' | 'on' | 'busy' | 'busy-dim' | 'off';

export interface ButtonModel {
  look: Look;
  label: string;
  /** Accessible name; always includes the state. */
  name: string;
  disabled: boolean;
  /** Subtitle under the button. */
  sub: string;
  /** The error shown, if any (it decides the one suggested action). */
  err: ErrorInfo | null;
}

export interface HomeInput {
  engineDown: boolean;
  /** Why starting the engine failed (`code`, `message`). */
  startError: { code: string; message?: string } | null;
  state: ConnState;
  error: EngineErrorBody | null;
  snapshot: (Pick<Snapshot, 'Savings'> & Partial<Pick<Snapshot, 'Tunnel'>>) | null;
}

export function buttonModel(s: HomeInput): ButtonModel {
  if (s.engineDown) {
    // A refused permission is retried from the big button; any other reason uses "Restart engine".
    if (s.startError?.code === 'permission_denied') {
      const err = errorInfo(s.startError, 'disconnected');
      const sentence = err?.sentence ?? '';
      return { look: 'idle', label: bigButton.error.label, name: fill(bigButton.error.name, { sentence }), disabled: false, sub: sentence, err };
    }
    const b = bigButton.noEngine;
    return { look: 'off', label: b.label, name: b.name, disabled: true, sub: b.sub, err: errorInfo({ code: 'engine_unreachable' }, 'disconnected') };
  }
  switch (s.state) {
    case 'connecting':
      return { look: 'busy', label: bigButton.connecting.label, name: bigButton.connecting.name, disabled: true, sub: bigButton.connecting.sub, err: null };
    case 'connected':
      return {
        look: 'on',
        label: bigButton.connected.label,
        name: bigButton.connected.name,
        disabled: false,
        sub: fill(bigButton.connected.sub, { savings: pct(s.snapshot?.Savings) }),
        err: null,
      };
    case 'disconnecting':
      return { look: 'busy-dim', label: bigButton.disconnecting.label, name: bigButton.disconnecting.name, disabled: true, sub: bigButton.disconnecting.sub, err: null };
    default: {
      const err = errorInfo(s.error, s.state);
      if (err) {
        return { look: 'idle', label: bigButton.error.label, name: fill(bigButton.error.name, { sentence: err.sentence }), disabled: false, sub: err.sentence, err };
      }
      return { look: 'idle', label: bigButton.disconnected.label, name: bigButton.disconnected.name, disabled: false, sub: bigButton.disconnected.sub, err: null };
    }
  }
}

/**
 * The "Details" text under a failed engine start: what the Rust side attached to the error
 * (sanitized there). Null when the engine is up or the failure had nothing to explain
 * (a cancelled permission prompt).
 */
export function startDetails(engineDown: boolean, startError: { details?: string } | null): string | null {
  if (!engineDown) return null;
  const d = cleanDetails(startError?.details);
  return d ? d : null;
}

export type SubAction = 'restart-engine' | 'setup' | 'log' | 'detour' | null;

/** The one suggested action under the subtitle. */
export function subAction(m: ButtonModel, engineDown: boolean, startError: { code: string } | null): SubAction {
  if (engineDown) return startError?.code === 'permission_denied' ? null : 'restart-engine';
  switch (m.err?.action) {
    case 'setup':
      return 'setup';
    case 'log':
      return 'log';
    case 'detour':
      return 'detour';
    case 'restart-engine':
      return 'restart-engine';
    default:
      return null;
  }
}

export type DetourNotice = 'none' | 'pick' | 'add';

/**
 * The detour-server notice on Home (docs/UI.md section 6). It never blocks Start: the engine
 * connects without a server (`Tunnel: "none"`). `add`: no server at all; `pick`: servers are saved
 * but none is in use (a new server is never activated by itself). Nothing while a server is in use,
 * also while it is being switched to (connected, `Tunnel` still `none` until the reload is done).
 */
export function detourNotice(opts: { engineDown: boolean; serversLoaded: boolean; servers: Server[] }): DetourNotice {
  if (opts.engineDown || !opts.serversLoaded) return 'none';
  if (opts.servers.some((s) => s.active)) return 'none';
  return opts.servers.length ? 'pick' : 'add';
}

export interface DetourNoticeText { title: string; text: string }

/** Words of the notice: "Running without ..." while the app is on without a detour, a calm invitation while it is off. */
export function detourNoticeText(kind: DetourNotice, running: boolean): DetourNoticeText | null {
  if (kind === 'none') return null;
  if (running) return { title: firstRun.runningTitle, text: kind === 'pick' ? firstRun.pickText : firstRun.running };
  return kind === 'pick' ? { title: firstRun.pickTitle, text: firstRun.pickText } : { title: firstRun.detourTitle, text: firstRun.detour };
}

/** True when the engine says it runs, or would run, without a detour server. An older engine (no field) is `ready`. */
export const noDetour = (s: Pick<Snapshot, 'Tunnel'> | null | undefined): boolean => s?.Tunnel === 'none';

/** The muted line under the Blocked card while there is no detour server. */
export function blockedCaveat(engineDown: boolean, s: Pick<Snapshot, 'Tunnel'> | null | undefined): string {
  return !engineDown && noDetour(s) ? pages.home.cardNoDetour : '';
}
