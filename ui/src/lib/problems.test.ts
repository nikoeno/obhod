import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { pages } from '../../design/copy';
import type { Problem } from '../backend/types';
import { cleanDetails, cleanProblems, createProblemHold, MAX_DETAIL_CHARS, orderProblems, problemStatus, problemViews } from './problems';

const desync: Problem = { Code: 'desync_failed', Message: 'The desync program could not start. Picky sites may not open.', Detail: 'Exit: gave up after 5 restarts: exit status 87\nOutput:\nwinws2.exe: the filter is not valid' };
const tunnel: Problem = { Code: 'tunnel_engine_failed', Message: 'The tunnel program stopped working. Blocked sites may not open.', Detail: '' };
const other: Problem = { Code: 'future_thing', Message: 'Something new broke.', Detail: 'some detail' };

describe('cleanDetails', () => {
  it('keeps normal multi-line text and trims', () => {
    expect(cleanDetails('  Exit: x\nOutput:\n\ty\n ')).toBe('Exit: x\nOutput:\n\ty');
  });
  it('normalises line breaks and strips control and direction characters', () => {
    expect(cleanDetails('a\r\nb\rc')).toBe('a\nb\nc');
    const dirty = ['a', 'b', 'c', 'd', 'e', 'f', 'g'].map((x, i) => x + String.fromCharCode([0, 0x1b, 0x7f, 0x85, 0x202e, 0x200b, 0][i]!)).join('');
    expect(cleanDetails(dirty)).toBe('abcdefg');
  });
  it('caps at 4000 characters', () => {
    const out = cleanDetails('x'.repeat(10000));
    expect(out.length).toBe(MAX_DETAIL_CHARS + 1);
    expect(out.endsWith('…')).toBe(true);
    expect(cleanDetails('y'.repeat(MAX_DETAIL_CHARS))).toHaveLength(MAX_DETAIL_CHARS);
  });
  it('is empty for blank or non-text input', () => {
    expect(cleanDetails('  \n ')).toBe('');
    expect(cleanDetails(undefined)).toBe('');
    expect(cleanDetails(42)).toBe('');
  });
});

describe('cleanProblems', () => {
  it('drops malformed entries and merges duplicate codes', () => {
    expect(cleanProblems(null)).toEqual([]);
    expect(cleanProblems('x')).toEqual([]);
    const got = cleanProblems([null, 1, { Message: 'no code' }, desync, { ...desync, Detail: 'again' }]);
    expect(got).toEqual([desync]);
  });
  it('cleans every field and tolerates missing ones', () => {
    const [p] = cleanProblems([{ Code: 'x\u0000y', Message: 'a\nb\u0007', Detail: 'q'.repeat(9000) }]);
    expect(p!.Code).toBe('xy');
    expect(p!.Message).toBe('a b');
    expect(p!.Detail.length).toBe(MAX_DETAIL_CHARS + 1);
    expect(cleanProblems([{ Code: 'only' }])).toEqual([{ Code: 'only', Message: '', Detail: '' }]);
  });
});

describe('problemViews', () => {
  it('uses our own words for known codes, with details', () => {
    const v = problemViews([desync]);
    expect(v).toHaveLength(1);
    expect(v[0]!.text).toBe('The quiet fix could not start. Picky sites may not open.');
    expect(v[0]!.detail).toContain('exit status 87');
  });
  it('orders known codes first, in a fixed order; unknown ones after, in engine order', () => {
    const list = [other, { ...other, Code: 'another' }, tunnel, desync];
    expect(problemViews(list).map((p) => p.code)).toEqual(['desync_failed', 'tunnel_engine_failed', 'future_thing', 'another']);
    expect(orderProblems([tunnel, desync]).map((p) => p.Code)).toEqual(['desync_failed', 'tunnel_engine_failed']);
  });
  it('shows an unknown code with its Message only, no details', () => {
    const v = problemViews([other]);
    expect(v).toEqual([{ key: 'future_thing', code: 'future_thing', text: 'Something new broke.', detail: null }]);
    expect(problemViews([{ Code: 'zzz', Message: '', Detail: 'x' }])[0]!.text).toBe(pages.home.problems.unknown);
  });
  it('has no details box for an empty detail, and nothing for no problems', () => {
    expect(problemViews([tunnel])[0]!.detail).toBeNull();
    expect(problemViews([])).toEqual([]);
    expect(problemViews(undefined)).toEqual([]);
  });
  it('caps a huge detail from a misbehaving engine', () => {
    expect(problemViews([{ ...desync, Detail: 'z'.repeat(50000) }])[0]!.detail!.length).toBe(MAX_DETAIL_CHARS + 1);
  });
});

describe('copy rules', () => {
  const t = pages.home.problems;
  it('user text has no network words', () => {
    const texts = [t.desync_failed, t.tunnel_engine_failed, t.unknown, t.label, t.summary, t.hint, t.detailsLabel, t.copied, t.copyFailed, problemStatus(1), problemStatus(3)];
    for (const x of texts) expect(x).not.toMatch(/tunnel|proxy|sing-?box|vpn|dns|desync|winws|nfqws|zapret/i);
  });
  it('says "quiet fix" and Picky for the desync problem', () => {
    expect(t.desync_failed).toBe('The quiet fix could not start. Picky sites may not open.');
  });
  it('status line', () => {
    expect(problemStatus(0)).toBe('');
    expect(problemStatus(1)).toBe('On, with a problem');
    expect(problemStatus(2)).toBe('On, with 2 problems');
  });
});

describe('createProblemHold', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  const make = (delay = 1500) => {
    const seen: string[][] = [];
    const hold = createProblemHold((l) => seen.push(l.map((p) => p.Code)), delay);
    return { hold, seen };
  };

  it('shows at once', () => {
    const { hold, seen } = make();
    hold.update([desync]);
    expect(seen).toEqual([['desync_failed']]);
  });

  it('hides only after the delay', () => {
    const { hold, seen } = make();
    hold.update([desync]);
    hold.update([]);
    vi.advanceTimersByTime(1499);
    expect(seen).toEqual([['desync_failed']]);
    vi.advanceTimersByTime(1);
    expect(seen).toEqual([['desync_failed'], []]);
  });

  it('does not flicker when a restart clears and re-raises the problem', () => {
    const { hold, seen } = make();
    hold.update([desync]);
    hold.update([]);
    vi.advanceTimersByTime(800);
    hold.update([desync]);
    vi.advanceTimersByTime(10000);
    expect(seen).toEqual([['desync_failed']]);
  });

  it('hides each problem on its own clock and keeps the order', () => {
    const { hold, seen } = make();
    hold.update([tunnel, desync]);
    expect(seen[0]).toEqual(['desync_failed', 'tunnel_engine_failed']);
    hold.update([tunnel]);
    vi.advanceTimersByTime(1500);
    expect(seen.at(-1)).toEqual(['tunnel_engine_failed']);
  });

  it('a delay of 0 hides at once; stop cancels pending hides', () => {
    const a = make(0);
    a.hold.update([desync]);
    a.hold.update([]);
    expect(a.seen).toEqual([['desync_failed'], []]);
    const b = make();
    b.hold.update([desync]);
    b.hold.update([]);
    b.hold.stop();
    vi.advanceTimersByTime(5000);
    expect(b.seen).toEqual([['desync_failed']]);
  });
});
