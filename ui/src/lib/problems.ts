// Problems the engine reports in `snapshot.Problems` (api/desktop/API.md, decision 44): something does
// not work although the connection stays up. The engine already sanitizes `Detail`; the UI does not
// trust that blindly (defence in depth): it caps and cleans every text again before showing it.
// Pure functions and one small timer helper; no Vue, so they are unit tested directly.

import { pages } from '../../design/copy';
import type { Problem } from '../backend/types';
import { plural } from './format';

/** Most characters of a details text shown or copied (the Rust side uses the same limit for start failures). */
export const MAX_DETAIL_CHARS = 4000;
/** Most characters of a problem's own sentence. */
export const MAX_MESSAGE_CHARS = 300;
/** A banner stays this long (ms) after the engine stops reporting its problem, so a restart that clears and re-raises it does not flicker. */
export const HIDE_DELAY_MS = 1500;
/** More problems than this are ignored (the engine has two codes today). */
const MAX_PROBLEMS = 10;

// C0 (except tab and newline), DEL, C1, zero-width and direction-control characters, line/paragraph separators.
const UNSAFE_RANGES: Array<[number, number]> = [[0, 8], [0x0b, 0x1f], [0x7f, 0x9f], [0x200b, 0x200f], [0x2028, 0x202e], [0x2060, 0x2069], [0xfeff, 0xfeff]];
function stripUnsafe(text: string): string {
  let out = '';
  for (const ch of text) {
    const c = ch.codePointAt(0)!;
    if (!UNSAFE_RANGES.some(([a, b]) => c >= a && c <= b)) out += ch;
  }
  return out;
}

/**
 * Cleans a details text for display: line breaks normalised, control characters removed, trimmed, capped.
 * Returns '' for blank input.
 */
export function cleanDetails(text: unknown, max = MAX_DETAIL_CHARS): string {
  if (typeof text !== 'string') return '';
  const t = stripUnsafe(text.replace(/\r\n?/g, '\n')).trim();
  return t.length > max ? `${t.slice(0, max).trimEnd()}…` : t;
}

/** A one-line sentence: like `cleanDetails`, but line breaks become spaces. */
export function cleanLine(text: unknown, max = MAX_MESSAGE_CHARS): string {
  return cleanDetails(typeof text === 'string' ? text.replace(/\s+/g, ' ') : '', max);
}

/** The snapshot's problems made safe: unknown shapes dropped, texts cleaned and capped, duplicate codes merged. */
export function cleanProblems(raw: unknown): Problem[] {
  if (!Array.isArray(raw)) return [];
  const out: Problem[] = [];
  for (const p of raw.slice(0, MAX_PROBLEMS * 2)) {
    if (!p || typeof p !== 'object') continue;
    const o = p as Record<string, unknown>;
    const code = cleanLine(o.Code, 64);
    if (!code || out.some((x) => x.Code === code)) continue;
    out.push({ Code: code, Message: cleanLine(o.Message), Detail: cleanDetails(o.Detail) });
    if (out.length >= MAX_PROBLEMS) break;
  }
  return out;
}

const KNOWN = ['desync_failed', 'tunnel_engine_failed'] as const;

/** Known codes first, in a fixed order; unknown ones after them in the order the engine sent. */
export function orderProblems(list: Problem[]): Problem[] {
  const rank = (p: Problem): number => {
    const i = (KNOWN as readonly string[]).indexOf(p.Code);
    return i < 0 ? KNOWN.length : i;
  };
  return list.map((p, i) => ({ p, i })).sort((a, b) => rank(a.p) - rank(b.p) || a.i - b.i).map((x) => x.p);
}

export interface ProblemView {
  /** Stable key (the code). */
  key: string;
  code: string;
  /** The user's sentence: our own words for a known code, the engine's `Message` for an unknown one. */
  text: string;
  /** The details box text; null for an unknown code (Message only) or when there is nothing to explain. */
  detail: string | null;
}

/** What Home and Activity show, one entry per problem, in display order. */
export function problemViews(list: Problem[] | undefined): ProblemView[] {
  const t = pages.home.problems;
  return orderProblems(cleanProblems(list)).map((p) => {
    const known = (KNOWN as readonly string[]).includes(p.Code);
    const text = known ? t[p.Code as (typeof KNOWN)[number]] : p.Message || t.unknown;
    return { key: p.Code, code: p.Code, text, detail: known && p.Detail ? p.Detail : null };
  });
}

/** The status line in the app shell: "On, with a problem". Empty when there is none. */
export function problemStatus(count: number): string {
  return count > 0 ? plural(pages.home.problems.status, count) : '';
}

export interface ProblemHold {
  /** Feed the engine's current problems. A new or still-present problem shows at once; a gone one is hidden after the delay. */
  update(next: Problem[]): void;
  /** Cancels pending hides. */
  stop(): void;
}

/**
 * Debounces the hiding of problems. `onChange` gets the list to show whenever it changes. A problem that
 * comes back before its delay has run out never disappears. `delay` 0 hides at once.
 */
export function createProblemHold(onChange: (shown: Problem[]) => void, delay = HIDE_DELAY_MS): ProblemHold {
  let shown: Problem[] = [];
  const timers = new Map<string, ReturnType<typeof setTimeout>>();

  const same = (a: Problem[], b: Problem[]): boolean => a.length === b.length && a.every((p, i) => p.Code === b[i]!.Code && p.Message === b[i]!.Message && p.Detail === b[i]!.Detail);
  const publish = (list: Problem[]): void => {
    const ordered = orderProblems(list);
    if (same(ordered, shown)) return;
    shown = ordered;
    onChange(shown);
  };

  return {
    update(next) {
      const incoming = cleanProblems(next);
      const codes = new Set(incoming.map((p) => p.Code));
      for (const c of codes) {
        const t = timers.get(c);
        if (t !== undefined) clearTimeout(t);
        timers.delete(c);
      }
      const kept = shown.filter((p) => !codes.has(p.Code));
      for (const p of kept) {
        if (timers.has(p.Code)) continue;
        if (delay <= 0) continue;
        const code = p.Code;
        timers.set(
          code,
          setTimeout(() => {
            timers.delete(code);
            publish(shown.filter((x) => x.Code !== code));
          }, delay),
        );
      }
      // Gone problems stay (with their last text) until their timer runs out.
      publish([...incoming, ...(delay > 0 ? kept : [])]);
    },
    stop() {
      for (const t of timers.values()) clearTimeout(t);
      timers.clear();
    },
  };
}
