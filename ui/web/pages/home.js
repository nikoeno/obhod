// Home: the one-button screen (docs/UI.md §4, §5.1).
import { h, svg, replace, fmtBytes, fmtAgo, pct, loadSetting, saveSetting, toast } from '../dom.js';
import { S, toggleConnection, checkNow, restartEngine, routeCount, routeTraffic, activateServer, activeServer } from '../store.js';
import { ROUTES, MAIN, EXTRA, errorInfo, networkLabel, FIRST_RUN, SERVER_TEXT, serverErrorText } from '../words.js';
import { showAddServerDialog } from '../serverui.js';
import { go, showSetupDialog, showLogDialog, keepFocus } from '../shell.js';

function powerIcon() {
  return svg('svg', { class: 'power', viewBox: '0 0 24 24', 'aria-hidden': 'true', focusable: 'false' },
    svg('path', { d: 'M12 3v8', fill: 'none', stroke: 'currentColor', 'stroke-width': '2.2', 'stroke-linecap': 'round' }),
    svg('path', { d: 'M6.3 7.2a8 8 0 1 0 11.4 0', fill: 'none', stroke: 'currentColor', 'stroke-width': '2.2', 'stroke-linecap': 'round' }));
}

/** Button look per state (§4). */
function buttonModel() {
  const err = errorInfo(S.error, S.state);
  if (S.engineDown) return { cls: 'off', label: '–', name: 'Start, unavailable: the engine is not running', disabled: true, sub: 'The engine is not running.' };
  switch (S.state) {
    case 'connecting': return { cls: 'busy', label: 'Starting…', name: 'Starting, please wait', disabled: true, sub: 'Asking for permission and starting.' };
    case 'connected': return { cls: 'on', label: 'Stop', name: 'Stop, currently on', disabled: false, sub: `On. ${pct(S.snapshot && S.snapshot.Savings)} of your traffic stays off the detour.` };
    case 'disconnecting': return { cls: 'busy dim', label: 'Stopping…', name: 'Stopping, please wait', disabled: true, sub: '' };
    default:
      if (err) return { cls: 'err', label: 'Try again', name: `Try again, currently off: ${err.sentence}`, disabled: false, sub: err.sentence, err };
      return { cls: '', label: 'Start', name: 'Start, currently off', disabled: false, sub: 'Off. Your traffic goes as usual.' };
  }
}

function makeCard(r, small) {
  const meta = ROUTES[r];
  const count = h('strong');
  const traffic = h('span', { class: 'muted' });
  const el = h('button', { class: ['card path', small && 'small'], type: 'button', onclick: () => go('sites', { route: r }) },
    h('span', { class: 'card-head' }, h('span', { class: 'dot ' + meta.dot, 'aria-hidden': 'true' }), h('span', { class: 'card-title', text: meta.long })),
    small ? null : h('span', { class: 'card-line muted', text: meta.line }),
    h('span', { class: 'card-nums' }, count, traffic));
  return {
    el,
    update() {
      const n = routeCount(r);
      const countText = `${n} site${n === 1 ? '' : 's'}`;
      const trafficText = r === 'block' ? 'never opened' : `${fmtBytes(routeTraffic(r))} today`;
      count.textContent = countText;
      traffic.textContent = trafficText;
      el.setAttribute('aria-label', `${meta.long}: ${countText}, ${trafficText}. Show in Sites`);
      if (small) el.hidden = n === 0;
    },
  };
}

function fan() {
  const p = (x) => `M300 0 C300 34, ${x} 26, ${x} 60`;
  return svg('svg', { class: 'fan', viewBox: '0 0 600 60', preserveAspectRatio: 'none', 'aria-hidden': 'true', focusable: 'false' },
    ...[100, 300, 500].map((x) => svg('path', { d: p(x), fill: 'none', 'vector-effect': 'non-scaling-stroke' })));
}

async function useServer(x) {
  try {
    const r = await activateServer(x.id);
    toast(r.reloaded ? SERVER_TEXT.switching(x.name) : SERVER_TEXT.nowActive(x.name));
  } catch (e) { toast(serverErrorText(e, 'activate')); }
}

export function create() {
  const banner = h('div', { class: 'banner-slot' });
  const setup = h('div', { class: 'setup-slot' });
  const big = h('button', { class: 'big', type: 'button', onclick: toggleConnection }, powerIcon(), h('span', { class: 'big-label' }));
  const sub = h('p', { class: 'subtitle', 'aria-live': 'polite' });
  const subAction = h('div', { class: 'sub-action' });
  const cardObjs = [...MAIN.map((r) => makeCard(r, false)), ...EXTRA.map((r) => makeCard(r, true))];
  const cards = h('div', { class: 'cards' }, cardObjs.slice(0, 3).map((c) => c.el));
  const extras = h('div', { class: 'cards extras' }, cardObjs.slice(3).map((c) => c.el));
  const savings = h('p', { class: 'savings' });
  const fanEl = fan();
  const netLine = h('div', { class: 'netline' });

  const el = h('section', { class: 'page home', 'aria-labelledby': 'home-h' },
    h('h1', { id: 'home-h', class: 'sr-only', text: 'Home' }),
    banner, setup,
    h('div', { class: 'hero' }, big, sub, subAction),
    fanEl, cards, extras, savings, netLine);

  function updateNumbers() {
    for (const c of cardObjs) c.update();
    extras.hidden = S.engineDown || EXTRA.every((r) => routeCount(r) === 0);
    cards.hidden = S.engineDown; // no stale numbers while the engine is gone
    fanEl.toggleAttribute('hidden', S.engineDown);
    const snap = S.snapshot;
    const total = snap ? Object.values(snap.Traffic || {}).reduce((a, b) => a + b, 0) : 0;
    savings.textContent = snap && total > 0 ? `${pct(snap.Savings)} of your traffic stays off the detour` : '';
    savings.hidden = S.engineDown || !savings.textContent;
    if (S.state === 'connected' && !S.engineDown) sub.textContent = buttonModel().sub;
  }

  function update() {
    const m = buttonModel();
    big.className = 'big ' + m.cls;
    big.disabled = m.disabled;
    big.setAttribute('aria-label', m.name);
    big.querySelector('.big-label').textContent = m.label;
    sub.textContent = m.sub;

    // One suggested action under the subtitle for errors and the no-engine state.
    keepFocus(subAction, () => {
    replace(subAction);
    if (S.engineDown) subAction.append(h('button', { class: 'btn primary', type: 'button', text: 'Restart engine', onclick: restartEngine }));
    else if (m.err) {
      const a = m.err.action;
      if (a === 'setup') subAction.append(h('button', { class: 'btn', type: 'button', text: 'Show setup commands', onclick: () => showSetupDialog(S.error) }));
      else if (a === 'log') subAction.append(h('button', { class: 'btn', type: 'button', text: 'Show engine log', onclick: () => showLogDialog() }));
      else if (a === 'detour') subAction.append(h('button', { class: 'btn', type: 'button', text: 'Open Detour server settings', onclick: () => go('settings', { tab: 'detour' }) }));
      else if (a === 'restart-engine') subAction.append(h('button', { class: 'btn', type: 'button', text: 'Restart engine', onclick: restartEngine }));
    }
    });

    // Warning banner: first warning + Details.
    const warns = (S.snapshot && S.snapshot.Warnings) || [];
    keepFocus(banner, () => {
    replace(banner);
    // A server switch that could not be applied while On: the old server is still in use.
    const failed = S.state === 'connected' && !S.engineDown ? errorInfo(S.error, S.state) : null;
    if (failed) {
      banner.append(h('div', { class: 'banner', role: 'alert' },
        h('span', { class: 'banner-icon', 'aria-hidden': 'true', text: '!' }),
        h('span', { class: 'grow', text: failed.sentence }),
        h('button', { class: 'link', type: 'button', text: 'Open Detour server settings', onclick: () => go('settings', { tab: 'detour' }) })));
    }
    if (warns.length && !S.engineDown) {
      banner.append(h('div', { class: 'banner', role: 'status' },
        h('span', { class: 'banner-icon', 'aria-hidden': 'true', text: '!' }),
        h('span', { class: 'grow', text: warns[0] + (warns.length > 1 ? ` (+${warns.length - 1} more)` : '') }),
        h('button', { class: 'link', type: 'button', text: 'Details', onclick: () => go('activity', { tab: 'warnings' }) })));
    }
    });

    // First run and missing detour server (docs/UI.md §6): a card to add one, or a reminder after "Skip for now".
    keepFocus(setup, () => {
      replace(setup);
      if (S.engineDown || !S.serversLoaded || activeServer()) return;
      if (S.servers.length) {
        const only = S.servers.length === 1 ? S.servers[0] : null;
        setup.append(h('section', { class: 'setup-card', 'aria-labelledby': 'setup-h' },
          h('h2', { id: 'setup-h', text: FIRST_RUN.pickTitle }),
          h('p', { class: 'muted', text: FIRST_RUN.pickText }),
          h('div', { class: 'row gap wrap' }, only
            ? h('button', { class: 'btn primary', type: 'button', text: `${SERVER_TEXT.makeActive}: ${only.name}`, onclick: () => useServer(only) })
            : h('button', { class: 'btn primary', type: 'button', text: 'Choose in Settings', onclick: () => go('settings', { tab: 'detour' }) }))));
      } else if (!loadSetting('detourSkipped', false)) {
        setup.append(h('section', { class: 'setup-card', 'aria-labelledby': 'setup-h' },
          h('h2', { id: 'setup-h', text: FIRST_RUN.title }),
          h('p', { class: 'muted', text: FIRST_RUN.text }),
          h('div', { class: 'row gap wrap' },
            h('button', { class: 'btn primary', type: 'button', text: SERVER_TEXT.add, onclick: () => showAddServerDialog() }),
            h('button', { class: 'btn', type: 'button', text: FIRST_RUN.skip, onclick: () => { saveSetting('detourSkipped', true); update(); } }))));
      } else {
        setup.append(h('div', { class: 'banner', role: 'status' },
          h('span', { class: 'banner-icon', 'aria-hidden': 'true', text: '!' }),
          h('span', { class: 'grow', text: FIRST_RUN.skippedBanner }),
          h('button', { class: 'link', type: 'button', text: FIRST_RUN.skippedAction, onclick: () => go('settings', { tab: 'detour' }) })));
      }
    });

    updateNumbers();
    const snap = S.snapshot;

    keepFocus(netLine, () => {
    replace(netLine);
    if (snap && !S.engineDown) {
      const label = networkLabel(snap.Network);
      const checked = S.checking ? 'Checking…' : `checked ${fmtAgo(snap.LastCheck)}`;
      netLine.append(
        h('button', { class: 'link plain', type: 'button', 'aria-label': `Network: ${label}, ${checked}. Show network details`, onclick: () => go('activity', { tab: 'network' }) },
          h('span', { text: `Network: ${label}` }), h('span', { class: 'muted', text: ` · ${checked}` })),
        h('button', { class: 'btn small', type: 'button', text: S.checking ? 'Checking…' : 'Check again', disabled: S.checking, onclick: () => checkNow() }));
    }
    });
  }

  update();
  const tick = setInterval(update, 30000); // keeps "checked N min ago" fresh
  return {
    el,
    update: (topic) => { if (topic === 'traffic') updateNumbers(); else if (topic !== 'changes') update(); },
    destroy: () => clearInterval(tick),
  };
}
