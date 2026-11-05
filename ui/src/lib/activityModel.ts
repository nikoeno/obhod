// Pure view model for Activity (docs/UI.md 5.3): changes, traffic charts, network, warnings.

import { pages, routes as routeCopy } from '../../design/copy';
import type { Change, NetworkSeen, Route, Snapshot } from '../backend/types';
import { explainWarning, isLearning } from './errors';
import { fill, fmtAgo, fmtBytes, fmtIn, fmtTime } from './format';
import { routeWord } from './sitesModel';

const t = pages.activity;

export const ACTIVITY_TABS = ['changes', 'traffic', 'network', 'warnings'] as const;
export type ActivityTab = (typeof ACTIVITY_TABS)[number];

export function parseTab(v: string | undefined): ActivityTab {
  return (ACTIVITY_TABS as readonly string[]).includes(v ?? '') ? (v as ActivityTab) : 'changes';
}

/** Most recent changes kept on screen; the engine itself sends the last 20 in each snapshot. */
export const RECENT_CAP = 50;

const sameChange = (a: Change, b: Change): boolean => a.Domain === b.Domain && a.At === b.At && a.From === b.From && a.To === b.To;

/** Adds the changes of a `routes` event to the snapshot's Recent list (oldest first), without duplicates. */
export function mergeChanges(recent: Change[], incoming: Change[], cap = RECENT_CAP): Change[] {
  const out = recent.slice();
  for (const c of incoming) if (!out.some((x) => sameChange(x, c))) out.push(c);
  return out.length > cap ? out.slice(out.length - cap) : out;
}

export interface ChangeRow { key: string; site: string; from: string; to: string; time: string; text: string }

/** Timeline, newest first. "Clear view" hides everything up to `clearedAt` (ms) on screen only. */
export function changeRows(recent: Change[] | undefined, clearedAt: number): ChangeRow[] {
  return (recent ?? [])
    .filter((c) => Date.parse(c.At) > clearedAt)
    .slice()
    .reverse()
    .map((c, i) => {
      const from = routeWord(c.From);
      const to = routeWord(c.To);
      const time = fmtTime(c.At);
      return { key: `${c.Domain}|${c.At}|${i}`, site: c.Domain, from, to, time, text: fill(t.change, { site: c.Domain, from, to, time }) };
    });
}

export const CHART_ROUTES: Route[] = ['direct', 'tunnel', 'desync', 'proxy'];
export const SERIES_SLOTS = 30;

export interface Chart { route: Route; dot: string; title: string; today: string; label: string; max: number; values: number[] }

/** One chart per path; Via proxy only when it has traffic. All charts share one scale. */
export function charts(traffic: Partial<Record<Route, number>> | undefined, series: Record<string, number[]>): Chart[] {
  const visible = CHART_ROUTES.filter((r) => r !== 'proxy' || (traffic?.[r] ?? 0) > 0);
  const max = Math.max(1, ...visible.flatMap((r) => series[r] ?? []));
  return visible.map((r) => {
    const s = series[r] ?? [];
    const title = routeCopy[r].long;
    return {
      route: r,
      dot: `r-${r}`,
      title,
      today: `${fmtBytes(traffic?.[r] ?? 0)} today`,
      max,
      values: s,
      label: `${title}: traffic per 2 seconds this session, last ${s.length} samples, latest ${fmtBytes(s[s.length - 1] ?? 0)}`,
    };
  });
}

export interface Bar { x: number; y: number; width: number; height: number }
export const CHART_W = 300;
export const CHART_H = 60;
const round1 = (n: number): number => Math.round(n * 10) / 10;

/** Bars for the SVG: newest on the right; a zero sample draws nothing; a non-zero one at least 2 units tall. */
export function bars(series: number[], max: number, slots = SERIES_SLOTS): Bar[] {
  const bw = CHART_W / slots;
  const shown = series.slice(-slots);
  const pad = slots - shown.length;
  const top = Math.max(1, max);
  return shown.map((v, i) => {
    const h = v > 0 ? Math.max(2, (v / top) * (CHART_H - 4)) : 0;
    return { x: round1((pad + i) * bw + 1), y: round1(CHART_H - h), width: round1(bw - 2), height: round1(h) };
  });
}

export interface Stat { label: string; value: string }

export function trafficStats(tr: { uploadTotal: number; downloadTotal: number; connections: number }): Stat[] {
  return [
    { label: 'Downloaded', value: fmtBytes(tr.downloadTotal) },
    { label: 'Uploaded', value: fmtBytes(tr.uploadTotal) },
    { label: 'Connections', value: String(tr.connections) },
  ];
}

export const networkLabel = (label: string | undefined): string => (isLearning(label) ? pages.home.learning : (label as string));

export interface NetworkFact { term: string; value: string }

export function networkFacts(s: Pick<Snapshot, 'Network' | 'LastCheck' | 'NextCheck'> | null, checking: boolean, now = Date.now()): NetworkFact[] {
  // "10 min ago (14:02)"; with an unknown time just "never" / "–".
  const when = (rel: string, iso: string): string => (fmtTime(iso) === '–' ? rel : `${rel} (${fmtTime(iso)})`);
  return [
    { term: 'Network', value: networkLabel(s?.Network) },
    { term: 'Last check', value: checking ? 'Checking…' : when(fmtAgo(s?.LastCheck ?? '', now), s?.LastCheck ?? '') },
    { term: t.nextCheck, value: when(fmtIn(s?.NextCheck ?? '', now), s?.NextCheck ?? '') },
  ];
}

/** Networks seen this session, newest first (labels only, never an address). */
export function networkRows(nets: NetworkSeen[]): Array<{ label: string; since: string }> {
  return nets
    .slice()
    .reverse()
    .map((n) => ({ label: networkLabel(n.label), since: `since ${fmtTime(n.since)}` }));
}

export interface WarningRow { text: string; why: string; todo: string }

export function warningRows(list: string[] | undefined): WarningRow[] {
  return (list ?? []).map((w) => ({ text: w, ...explainWarning(w) }));
}
