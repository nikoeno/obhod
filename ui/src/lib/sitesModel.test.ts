import { describe, expect, it } from 'vitest';
import type { Route, RouteInfo } from '../backend/types';
import { pages } from '../../design/copy';
import { countLine, effectiveFilter, emptyText, filterSites, historyRows, listView, isDegraded, panelFacts, parseFilter, siteTabs, sourceWord } from './sitesModel';

const NOW = Date.parse('2026-11-01T12:00:00Z');
const r = (domain: string, route: Route, o: Partial<RouteInfo> = {}): RouteInfo => ({
  domain, route, source: 'checked', since: '2026-11-01T11:55:00Z', lastCheck: '2026-11-01T11:58:00Z', history: [], ...o,
});
const table = [r('Video.example', 'desync'), r('news.example', 'direct'), r('mail.example', 'direct'), r('social.example', 'tunnel')];

describe('parseFilter', () => {
  it('accepts a route and falls back to all', () => {
    expect(parseFilter('tunnel')).toBe('tunnel');
    expect(parseFilter('block')).toBe('block');
    expect(parseFilter('bogus')).toBe('all');
    expect(parseFilter('toString')).toBe('all');
    expect(parseFilter(undefined)).toBe('all');
  });
});

describe('siteTabs', () => {
  it('lists All and the three paths with counts, extras only when non-empty', () => {
    const tabs = siteTabs(table);
    expect(tabs.map((t) => [t.id, t.label, t.count])).toEqual([['all', 'All', 4], ['direct', 'Everyday', 2], ['tunnel', 'Blocked', 1], ['desync', 'Picky', 1]]);
    const withExtras = siteTabs([...table, r('ads.example', 'block'), r('office.example', 'proxy')]);
    expect(withExtras.map((t) => t.id)).toEqual(['all', 'direct', 'tunnel', 'desync', 'proxy', 'block']);
  });

  it('keeps the three main tabs even at zero', () => {
    expect(siteTabs([]).map((t) => t.count)).toEqual([0, 0, 0, 0]);
  });

  it('falls back to all when the filtered tab is gone', () => {
    expect(effectiveFilter('block', siteTabs(table))).toBe('all');
    expect(effectiveFilter('direct', siteTabs(table))).toBe('direct');
  });
});

describe('filterSites', () => {
  it('filters by path and by substring, case-insensitively, sorted by domain', () => {
    expect(filterSites(table, 'all', '').map((x) => x.domain)).toEqual(['Video.example', 'mail.example', 'news.example', 'social.example']);
    expect(filterSites(table, 'direct', '').map((x) => x.domain)).toEqual(['mail.example', 'news.example']);
    expect(filterSites(table, 'all', '  VIDEO ').map((x) => x.domain)).toEqual(['Video.example']);
    expect(filterSites(table, 'tunnel', 'news')).toEqual([]);
  });

  it('does not reorder the input', () => {
    const before = table.map((x) => x.domain);
    filterSites(table, 'all', '');
    expect(table.map((x) => x.domain)).toEqual(before);
  });
});

describe('listView and texts', () => {
  const base = { engineDown: false, learning: false, loaded: true, total: 4, shown: 4, query: '' };
  it('picks the right state', () => {
    expect(listView(base)).toBe('list');
    expect(listView({ ...base, engineDown: true })).toBe('down');
    expect(listView({ ...base, learning: true })).toBe('list');
    expect(listView({ ...base, learning: true, total: 0, shown: 0 })).toBe('learning');
    expect(listView({ ...base, total: 0, shown: 0 })).toBe('learning');
    expect(listView({ ...base, loaded: false, total: 0, shown: 0 })).toBe('none-on-path');
    expect(listView({ ...base, shown: 0, query: 'zz' })).toBe('no-match');
    expect(listView({ ...base, shown: 0 })).toBe('none-on-path');
  });

  it('uses the docs/UI.md sentences', () => {
    expect(emptyText('learning', '')).toBe('Still learning this network. Sites will appear in a few seconds.');
    expect(emptyText('no-match', 'zz')).toBe('No sites match “zz”.');
    expect(emptyText('none-on-path', '')).toBe('No sites on this path yet.');
    expect(emptyText('down', '')).toBe('The engine is not running.');
    expect(countLine(1, '')).toBe('1 site');
    expect(countLine(3, 'vid')).toBe('3 sites matching “vid”');
  });
});

describe('side panel', () => {
  it('words the source', () => {
    expect(sourceWord('checked')).toBe('Checked on this network');
    expect(sourceWord('static')).toBe('From a fixed list');
    expect(sourceWord('default')).toBe('Safe default until checked');
    expect(sourceWord('???')).toBe('–');
  });

  it('describes the path with the user word and the engine name for Details', () => {
    const f = panelFacts(r('video.example', 'desync'), NOW);
    expect(f).toMatchObject({ word: 'Picky', long: 'Picky sites', engine: 'desync', dot: 'r-desync', decided: 'Checked on this network' });
    expect(f.since).toMatch(/^5 min ago \(/);
    expect(f.lastCheck).toBe('2 min ago (all sites)');
  });

  it('lists the history newest first', () => {
    const rows = historyRows([
      { Domain: 'a', From: 'direct', To: 'desync', At: '2026-11-01T10:00:00Z' },
      { Domain: 'a', From: 'desync', To: 'tunnel', At: '2026-11-01T11:00:00Z' },
    ]);
    expect(rows.map((x) => x.text)).toEqual(['Picky → Blocked', 'Everyday → Picky']);
    expect(rows.map((x) => x.tech)).toEqual(['desync → tunnel', 'direct → desync']);
    expect(rows[0]!.time).toMatch(/\d{1,2}[:.]\d{2}/);
    expect(historyRows(undefined)).toEqual([]);
  });
});

describe('rules of the user', () => {
  it('words the source "user" as Set by you', () => {
    expect(sourceWord('user')).toBe('Set by you');
    const f = panelFacts(r('ads.example', 'block', { source: 'user' }), NOW);
    expect(f).toMatchObject({ decided: 'Set by you', byUser: true, word: 'Blocked by you', engine: 'block' });
    expect(panelFacts(r('ads.example', 'block'), NOW).byUser).toBe(false);
  });

  it('puts a rule for block under Blocked by you, following the route the engine reports', () => {
    const rows = [...table, r('mine.example', 'block', { source: 'user' }), r('own.example', 'tunnel', { source: 'user' })];
    const tabs = siteTabs(rows);
    expect(tabs.find((t) => t.id === 'block')).toMatchObject({ label: 'Blocked by you', count: 1 });
    expect(tabs.find((t) => t.id === 'tunnel')?.count).toBe(2);
    expect(filterSites(rows, 'block', '').map((x) => x.domain)).toEqual(['mine.example']);
  });
});

describe('degraded sites (no detour server)', () => {
  it('flags only a Blocked-path row the engine marks degraded', () => {
    expect(isDegraded(r('a.example', 'tunnel', { degraded: true }))).toBe(true);
    expect(isDegraded(r('a.example', 'tunnel', { degraded: false }))).toBe(false);
    expect(isDegraded(r('a.example', 'tunnel'))).toBe(false); // older engine
    expect(isDegraded(r('a.example', 'direct', { degraded: true }))).toBe(false);
  });
  it('the side panel facts carry the flag; the copy has a label and an explaining sentence without network words', () => {
    expect(panelFacts(r('a.example', 'tunnel', { degraded: true }), NOW).degraded).toBe(true);
    expect(panelFacts(r('a.example', 'tunnel'), NOW).degraded).toBe(false);
    expect(pages.sites.noDetour.flag).toBe('No detour');
    expect(pages.sites.noDetour.note).toMatch(/opens directly/);
    expect(`${pages.sites.noDetour.flag} ${pages.sites.noDetour.note}`).not.toMatch(/tunnel|proxy|sing-?box|vpn|dns/i);
  });
});
