// App shell: router, live updates, keyboard shortcuts (docs/UI.md §8), dev panel.
import * as api from './api.js';
import { h, replace, toast } from './dom.js';
import { S, onChange, refresh, startLive, toggleConnection, checkNow, quit } from './store.js';
import { go, parseHash, showSetupDialog } from './shell.js';
import { errorInfo } from './words.js';
import * as home from './pages/home.js';
import * as sites from './pages/sites.js';
import * as activity from './pages/activity.js';
import * as settings from './pages/settings.js';
import * as help from './pages/help.js';

const PAGES = { home, sites, activity, settings, help };
const TITLES = { home: 'Home', sites: 'Sites', activity: 'Activity', settings: 'Settings', help: 'Help' };
const main = document.getElementById('main');
const navStatus = document.getElementById('nav-status');

let current = null; // {name, page}

function route() {
  if (S.quit) return;
  const { page, params } = parseHash();
  const name = PAGES[page] ? page : 'home';
  if (current && current.page.destroy) current.page.destroy();
  const p = PAGES[name].create(params);
  current = { name, page: p };
  replace(main, p.el);
  for (const a of document.querySelectorAll('.nav a')) {
    if (a.dataset.page === name) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
  }
  document.title = `${TITLES[name]} · Obhod`;
}

function stateWord() {
  if (S.engineDown) return 'Engine not running';
  return { connected: 'On', connecting: 'Starting…', disconnecting: 'Stopping…' }[S.state] || (S.error ? 'Off (error)' : 'Off');
}

onChange((topic, extra) => {
  if (topic === 'quit') return showQuit();
  if (topic === 'state') {
    navStatus.textContent = stateWord();
    navStatus.className = 'nav-status ' + (S.engineDown ? 'down' : S.state);
    if (extra && extra.newError) {
      const info = errorInfo(extra.newError, S.state);
      if (info.code === 'needs_setup') showSetupDialog(extra.newError);
      else if (S.state === 'connected') toast(info.sentence); // a failed server switch; the banner on Home stays
    }
  }
  if (current && current.page.update) current.page.update(topic, extra);
});

function showQuit() {
  if (current && current.page.destroy) current.page.destroy();
  current = null;
  document.body.classList.add('quit');
  replace(main, h('section', { class: 'page quitpage' },
    h('h1', { text: 'Obhod has quit.' }),
    h('p', { class: 'muted', text: 'In the real app the window closes and the engine stops. The mock only acknowledged the request.' }),
    h('button', { class: 'btn primary', type: 'button', text: 'Open again', onclick: () => location.reload() })));
  main.focus();
}

// ---------- keyboard ----------

document.addEventListener('keydown', (e) => {
  const mod = e.ctrlKey || e.metaKey;
  if (mod && e.shiftKey && (e.key === 'D' || e.key === 'd')) { e.preventDefault(); toggleDevPanel(); return; }
  if (document.querySelector('dialog[open]')) return; // dialogs handle their own keys (Esc closes)
  if (e.key === 'Escape') {
    if (current && current.page.escape && current.page.escape()) e.preventDefault();
    else if (devPanel && !devPanel.hidden) { devPanel.hidden = true; }
    return;
  }
  if (!mod || e.altKey) return;
  if (e.key === 'Enter') { e.preventDefault(); toggleConnection(); }
  else if (['1', '2', '3', '4'].includes(e.key)) { e.preventDefault(); go(['home', 'sites', 'activity', 'settings'][Number(e.key) - 1]); main.focus(); }
  else if (e.key === 'f' || e.key === 'F') {
    e.preventDefault();
    if (current && current.name !== 'sites') go('sites');
    requestAnimationFrame(() => current && current.page.focusSearch && current.page.focusSearch());
  } else if (e.key === 'r' || e.key === 'R') { e.preventDefault(); if (!S.checking) { checkNow(); toast('Checking all sites again…'); } }
  else if (e.key === 'q' || e.key === 'Q') { e.preventDefault(); quit(); }
});

// ---------- dev panel (mock only, Ctrl+Shift+D) ----------

let devPanel = null;
const SCENARIOS = [
  ['normal', 'Normal'],
  ['permission-denied', 'Permission denied on Start'],
  ['needs-setup', 'Linux: one-time setup needed'],
  ['crash', 'Connection drops after a few seconds'],
  ['no-engine', 'Engine not running (all calls 503)'],
  ['no-server', 'No detour server (Start fails, list is empty)'],
];

async function toggleDevPanel() {
  if (devPanel) {
    devPanel.hidden = !devPanel.hidden;
    if (!devPanel.hidden) { syncDev(); devPanel.querySelector('input')?.focus(); }
    return;
  }
  const radios = SCENARIOS.map(([id, label]) => h('label', { class: 'dev-opt' },
    h('input', { type: 'radio', name: 'dev-scenario', value: id, onchange: () => setMock({ scenario: id }) }), label));
  const flag = (key, label) => h('label', { class: 'dev-opt' },
    h('input', { type: 'checkbox', dataset: { key }, onchange: (e) => setMock({ [key]: e.target.checked }) }), label);
  devPanel = h('aside', { class: 'devpanel', 'aria-label': 'Mock controls' },
    h('div', { class: 'row' }, h('strong', { class: 'grow', text: 'Mock controls' }),
      h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Close mock controls', text: '×', onclick: () => { devPanel.hidden = true; } })),
    h('fieldset', null, h('legend', { text: 'Scenario' }), radios),
    h('fieldset', null, h('legend', { text: 'Data' }),
      flag('warnings', 'Warnings'), flag('extras', 'Proxy and blocked-by-you sites'), flag('learning', 'Still learning (no network yet)'),
      flag('reloadFails', 'Switching server while On fails')),
    h('div', { class: 'row gap wrap' },
      h('button', { class: 'btn small', type: 'button', text: 'Change network', onclick: () => setMock({ network: 'next' }) }),
      h('button', { class: 'btn small', type: 'button', text: 'Reset data', onclick: () => setMock({ reset: true }) })),
    h('p', { class: 'muted small', text: 'Dev only. Ctrl+Shift+D toggles this panel.' }));
  document.body.append(devPanel);
  await syncDev();
  devPanel.querySelector('input')?.focus();
}

async function syncDev() {
  try {
    const { mock } = await api.getMock();
    for (const r of devPanel.querySelectorAll('input[name=dev-scenario]')) r.checked = r.value === mock.scenario;
    for (const c of devPanel.querySelectorAll('input[type=checkbox]')) c.checked = !!mock[c.dataset.key];
  } catch (e) { toast(e.message); }
}

async function setMock(opts) {
  try {
    await api.setMock(opts);
    await syncDev();
    // Leaving or entering the no-engine scenario: re-read everything.
    if (await refresh()) startLive();
  } catch (e) { toast(e.message); }
}

// ---------- start ----------

window.addEventListener('hashchange', route);
navStatus.textContent = 'Connecting to the engine…';
route();
refresh().then(() => startLive());
