// Navigation and shared dialogs used by several pages.
import * as api from './api.js';
import { h, openDialog, copyText, toast, tabs, replace } from './dom.js';

/** Navigate: go('sites', {route:'tunnel'}) → #/sites?route=tunnel */
export function go(page, params) {
  const q = new URLSearchParams(params || {}).toString();
  const hash = `#/${page}${q ? '?' + q : ''}`;
  if (location.hash === hash) window.dispatchEvent(new HashChangeEvent('hashchange'));
  else location.hash = hash;
}

export function parseHash() {
  const raw = location.hash.replace(/^#\/?/, '');
  const [page, query] = raw.split('?');
  return { page: page || 'home', params: Object.fromEntries(new URLSearchParams(query || '')) };
}

/** Re-render part of a page without losing keyboard focus inside it. */
export function keepFocus(container, fn) {
  const sel = 'button, input, select, a[href], [tabindex]';
  const before = [...container.querySelectorAll(sel)];
  const idx = before.indexOf(document.activeElement);
  fn();
  if (idx >= 0) {
    const after = [...container.querySelectorAll(sel)];
    const target = after[Math.min(idx, after.length - 1)];
    if (target && !target.disabled) target.focus();
    else container.closest('main')?.focus();
  }
}

let setupOpen = false;
export function showSetupDialog(err) {
  if (setupOpen) return;
  const commands = (err && err.commands) || [];
  const text = commands.join('\n');
  setupOpen = true;
  const d = openDialog({
    title: 'One-time setup is needed on this computer.',
    body: [
      h('p', { text: 'Run these commands once in a terminal, then press Start again. The app never runs them itself.' }),
      h('pre', { class: 'code', tabIndex: 0, 'aria-label': 'Setup commands', text: text || '(no commands were given)' }),
    ],
    actions: [
      { label: 'Copy commands', primary: true, onClick: async () => { toast((await copyText(text)) ? 'Commands copied.' : 'Could not copy. Select the text instead.'); return true; } },
      { label: 'Close' },
    ],
  });
  d.el.addEventListener('close', () => { setupOpen = false; });
}

export const PROCS = [
  { id: 'splitter', label: 'splitter' },
  { id: 'sing-box', label: 'sing-box' },
  { id: 'desync', label: 'desync engine' },
];

/** Read-only engine log viewer with a tab per process and Copy. */
export function logViewer(initial = 'splitter') {
  let active = initial;
  let lines = [];
  const strip = h('div');
  const pre = h('pre', { class: 'code log', tabIndex: 0, role: 'tabpanel', 'aria-label': 'Engine log' });
  const status = h('p', { class: 'muted small', 'aria-live': 'polite' });

  async function load() {
    replace(strip, tabs(PROCS, active, (id) => { active = id; load(); }, 'Engine processes'));
    pre.setAttribute('aria-labelledby', 'tab-' + active);
    status.textContent = 'Loading…';
    try {
      const r = await api.getLogs(active);
      lines = r.lines;
      pre.textContent = lines.length ? lines.join('\n') : '(empty)';
      status.textContent = `${lines.length} line${lines.length === 1 ? '' : 's'}`;
      pre.scrollTop = pre.scrollHeight;
    } catch (e) {
      lines = [];
      pre.textContent = '';
      status.textContent = e.message;
    }
  }
  const el = h('div', { class: 'logviewer' }, strip, pre,
    h('div', { class: 'row' }, status, h('span', { class: 'grow' }),
      h('button', { class: 'btn small', type: 'button', text: 'Refresh', onclick: load }),
      h('button', { class: 'btn small', type: 'button', text: 'Copy', onclick: async () => toast((await copyText(lines.join('\n'))) ? 'Log copied.' : 'Could not copy.') })));
  load();
  return el;
}

export function showLogDialog(name) {
  openDialog({ title: 'Engine log', body: logViewer(name), actions: [{ label: 'Close', primary: true }] });
}
