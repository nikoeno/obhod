import { describe, expect, it } from 'vitest';
import { processRows, recoverySteps, settingsLinks, stateTone, stateWord, versionRows } from './helpModel';

describe('processRows', () => {
  it('names processes for the user and keeps the engine name for Details', () => {
    const rows = processRows([
      { Name: 'splitter', State: 'running', Restarts: 0 },
      { Name: 'sing-box', State: 'failed', Restarts: 3 },
      { Name: 'desync', State: 'stopped', Restarts: 1 },
    ]);
    expect(rows.map((r) => r.label)).toEqual(['splitter', 'sing-box', 'desync engine']);
    expect(rows.map((r) => r.tone)).toEqual(['ok', 'warn', 'idle']);
    expect(rows.map((r) => r.restarts)).toEqual(['', 'restarted 3 times', 'restarted once']);
    expect(rows[1]!.state).toBe('Stopped after a failure');
  });
  it('handles no list and unknown states', () => {
    expect(processRows(undefined)).toEqual([]);
    expect(stateWord('weird')).toBe('Weird');
    expect(stateWord('')).toBe('–');
    expect(stateTone('weird')).toBe('warn');
    expect(stateTone('starting')).toBe('idle');
  });
});

describe('versionRows', () => {
  it('shows only the app version without an engine', () => {
    expect(versionRows('0.3.0', null)).toEqual([{ term: 'App (UI)', value: '0.3.0' }]);
  });
  it('marks versions the engine does not report', () => {
    const rows = versionRows('0.3.0', { splitter: 'splitter 1', singBox: '', desync: 'z 1', api: 'v1' });
    expect(rows.map((r) => r.value)).toEqual(['0.3.0', 'splitter 1', 'not reported', 'z 1', 'v1']);
  });
});

describe('static help content', () => {
  it('links to real settings tabs and points at docs/RECOVERY.md', () => {
    expect(settingsLinks.map((l) => l.tab)).toEqual(['detour', 'advanced', 'about']);
    expect(recoverySteps.at(-1)!.text).toContain('docs/RECOVERY.md');
  });
});
