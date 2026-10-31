// Pure view model for Sites (docs/UI.md 5.2): tabs, search, list state, the side panel facts.
// No Vue here, so it is unit tested directly.

import { extraRoutes, mainRoutes, pages, routes as routeCopy } from '../../design/copy';
import type { Change, Route, RouteInfo } from '../backend/types';
import { fill, fmtAgo, fmtTime, plural } from './format';

export type SiteFilter = 'all' | Route;

const t = pages.sites;

export const isRoute = (v: string | undefined): v is Route => !!v && Object.prototype.hasOwnProperty.call(routeCopy, v);

/** The `route` query parameter of `#/sites?route=tunnel`; anything unknown means "all". */
export function parseFilter(v: string | undefined): SiteFilter {
  return isRoute(v) ? v : 'all';
}

export interface SiteTab { id: SiteFilter; label: string; count: number; dot?: string }

/** All, the three main paths, and Via proxy / Blocked by you only when they have entries. */
export function siteTabs(routes: RouteInfo[]): SiteTab[] {
  const count = (r: Route): number => routes.filter((x) => x.route === r).length;
  const tabs: SiteTab[] = [{ id: 'all', label: t.tabAll, count: routes.length }];
  for (const r of mainRoutes) tabs.push({ id: r, label: routeCopy[r].word, count: count(r), dot: `r-${r}` });
  for (const r of extraRoutes) if (count(r) > 0) tabs.push({ id: r, label: routeCopy[r].word, count: count(r), dot: `r-${r}` });
  return tabs;
}

/** A filter whose tab has disappeared (an extra path became empty) falls back to "all". */
export function effectiveFilter(filter: SiteFilter, tabs: SiteTab[]): SiteFilter {
  return tabs.some((x) => x.id === filter) ? filter : 'all';
}

/** Substring search on the domain (case-insensitive), then the path filter; sorted by domain so rows do not jump. */
export function filterSites(routes: RouteInfo[], filter: SiteFilter, query: string): RouteInfo[] {
  const q = query.trim().toLowerCase();
  return routes
    .filter((r) => (filter === 'all' || r.route === filter) && (!q || r.domain.toLowerCase().includes(q)))
    .sort((a, b) => (a.domain < b.domain ? -1 : a.domain > b.domain ? 1 : 0));
}

export type ListView = 'down' | 'learning' | 'no-match' | 'none-on-path' | 'list';

export function listView(o: { engineDown: boolean; learning: boolean; loaded: boolean; total: number; shown: number; query: string }): ListView {
  if (o.engineDown) return 'down';
  if (o.learning || (o.loaded && o.total === 0)) return 'learning';
  if (o.shown === 0) return o.query.trim() ? 'no-match' : 'none-on-path';
  return 'list';
}

/** "3 sites" or "3 sites matching “x”". */
export function countLine(n: number, query: string): string {
  return plural(t.count, n) + (query ? fill(t.countMatching, { query }) : '');
}

export function emptyText(view: ListView, query: string): string {
  switch (view) {
    case 'down':
      return 'The engine is not running.';
    case 'learning':
      return t.empty;
    case 'no-match':
      return fill(t.noMatch, { query });
    case 'none-on-path':
      return t.noneOnPath;
    default:
      return '';
  }
}

/** How the engine decided (`Route.source`); "–" for an unknown source. */
export function sourceWord(source: string): string {
  return (t.sources as Record<string, string>)[source] ?? '–';
}

export const routeWord = (r: string): string => routeCopy[r as Route]?.word ?? r;

export interface HistoryRow { text: string; time: string; tech: string }

/** Route changes of one site, newest first. The engine name is the Details-only tech tag. */
export function historyRows(history: Change[] | undefined): HistoryRow[] {
  return (history ?? [])
    .slice()
    .reverse()
    .map((c) => ({ text: `${routeWord(c.From)} → ${routeWord(c.To)}`, time: fmtTime(c.At), tech: `${c.From} → ${c.To}` }));
}

export interface PanelFacts { word: string; long: string; engine: string; dot: string; since: string; decided: string; lastCheck: string }

export function panelFacts(r: RouteInfo, now = Date.now()): PanelFacts {
  const meta = routeCopy[r.route];
  return {
    word: meta?.word ?? r.route,
    long: meta?.long ?? r.route,
    engine: r.route,
    dot: `r-${r.route}`,
    since: `${fmtAgo(r.since, now)} (${fmtTime(r.since)})`,
    decided: sourceWord(r.source),
    lastCheck: `${fmtAgo(r.lastCheck, now)} (all sites)`,
  };
}
