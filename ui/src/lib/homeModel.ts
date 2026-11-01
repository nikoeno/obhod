// Pure view models for Home: the big button (docs/UI.md section 4) and the first-run card
// (section 6). No Vue here, so they are unit tested directly.

import { bigButton } from '../../design/copy';
import type { ConnState, EngineErrorBody, Server, Snapshot } from '../backend/types';
import { errorInfo, type ErrorInfo } from './errors';
import { fill, pct } from './format';

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
  snapshot: Pick<Snapshot, 'Savings'> | null;
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
  const d = startError?.details?.trim();
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

export type SetupCard = 'none' | 'pick' | 'add' | 'skipped';

/**
 * First run (docs/UI.md section 6): no active server. With servers saved but none active the card
 * asks to pick one; with none, to add one, or (after "Skip for now") a reminder banner.
 */
export function setupCard(opts: { engineDown: boolean; serversLoaded: boolean; servers: Server[]; skipped: boolean }): SetupCard {
  if (opts.engineDown || !opts.serversLoaded) return 'none';
  if (opts.servers.some((s) => s.active)) return 'none';
  if (opts.servers.length) return 'pick';
  return opts.skipped ? 'skipped' : 'add';
}
