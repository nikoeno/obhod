// Pure view models for the desync (Picky sites) choice: the Settings tab and the line on Home.
// No Vue here, so they are unit tested directly. The engine owns the catalogue and the search;
// this module only turns its answers into words (design/copy.ts).

import { pages } from '../../design/copy';
import type { ConnState, Desync, DesyncEvent, DesyncPreset, DesyncSearch, RouteInfo } from '../backend/types';
import { fill } from './format';

const t = pages.settings.desync;
const h = pages.home.desyncLine;

/** The id of the "Automatic" choice (same as the engine's `auto`). */
export const AUTO = 'auto';
/** The id of the engine's custom mode; shown, never offered. */
export const CUSTOM = 'custom';

/** Where a running search is, from the `desync` events. */
export interface DesyncProgress {
  /** Presets tried so far (0 while the search starts). */
  index: number;
  total: number;
  /** Catalogue id being tried, or '' before the first one. */
  preset: string;
}

/** The next progress after a `desync` event; null when the search ended. */
export function nextProgress(prev: DesyncProgress | null, ev: DesyncEvent): DesyncProgress | null {
  switch (ev.phase) {
    case 'searching':
      return { index: 0, total: Math.max(0, ev.total | 0), preset: '' };
    case 'trying':
      return { index: Math.max(0, ev.index | 0), total: Math.max(0, ev.total | 0), preset: typeof ev.preset === 'string' ? ev.preset : '' };
    case 'done':
      return null;
    default:
      return prev;
  }
}

export interface PresetChoice {
  id: string;
  name: string;
  description: string;
  kind: 'auto' | 'preset' | 'custom';
}

const KNOWN_COPY = t.presets as Record<string, string>;

/** Plain words for a catalogue preset: ours when we know the id, else the engine's own sentence. */
export function presetDescription(p: Pick<DesyncPreset, 'id' | 'description'>): string {
  return KNOWN_COPY[p.id] ?? p.description;
}

/** The name of a catalogue id for display; a plain fallback to the id itself for one this build does not know. */
export function presetName(presets: readonly DesyncPreset[], id: string): string {
  if (id === AUTO) return t.auto.name;
  if (id === CUSTOM) return t.custom.name;
  return presets.find((p) => p.id === id)?.name ?? id;
}

/** The radio list: Automatic first, the catalogue in the engine's order, and "your own setting" only while it is in use. */
export function choices(presets: readonly DesyncPreset[], info: Pick<Desync, 'mode'> | null): PresetChoice[] {
  const out: PresetChoice[] = [{ id: AUTO, name: t.auto.name, description: t.auto.description, kind: 'auto' }];
  if (info?.mode === 'custom') out.push({ id: CUSTOM, name: t.custom.name, description: t.custom.description, kind: 'custom' });
  for (const p of presets) out.push({ id: p.id, name: p.name, description: presetDescription(p), kind: 'preset' });
  return out;
}

/** Which radio is selected: `auto`, `custom` or the preset in use. Empty while nothing was read. */
export function selectedId(info: Pick<Desync, 'mode' | 'preset'> | null): string {
  if (!info) return '';
  if (info.mode === 'auto') return AUTO;
  if (info.mode === 'custom') return CUSTOM;
  return info.preset;
}

export type PanelState = 'loading' | 'engine-down' | 'failed' | 'ready';

export function panelState(o: { engineDown: boolean; loaded: boolean; failed: boolean }): PanelState {
  if (o.engineDown) return 'engine-down';
  if (o.loaded) return 'ready';
  return o.failed ? 'failed' : 'loading';
}

/** Counts of the `desync` states of the table (set only in automatic mode). */
export interface SiteCounts { verified: number; pending: number; failed: number; routed: number }

export function siteCounts(routes: readonly Pick<RouteInfo, 'route' | 'desync'>[]): SiteCounts {
  const c: SiteCounts = { verified: 0, pending: 0, failed: 0, routed: 0 };
  for (const r of routes) {
    if (r.route === 'desync') c.routed++;
    if (r.desync === 'verified') c.verified++;
    else if (r.desync === 'pending') c.pending++;
    else if (r.desync === 'failed') c.failed++;
  }
  return c;
}

export function siteCountText(c: SiteCounts): string {
  const parts: string[] = [];
  if (c.verified) parts.push(fill(t.status.sites.verified, { n: String(c.verified) }));
  if (c.pending) parts.push(fill(t.status.sites.pending, { n: String(c.pending) }));
  if (c.failed) parts.push(fill(t.status.sites.failed, { n: String(c.failed) }));
  return parts.join(' · ');
}

/** The sentence for a finished search, with the preset's name. Empty for an unknown outcome. */
export function outcomeText(s: Pick<DesyncSearch, 'outcome' | 'chosen' | 'tried'>, presets: readonly DesyncPreset[]): string {
  const o = t.status.outcome as Record<string, string>;
  const text = o[s.outcome];
  if (!text) return '';
  const hit = s.tried.find((x) => x.preset === s.chosen);
  return fill(text, { name: presetName(presets, s.chosen), fixed: String(hit?.fixed ?? 0), of: String(hit?.total ?? 0) });
}

export interface TriedLine { key: string; text: string; chosen: boolean }

export function triedLines(s: Pick<DesyncSearch, 'tried' | 'chosen'>, presets: readonly DesyncPreset[]): TriedLine[] {
  return s.tried.map((x, i) => ({
    key: `${i}-${x.preset}`,
    text: fill(t.status.tried, { name: presetName(presets, x.preset), fixed: String(x.fixed), total: String(x.total) }),
    chosen: !!s.chosen && x.preset === s.chosen,
  }));
}

export interface StatusInput {
  info: Desync | null;
  presets: readonly DesyncPreset[];
  progress: DesyncProgress | null;
  engineDown: boolean;
  state: ConnState;
  network: string;
  /** Sites routed to desync (route `desync`). */
  routed: number;
}

export interface StatusView {
  /** The headline of the status block. */
  line: string;
  /** Progress, 0 to 1, or null when no search runs or the count is unknown. */
  fraction: number | null;
  /** "Search again" is available; otherwise `why` says in words why not. */
  canSearch: boolean;
  why: string;
}

/** The Automatic search block. Only meaningful while the mode is `auto`. */
export function statusView(o: StatusInput): StatusView {
  const searching = !!o.info?.searching || !!o.progress;
  const why = searchBlock(o, searching);
  let line: string;
  let fraction: number | null = null;
  if (searching) {
    const p = o.progress;
    if (p && p.index > 0 && p.total > 0) {
      line = fill(t.status.trying, { index: String(p.index), total: String(p.total), name: presetName(o.presets, p.preset) });
      fraction = Math.min(1, p.index / p.total);
    } else {
      line = t.status.searching;
    }
  } else if (o.routed === 0 && o.state === 'connected') {
    line = t.status.noSites;
  } else if (o.state !== 'connected') {
    line = t.status.waitingStart;
  } else if (!o.info?.lastSearch) {
    line = t.status.idle;
  } else {
    line = usingLine(o);
  }
  return { line, fraction, canSearch: why === '', why };
}

function usingLine(o: StatusInput): string {
  const name = presetName(o.presets, o.info?.preset ?? '');
  return o.network && o.network !== 'unknown network' ? fill(t.status.using, { network: o.network, name }) : fill(t.status.usingUnknown, { name });
}

function searchBlock(o: StatusInput, searching: boolean): string {
  const w = t.searchAgainWhy;
  if (o.engineDown) return w.engineDown;
  if (searching) return w.searching;
  if (o.info?.mode !== 'auto') return w.notAuto;
  if (o.state !== 'connected') return w.notConnected;
  if (o.routed === 0) return w.noSites;
  return '';
}

/** The words for a failed desync call (set or search); never the engine's own text except fixed sentences. */
export function desyncErrorText(e: { code?: string; message?: string } | null | undefined): string {
  const m = t.errors as Record<string, string>;
  const code = e?.code ?? '';
  return m[code] ?? t.errors.unknown;
}

/** The Home line, or null when no site uses the quiet fix (or the engine is down / nothing was read). */
export interface HomeLine { text: string; label: string }

export function homeLine(o: { engineDown: boolean; routed: number; info: Desync | null; presets: readonly DesyncPreset[]; progress: DesyncProgress | null }): HomeLine | null {
  if (o.engineDown || o.routed <= 0 || !o.info) return null;
  const name = presetName(o.presets, o.info.preset);
  let text: string;
  if (o.info.mode === 'custom') text = h.custom;
  else if (o.info.mode === 'auto') text = o.info.searching || o.progress ? h.autoSearching : fill(h.auto, { name });
  else text = fill(h.preset, { name });
  return { text, label: h.label };
}
