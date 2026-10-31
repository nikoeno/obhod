// Error mapping (docs/UI.md section 10): engine / Rust error codes to the one sentence and the one
// suggested action the user sees. The sentences live in design/copy.ts.

import { errors as errorCopy, switchFailed, unknownError, type ErrorAction } from '../../design/copy';
import type { ConnState } from '../backend/types';

/** Anything with a code: the engine's `state.error` body or a `BackendError`. */
export interface CodedError {
  code: string;
  /** Engine body sentence. */
  error?: string;
  /** BackendError sentence. */
  message?: string;
  commands?: string[];
}

export interface ErrorInfo {
  code: string;
  sentence: string;
  action: ErrorAction;
  commands?: string[];
}

/**
 * `state` is the connection state the error arrived with. A `connect_failed` that arrives while the
 * state is `connected` is a server switch that could not be applied (API.md, activate): the old
 * server is still in use and the one action is to pick another server.
 */
export function errorInfo(err: CodedError | null | undefined, state: ConnState): ErrorInfo | null {
  if (!err) return null;
  if (err.code === 'connect_failed' && state === 'connected') {
    return { code: err.code, sentence: switchFailed, action: 'detour' };
  }
  const known = errorCopy[err.code];
  return {
    code: err.code,
    sentence: known ? known.sentence : err.error || err.message || unknownError,
    action: known ? known.action : 'retry',
    commands: err.commands,
  };
}

/** Plain explanation and suggestion for the engine's warning texts (Activity, Warnings). */
export function explainWarning(text: string): { why: string; todo: string } {
  const t = text.toLowerCase();
  if (t.includes('ipv6')) {
    return { why: 'Your network does not offer IPv6, so the app uses IPv4 only. Nothing is broken.', todo: 'No action needed. If sites feel slow, use Check sites again.' };
  }
  if (t.includes('detour server') || t.includes('slower')) {
    return { why: 'The detour server takes longer than usual to answer. Only Blocked sites are affected.', todo: 'Wait a few minutes, or check Settings → Detour server.' };
  }
  return { why: 'The engine reported this while running.', todo: 'If it keeps showing, open Help → Show engine log.' };
}

/** The engine says "unknown network" until it has identified the network. */
export const isLearning = (label: string | undefined): boolean => !label || label === 'unknown network';
