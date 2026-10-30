// Settings (docs/UI.md §5.4). In this MVP every setting lives in localStorage only.
import * as api from '../api.js';
import { h, replace, tabs, toast, toggleRow, loadSetting, saveSetting } from '../dom.js';
import { S, restartConnection, restartEngine, quit } from '../store.js';
import { keepFocus } from '../shell.js';

const TABS = [
  { id: 'general', label: 'General' },
  { id: 'detour', label: 'Detour server' },
  { id: 'advanced', label: 'Advanced' },
  { id: 'about', label: 'About' },
];
const NOT_IN_MOCK = 'Not available in the mock';

let restartNeeded = false;

export function create(params) {
  let tab = TABS.some((t) => t.id === params.tab) ? params.tab : 'general';
  const tabSlot = h('div');
  const panel = h('div', { role: 'tabpanel', class: 'tabpanel', tabIndex: -1 });
  const el = h('section', { class: 'page settings', 'aria-labelledby': 'set-h' },
    h('h1', { id: 'set-h', text: 'Settings' }), tabSlot, panel,
    h('div', { class: 'quit-row' }, h('button', { class: 'btn danger', type: 'button', text: 'Quit Obhod', onclick: quit }),
      h('span', { class: 'muted small', text: 'Stops the engine and closes the app.' })));

  function renderTabs() {
    keepFocus(tabSlot, () => replace(tabSlot, tabs(TABS, tab, (id) => { tab = id; renderTabs(); render(); }, 'Settings sections')));
    panel.setAttribute('aria-labelledby', 'tab-' + tab);
  }
  function render() { keepFocus(panel, () => replace(panel, ...({ general, detour, advanced, about })[tab]())); }

  function setting(key, def) {
    return { checked: loadSetting(key, def), onChange: (v) => saveSetting(key, v) };
  }

  function general() {
    const lang = h('select', { id: 'set-lang', onchange: (e) => saveSetting('language', e.target.value) },
      h('option', { value: 'en', text: 'English' }), h('option', { value: 'ru', text: 'Русский (later)', disabled: true }));
    lang.value = loadSetting('language', 'en');
    return [
      toggleRow({ id: 'set-signin', label: 'Start the app when I sign in', ...setting('startOnSignIn', true) }),
      toggleRow({ id: 'set-autoconnect', label: 'Connect automatically when the app starts', ...setting('autoConnect', false) }),
      toggleRow({ id: 'set-tray', label: 'Close button hides to tray', hint: 'Quit from the tray menu or below.', ...setting('closeToTray', true) }),
      toggleRow({ id: 'set-notify', label: 'Show notifications', hint: 'Connected, stopped after an error, and when a detour is first needed on a network.', ...setting('notifications', true) }),
      h('div', { class: 'row setting' }, h('label', { class: 'grow', htmlFor: 'set-lang', text: 'Language' }), lang),
    ];
  }

  function detour() {
    const ready = loadSetting('detourReady', true);
    return [
      h('div', { class: 'row setting' },
        h('div', { class: 'grow' }, h('strong', { text: 'Detour server' }),
          h('p', { class: 'muted small', text: 'Needed to open Blocked sites. The file is kept private and never shown again.' })),
        h('span', { class: ['pill', ready ? 'ok' : 'warn'], text: ready ? 'Ready' : 'Missing' })),
      h('div', { class: 'row gap' },
        h('button', { class: 'btn', type: 'button', text: 'Import server file…', onclick: () => toast(NOT_IN_MOCK) }),
        h('button', { class: 'btn', type: 'button', text: 'Remove', disabled: !ready, onclick: () => { saveSetting('detourReady', false); toast('Removed (mock: nothing was deleted).'); render(); } })),
      h('div', { class: 'row setting' },
        h('div', { class: 'grow' }, h('strong', { text: 'Extra proxy (optional)' }),
          h('p', { class: 'muted small', text: 'Sites on the "Via proxy" path go through it.' }))),
      h('div', { class: 'row gap' }, h('button', { class: 'btn', type: 'button', text: 'Import proxy file…', onclick: () => toast(NOT_IN_MOCK) })),
    ];
  }

  function advanced() {
    const changed = (key) => (v) => { saveSetting(key, v); restartNeeded = true; render(); };
    const level = h('select', { id: 'set-loglevel', onchange: (e) => changed('logLevel')(e.target.value) },
      ...['warn', 'info', 'debug'].map((v) => h('option', { value: v, text: v })));
    level.value = loadSetting('logLevel', 'info');
    const banner = restartNeeded ? h('div', { class: 'banner', role: 'status' },
      h('span', { class: 'banner-icon', 'aria-hidden': 'true', text: '!' }),
      h('span', { class: 'grow', text: 'Restart needed. The engine reads these settings when it starts.' }),
      h('button', { class: 'btn primary small', type: 'button', text: 'Restart now', onclick: restartNow })) : null;
    return [
      banner,
      toggleRow({ id: 'set-quic', label: 'Block QUIC', hint: 'Sites fall back to regular HTTPS, which the app handles better.', checked: loadSetting('blockQuic', true), onChange: changed('blockQuic') }),
      toggleRow({ id: 'set-dnsq', label: 'Allow other DNS query types', hint: 'Technical: dns.allow_other_query_types. Off by default.', checked: loadSetting('allowOtherQueryTypes', false), onChange: changed('allowOtherQueryTypes') }),
      h('div', { class: 'row setting' }, h('label', { class: 'grow', htmlFor: 'set-loglevel', text: 'Log level' }), level),
      h('div', { class: 'row setting' },
        h('div', { class: 'grow' }, h('strong', { text: 'Restart engine' }), h('p', { class: 'muted small', text: 'Starts the background engine again.' })),
        h('button', { class: 'btn', type: 'button', text: 'Restart engine', onclick: restartEngine })),
    ];
  }

  async function restartNow() {
    try {
      if (S.state === 'connected') {
        toast('Restarting…');
        await restartConnection();
      } else {
        toast('Saved. It applies the next time you press Start.');
      }
      restartNeeded = false;
      render();
    } catch (e) { toast(e.message); }
  }

  function about() {
    const ver = h('dl', { class: 'facts wide' }, h('dt', { text: 'Loading…' }));
    api.getVersion().then((v) => replace(ver,
      h('dt', { text: 'App (UI)' }), h('dd', { text: v.ui }),
      h('dt', { text: 'Engine (splitter)' }), h('dd', { text: v.splitter }),
      h('dt', { text: 'sing-box' }), h('dd', { text: v.singBox }),
      h('dt', { text: 'Desync engine' }), h('dd', { text: v.desync }),
    )).catch((e) => replace(ver, h('dt', { text: 'Version' }), h('dd', { text: e.message })));
    return [
      ver,
      h('h2', { class: 'h3', text: 'Bundled programs' }),
      h('ul', { class: 'plainlist' },
        h('li', { text: 'sing-box: GPL-3.0-or-later' }),
        h('li', { text: 'zapret2 (desync engine): MIT' }),
        h('li', { text: 'WinDivert (Windows): LGPL-3.0 / GPL-2.0' }),
        h('li', { text: 'cygwin1.dll (Windows): LGPL' })),
      h('button', { class: 'btn', type: 'button', text: 'Check for updates', onclick: () => toast('Would open the release page in your browser. Not available in the mock.') }),
      h('p', { class: 'muted small', text: 'The app makes no automatic network calls to check for updates.' }),
    ];
  }

  renderTabs();
  render();
  return { el, update() { /* settings do not depend on live data */ } };
}
