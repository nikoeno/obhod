import { describe, expect, it } from 'vitest';
import { fill, fmtAgo, fmtBytes, fmtIn, pct, plural } from './format';

describe('format (design README section 9)', () => {
  it('bytes', () => {
    expect(fmtBytes(0)).toBe('0 B');
    expect(fmtBytes(-5)).toBe('0 B');
    expect(fmtBytes(Number.NaN)).toBe('0 B');
    expect(fmtBytes(512)).toBe('512 B');
    expect(fmtBytes(1.5 * 1024 * 1024)).toBe('1.5 MB');
    expect(fmtBytes(120 * 1024 ** 3)).toBe('120 GB');
  });
  const now = Date.UTC(2026, 10, 1, 12, 0, 0);
  it('past times', () => {
    expect(fmtAgo(new Date(now - 10_000).toISOString(), now)).toBe('just now');
    expect(fmtAgo(new Date(now - 5 * 60_000).toISOString(), now)).toBe('5 min ago');
    expect(fmtAgo(new Date(now - 3 * 3600_000).toISOString(), now)).toBe('3 h ago');
    expect(fmtAgo(new Date(now - 2 * 86400_000).toISOString(), now)).toBe('2 d ago');
    expect(fmtAgo('0001-01-01T00:00:00Z', now)).toBe('never');
    expect(fmtAgo('garbage', now)).toBe('never');
  });
  it('future times', () => {
    expect(fmtIn(new Date(now + 12 * 60_000).toISOString(), now)).toBe('in 12 min');
    expect(fmtIn(new Date(now + 2 * 3600_000).toISOString(), now)).toBe('in 2 h');
    expect(fmtIn(new Date(now - 1000).toISOString(), now)).toBe('now');
    expect(fmtIn('0001-01-01T00:00:00Z', now)).toBe('–');
  });
  it('percent, templates and plurals', () => {
    expect(pct(0.78)).toBe('78%');
    expect(pct(undefined)).toBe('0%');
    expect(fill('{a} and {b} {c}', { a: 1, b: 'two' })).toBe('1 and two {c}');
    expect(plural({ one: '{n} site', other: '{n} sites' }, 1)).toBe('1 site');
    expect(plural({ one: '{n} site', other: '{n} sites' }, 0)).toBe('0 sites');
  });
});
