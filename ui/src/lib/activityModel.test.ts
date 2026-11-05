import { describe, expect, it } from 'vitest';
import type { Change } from '../backend/types';
import { CHART_H, RECENT_CAP, bars, changeRows, charts, mergeChanges, networkFacts, networkRows, parseTab, trafficStats, warningRows } from './activityModel';

const ch = (d: string, from: string, to: string, at: string): Change => ({ Domain: d, From: from, To: to, At: at });

describe('parseTab', () => {
  it('knows the four tabs', () => {
    expect(parseTab('traffic')).toBe('traffic');
    expect(parseTab('warnings')).toBe('warnings');
    expect(parseTab('nope')).toBe('changes');
    expect(parseTab(undefined)).toBe('changes');
  });
});

describe('mergeChanges', () => {
  const a = ch('a.example', 'direct', 'desync', '2026-11-01T10:00:00Z');
  const b = ch('b.example', 'desync', 'tunnel', '2026-11-01T10:05:00Z');
  it('appends new changes and skips duplicates', () => {
    expect(mergeChanges([a], [b, a])).toEqual([a, b]);
  });
  it('caps the list, keeping the newest', () => {
    const many = Array.from({ length: RECENT_CAP + 5 }, (_, i) => ch(`d${i}.example`, 'direct', 'desync', `2026-11-01T10:${String(i % 60).padStart(2, '0')}:00Z`));
    const out = mergeChanges([], many);
    expect(out).toHaveLength(RECENT_CAP);
    expect(out.at(-1)).toEqual(many.at(-1));
  });
  it('does not change the input', () => {
    const base = [a];
    mergeChanges(base, [b]);
    expect(base).toEqual([a]);
  });
});

describe('changeRows', () => {
  const list = [ch('a.example', 'direct', 'desync', '2026-11-01T10:00:00Z'), ch('b.example', 'desync', 'tunnel', '2026-11-01T11:00:00Z')];
  it('shows newest first with user words', () => {
    const rows = changeRows(list, 0);
    expect(rows.map((x) => x.site)).toEqual(['b.example', 'a.example']);
    expect(rows[0]).toMatchObject({ from: 'Picky', to: 'Blocked' });
    expect(rows[0]!.text).toMatch(/^b\.example moved from Picky to Blocked · /);
  });
  it('Clear view hides older changes only', () => {
    expect(changeRows(list, Date.parse('2026-11-01T10:30:00Z')).map((x) => x.site)).toEqual(['b.example']);
    expect(changeRows(list, Date.parse('2026-11-01T12:00:00Z'))).toEqual([]);
    expect(changeRows(undefined, 0)).toEqual([]);
  });
});

describe('charts and bars', () => {
  const series = { direct: [0, 100, 50], tunnel: [10], desync: [], proxy: [999] };
  it('hides Via proxy without traffic and shares one scale', () => {
    expect(charts({ direct: 10, tunnel: 20 }, series).map((c) => c.route)).toEqual(['direct', 'tunnel', 'desync']);
    const withProxy = charts({ proxy: 5 }, series);
    expect(withProxy.map((c) => c.route)).toContain('proxy');
    expect(withProxy[0]!.max).toBe(999);
    expect(charts({}, { direct: [], tunnel: [], desync: [], proxy: [] })[0]!.max).toBe(1);
  });
  it('labels a chart with title, today and the latest sample', () => {
    const c = charts({ direct: 2048 }, series)[0]!;
    expect(c.title).toBe('Everyday sites');
    expect(c.today).toBe('2.0 KB today');
    expect(c.label).toContain('latest 50 B');
  });
  it('draws newest samples on the right, zero as nothing, small values at least 2 tall', () => {
    const b = bars([0, 100, 1], 100);
    expect(b).toHaveLength(3);
    expect(b[0]!.height).toBe(0);
    expect(b[1]!.height).toBe(CHART_H - 4);
    expect(b[2]!.height).toBe(2);
    expect(b[2]!.x).toBeGreaterThan(b[1]!.x);
    expect(b[2]!.x + b[2]!.width).toBeLessThanOrEqual(300);
    expect(bars(Array(40).fill(5), 5)).toHaveLength(30);
  });
});

describe('network, stats, warnings', () => {
  const now = Date.parse('2026-11-01T12:00:00Z');
  const snap = { Network: 'Home Wi-Fi', LastCheck: '2026-11-01T11:50:00Z', NextCheck: '2026-11-01T12:12:00Z' };
  it('shows the label, last and next check', () => {
    const f = networkFacts(snap, false, now);
    expect(f[0]).toEqual({ term: 'Network', value: 'Home Wi-Fi' });
    expect(f[1]!.value).toMatch(/^10 min ago \(/);
    expect(f[2]!.value).toMatch(/^in 12 min \(/);
    expect(networkFacts(snap, true, now)[1]!.value).toBe('Checking…');
  });
  it('says learning for an unknown network and tolerates no snapshot', () => {
    expect(networkFacts({ ...snap, Network: 'unknown network' }, false, now)[0]!.value).toBe('Still learning this network');
    expect(networkFacts(null, false, now).map((x) => x.value)).toEqual(['Still learning this network', 'never', '–']);
  });
  it('lists networks newest first by label only', () => {
    const rows = networkRows([{ label: 'Office', since: '2026-11-01T08:00:00Z' }, { label: 'Home', since: '2026-11-01T11:00:00Z' }]);
    expect(rows.map((x) => x.label)).toEqual(['Home', 'Office']);
    expect(rows[0]!.since).toMatch(/^since /);
  });
  it('formats traffic stats', () => {
    expect(trafficStats({ uploadTotal: 1024, downloadTotal: 5 * 1024 * 1024, connections: 14 })).toEqual([
      { label: 'Downloaded', value: '5.0 MB' },
      { label: 'Uploaded', value: '1.0 KB' },
      { label: 'Connections', value: '14' },
    ]);
  });
  it('explains each warning with what to do', () => {
    const rows = warningRows(['IPv6 is not available on this network', 'Something odd']);
    expect(rows[0]).toMatchObject({ text: 'IPv6 is not available on this network' });
    expect(rows[0]!.todo).toContain('No action needed');
    expect(rows[1]!.todo).toContain('Show engine log');
    expect(warningRows(undefined)).toEqual([]);
  });
});
