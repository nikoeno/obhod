// Settings (docs/UI.md §5.4). In this MVP every setting lives in localStorage only.
import * as api from '../api.js';
import { UI_VERSION } from '../api.js';
import { h, replace, tabs, toast, toggleRow, loadSetting, saveSetting } from '../dom.js';
import { S, restartConnection, restartEngine, quit, activateServer } from '../store.js';
import { SERVER_TEXT, MAX_SERVERS, protocolName, serverErrorText } from '../words.js';
import { showAddServerDialog, confirmRemoveServer } from '../serverui.js';
import { keepFocus } from '../shell.js';

const TABS = [
  { id: 'general', label: 'General' },
  { id: 'detour', label: 'Detour server' },
  { id: 'advanced', label: 'Advanced' },
  { id: 'about', label: 'About' },
];
const NOT_IN_MOCK = 'Not available in the mock';

let restartNeeded = false;
const revealed = new Set(); // server ids whose address is shown (addresses stay hidden by default)

export function create(params) {
  let pendingId = null;
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
    const list = S.servers;
    const active = list.find((x) => x.active);
    const locked = S.engineDown || S.state === 'connecting' || S.state === 'disconnecting';
    const full = list.length >= MAX_SERVERS;
    const out = [
      h('div', { class: 'row setting' },
        h('div', { class: 'grow' }, h('strong', { text: 'Detour server' }),
          h('p', { class: 'muted small', text: SERVER_TEXT.lead })),
        S.serversLoaded ? h('span', { class: ['pill', active ? 'ok' : 'warn'], text: active ? SERVER_TEXT.ready : SERVER_TEXT.missing }) : null),
    ];
    if (!S.serversLoaded) {
      out.push(h('p', { class: 'empty', text: S.engineDown ? 'The engine is not running.' : 'Loading…' }));
    } else if (!list.length) {
      out.push(h('p', { class: 'empty', text: SERVER_TEXT.none }));
    } else {
      out.push(h('ul', { class: 'servers', 'aria-label': 'Detour servers' }, list.map((x) => serverRow(x, locked))));
    }
    out.push(h('div', { class: 'row gap wrap' },
      h('button', { class: 'btn primary', id: 'srv-add', type: 'button', text: SERVER_TEXT.add, disabled: locked || full || !S.serversLoaded, 'aria-describedby': 'srv-note',
        onclick: () => showAddServerDialog((s2) => focusNew(s2)) }),
      h('span', { class: 'muted small', text: SERVER_TEXT.count(list.length) })));
    const note = full ? SERVER_TEXT.listFull
      : locked && !S.engineDown ? SERVER_TEXT.busyHint
        : S.state === 'connected' && list.length > 1 ? SERVER_TEXT.switchHint : '';
    out.push(h('p', { id: 'srv-note', class: 'muted small', 'aria-live': 'polite', text: note }));
    out.push(h('div', { class: 'row setting' },
      h('div', { class: 'grow' }, h('strong', { text: SERVER_TEXT.proxyTitle }),
        h('p', { class: 'muted small', text: SERVER_TEXT.proxyHint })),
      h('span', { class: 'later', text: 'later' })));
    return out;
  }

  function serverRow(x, locked) {
    const shown = revealed.has(x.id);
    const hostText = x.host.includes(':') && !x.host.startsWith('[') ? `[${x.host}]` : x.host;
    const pending = pendingId === x.id;
    return h('li', { class: 'server' },
      h('div', { class: 'server-main' },
        h('div', { class: 'server-name' }, h('span', { text: x.name }), x.active ? h('span', { class: 'pill ok', text: SERVER_TEXT.inUse }) : null),
        h('div', { class: 'muted small' },
          protocolName(x.protocol), ' · ',
          shown ? h('span', { class: 'tech', text: `${hostText}:${x.port}` }) : SERVER_TEXT.hidden)),
      h('div', { class: 'server-actions' },
        h('button', {
          class: 'link', type: 'button', text: shown ? SERVER_TEXT.hideAddress : SERVER_TEXT.showAddress,
          'aria-expanded': String(shown), 'aria-label': `${shown ? SERVER_TEXT.hideAddress : SERVER_TEXT.showAddress} of ${x.name}`,
          onclick: () => { if (shown) revealed.delete(x.id); else revealed.add(x.id); render(); },
        }),
        x.active ? null : h('button', {
          class: 'btn small', id: 'srv-use-' + x.id, type: 'button', text: pending ? 'Switching…' : SERVER_TEXT.makeActive,
          'aria-label': `${SERVER_TEXT.makeActive}: ${x.name}`, disabled: locked || pending, onclick: () => makeActive(x),
        }),
        h('button', {
          class: 'btn small danger', id: 'srv-rm-' + x.id, type: 'button', text: SERVER_TEXT.remove,
          'aria-label': `${SERVER_TEXT.remove}: ${x.name}`, disabled: locked,
          onclick: () => confirmRemoveServer(x, () => document.getElementById('srv-add')?.focus()),
        })));
  }

  async function makeActive(x) {
    pendingId = x.id;
    render();
    try {
      const r = await activateServer(x.id);
      toast(r.reloaded ? SERVER_TEXT.switching(x.name) : SERVER_TEXT.nowActive(x.name));
    } catch (e) {
      toast(serverErrorText(e, 'activate'));
    } finally {
      pendingId = null;
      render();
    }
  }

  function focusNew(s2) { document.getElementById('srv-use-' + s2.id)?.focus(); }

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
      h('dt', { text: 'App (UI)' }), h('dd', { text: UI_VERSION }),
      h('dt', { text: 'Engine (splitter)' }), h('dd', { text: v.splitter }),
      h('dt', { text: 'sing-box' }), h('dd', { text: v.singBox || 'not reported' }),
      h('dt', { text: 'Desync engine' }), h('dd', { text: v.desync || 'not reported' }),
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
  return {
    el,
    // The server list and the connection state change what the Detour server tab offers.
    update(topic) { if (tab === 'detour' && (topic === 'servers' || topic === 'state')) render(); },
  };
}
