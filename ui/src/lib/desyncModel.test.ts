import { describe, expect, it } from 'vitest';
import { pages } from '../../design/copy';
import type { Desync, DesyncPreset } from '../backend/types';
import { AUTO, choices, CUSTOM, desyncErrorText, homeLine, nextProgress, outcomeText, panelState, presetDescription, presetName, selectedId, siteCountText, siteCounts, statusView, triedLines } from './desyncModel';

const presets: DesyncPreset[] = [
  { id: 'alt2-multisplit', name: 'ALT2 (multisplit)', description: 'engine text' },
  { id: 'alt3-hostfakesplit', name: 'ALT3 (host fake split)', description: 'engine text 3' },
  { id: 'future-one', name: 'Future', description: 'Only the engine knows this one.' },
];
const preset: Desync = { mode: 'preset', preset: 'alt2-multisplit', searching: false, lastSearch: null };
const auto: Desync = { mode: 'auto', preset: 'alt3-hostfakesplit', searching: false, lastSearch: null };
const found: Desync = {
  ...auto,
  lastSearch: { at: '2026-10-31T12:05:00Z', tried: [{ preset: 'alt2-multisplit', fixed: 0, total: 3 }, { preset: 'alt3-hostfakesplit', fixed: 3, total: 3 }], chosen: 'alt3-hostfakesplit', outcome: 'chosen' },
};

describe('choices', () => {
  it('puts Automatic first and keeps the engine order', () => {
    const c = choices(presets, preset);
    expect(c.map((x) => x.id)).toEqual([AUTO, 'alt2-multisplit', 'alt3-hostfakesplit', 'future-one']);
    expect(c[0]!.kind).toBe('auto');
  });
  it('offers "your own setting" only while custom is in use', () => {
    expect(choices(presets, { mode: 'custom' })[1]!.id).toBe(CUSTOM);
    expect(choices(presets, auto).some((x) => x.id === CUSTOM)).toBe(false);
    expect(choices(presets, null).some((x) => x.id === CUSTOM)).toBe(false);
  });
  it('uses our words for known ids and the engine text for a new one', () => {
    expect(presetDescription(presets[0]!)).toBe(pages.settings.desync.presets['alt2-multisplit']);
    expect(presetDescription(presets[2]!)).toBe('Only the engine knows this one.');
  });
  it('has plain copy for every id the engine ships', () => {
    // The 21 ids of internal/pc/desync/presets.go.
    expect(Object.keys(pages.settings.desync.presets)).toHaveLength(21);
  });
});

describe('selectedId and names', () => {
  it('maps mode and preset to a radio', () => {
    expect(selectedId(null)).toBe('');
    expect(selectedId(preset)).toBe('alt2-multisplit');
    expect(selectedId(auto)).toBe(AUTO);
    expect(selectedId({ mode: 'custom', preset: 'custom' })).toBe(CUSTOM);
  });
  it('falls back to the id for an unknown preset', () => {
    expect(presetName(presets, 'alt3-hostfakesplit')).toBe('ALT3 (host fake split)');
    expect(presetName(presets, 'gone')).toBe('gone');
    expect(presetName(presets, AUTO)).toBe('Automatic');
  });
});

describe('panelState', () => {
  it('orders engine down, ready, failed, loading', () => {
    expect(panelState({ engineDown: true, loaded: true, failed: false })).toBe('engine-down');
    expect(panelState({ engineDown: false, loaded: true, failed: true })).toBe('ready');
    expect(panelState({ engineDown: false, loaded: false, failed: true })).toBe('failed');
    expect(panelState({ engineDown: false, loaded: false, failed: false })).toBe('loading');
  });
});

describe('progress from events', () => {
  it('follows searching, trying and done', () => {
    let p = nextProgress(null, { phase: 'searching', total: 6 });
    expect(p).toEqual({ index: 0, total: 6, preset: '' });
    p = nextProgress(p, { phase: 'trying', index: 2, total: 6, preset: 'alt3-hostfakesplit' });
    expect(p).toEqual({ index: 2, total: 6, preset: 'alt3-hostfakesplit' });
    expect(nextProgress(p, { phase: 'done', outcome: 'chosen', chosen: 'x', fixed: 1, of: 1 })).toBeNull();
  });
});

describe('site counts', () => {
  it('counts desync states and routed sites', () => {
    const c = siteCounts([
      { route: 'desync', desync: 'verified' }, { route: 'desync', desync: 'pending' }, { route: 'desync', desync: 'pending' },
      { route: 'tunnel', desync: 'failed' }, { route: 'direct' },
    ]);
    expect(c).toEqual({ verified: 1, pending: 2, failed: 1, routed: 3 });
    expect(siteCountText(c)).toBe('1 working · 2 waiting · 1 not reached');
    expect(siteCountText({ verified: 0, pending: 0, failed: 0, routed: 0 })).toBe('');
  });
});

describe('statusView', () => {
  const base = { info: auto, presets, progress: null, engineDown: false, state: 'connected' as const, network: 'Home Wi-Fi', routed: 3 };
  it('says a search is running, with a count when known', () => {
    expect(statusView({ ...base, info: { ...auto, searching: true } })).toMatchObject({ line: 'Searching for the best fix…', fraction: null, canSearch: false });
    const v = statusView({ ...base, info: { ...auto, searching: true }, progress: { index: 2, total: 6, preset: 'alt3-hostfakesplit' } });
    expect(v.line).toBe('Trying fix 2 of 6: ALT3 (host fake split)');
    expect(v.fraction).toBeCloseTo(1 / 3);
    expect(v.why).toBe(pages.settings.desync.searchAgainWhy.searching);
  });
  it('shows the preset in use for the network after a search and allows another', () => {
    const v = statusView({ ...base, info: found });
    expect(v.line).toBe('Using on Home Wi-Fi: ALT3 (host fake split)');
    expect(v.canSearch).toBe(true);
    expect(statusView({ ...base, info: found, network: 'unknown network' }).line).toBe('Using: ALT3 (host fake split)');
  });
  it('explains why search is not possible', () => {
    expect(statusView({ ...base, engineDown: true }).why).toBe('The engine is not running.');
    expect(statusView({ ...base, state: 'disconnected' })).toMatchObject({ canSearch: false, line: 'Press Start to let the app search.', why: 'Press Start first.' });
    expect(statusView({ ...base, routed: 0 })).toMatchObject({ canSearch: false, line: expect.stringContaining('nothing to search') });
    expect(statusView({ ...base, info: preset }).why).toBe('Choose Automatic first.');
    expect(statusView(base)).toMatchObject({ canSearch: true, line: 'No search has finished yet in this session.' });
  });
});

describe('search results', () => {
  it('words every outcome and fills the counts', () => {
    const s = found.lastSearch!;
    expect(outcomeText(s, presets)).toBe('Found a fix that works: ALT3 (host fake split). It reaches 3 of 3 sites.');
    for (const o of ['none', 'inconclusive', 'error', 'canceled']) expect(outcomeText({ ...s, outcome: o, chosen: '' }, presets)).not.toBe('');
    expect(outcomeText({ ...s, outcome: 'new-outcome' }, presets)).toBe('');
  });
  it('lists what was tried and marks the chosen one', () => {
    const l = triedLines(found.lastSearch!, presets);
    expect(l.map((x) => x.text)).toEqual(['ALT2 (multisplit): 0 of 3', 'ALT3 (host fake split): 3 of 3']);
    expect(l.map((x) => x.chosen)).toEqual([false, true]);
  });
});

describe('desyncErrorText', () => {
  it('uses fixed sentences and never the engine message', () => {
    expect(desyncErrorText({ code: 'program_missing', message: 'C:\\secret\\path' })).toContain('Reinstall');
    expect(desyncErrorText({ code: 'not_auto' })).toBe('Choose Automatic first.');
    expect(desyncErrorText({ code: 'whatever', message: 'leak' })).toBe('Something went wrong.');
    expect(desyncErrorText(null)).toBe('Something went wrong.');
  });
});

describe('homeLine', () => {
  const o = { engineDown: false, routed: 2, info: preset, presets, progress: null };
  it('is hidden without a desync site, without data or with the engine down', () => {
    expect(homeLine({ ...o, routed: 0 })).toBeNull();
    expect(homeLine({ ...o, info: null })).toBeNull();
    expect(homeLine({ ...o, engineDown: true })).toBeNull();
  });
  it('names the preset, the automatic pick, a running search or the custom setting', () => {
    expect(homeLine(o)!.text).toBe('Picky sites use: ALT2 (multisplit)');
    expect(homeLine({ ...o, info: auto })!.text).toBe('Picky sites, automatic: ALT3 (host fake split)');
    expect(homeLine({ ...o, info: { ...auto, searching: true } })!.text).toContain('searching');
    expect(homeLine({ ...o, info: { mode: 'custom', preset: 'custom', searching: false, lastSearch: null } })!.text).toContain('own setting');
  });
});
