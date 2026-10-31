// Pure view model for Help (docs/UI.md 5.5): processes and their states, version info, recovery steps.

import { processes as processCopy } from '../../design/copy';
import type { ProcessInfo, Version } from '../backend/types';

export type Tone = 'ok' | 'idle' | 'warn';
export interface ProcessRow { name: string; label: string; state: string; tone: Tone; restarts: string }

const STATE_WORDS: Record<string, string> = {
  running: 'Running',
  stopped: 'Stopped',
  starting: 'Starting',
  stopping: 'Stopping',
  failed: 'Stopped after a failure',
  backoff: 'Restarting',
};

export function stateWord(state: string): string {
  return STATE_WORDS[state] ?? (state ? state.charAt(0).toUpperCase() + state.slice(1) : '–');
}

/** Running is good, a plain stop or a change in progress is neutral, anything else (failed, restarting) is a warning. */
export function stateTone(state: string): Tone {
  if (state === 'running') return 'ok';
  if (state === 'stopped' || state === 'starting' || state === 'stopping') return 'idle';
  return 'warn';
}

/** One row per engine process, in plain words. */
export function processRows(list: ProcessInfo[] | undefined): ProcessRow[] {
  return (list ?? []).map((p) => ({
    name: p.Name,
    label: (processCopy as Record<string, string>)[p.Name] ?? p.Name,
    state: stateWord(p.State),
    tone: stateTone(p.State),
    restarts: p.Restarts > 0 ? (p.Restarts === 1 ? 'restarted once' : `restarted ${p.Restarts} times`) : '',
  }));
}

export interface VersionRow { term: string; value: string }

export function versionRows(ui: string, v: Version | null): VersionRow[] {
  const rows: VersionRow[] = [{ term: 'App (UI)', value: ui || '–' }];
  if (!v) return rows;
  rows.push(
    { term: 'Engine (splitter)', value: v.splitter || '–' },
    { term: 'sing-box', value: v.singBox || 'not reported' },
    { term: 'Desync engine', value: v.desync || 'not reported' },
    { term: 'Control API', value: v.api || '–' },
  );
  return rows;
}

export interface RecoveryStep { title: string; text: string; code?: string }

export const recoveryLead = 'Keep these steps where you can read them offline. Use an administrator PowerShell; after each step, check whether the internet is back.';
export const recoverySteps: RecoveryStep[] = [
  { title: 'Stop everything. ', text: 'This fixes it in most cases.', code: 'Stop-Process -Name splitter, sing-box, winws2 -Force -ErrorAction SilentlyContinue' },
  { title: 'Turn off the leftover network adapter. ', text: 'It is named "splitter".', code: 'Disable-NetAdapter -Name "splitter" -Confirm:$false' },
  { title: 'Unload the packet driver ', text: 'only if packets still seem dropped. A reboot does this too.' },
  { title: 'Reset DNS and the network stack. ', text: '', code: 'ipconfig /flushdns' },
  { title: '', text: 'The full steps are in docs/RECOVERY.md.' },
];

export const settingsLinks = [
  { label: 'Detour server', tab: 'detour' },
  { label: 'Advanced settings', tab: 'advanced' },
  { label: 'Versions and licenses', tab: 'about' },
] as const;
