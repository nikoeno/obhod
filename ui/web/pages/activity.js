// Activity: what happened recently (docs/UI.md §5.3).
import { h, svg, replace, tabs, fmtBytes, fmtAgo, fmtIn, fmtTime } from '../dom.js';
import { S, routeTraffic } from '../store.js';
import { ROUTES, routeWord, explainWarning, networkLabel } from '../words.js';
import { go, keepFocus } from '../shell.js';

const TABS = [
  { id: 'changes', label: 'Changes' },
  { id: 'traffic', label: 'Traffic' },
  { id: 'network', label: 'Network' },
  { id: 'warnings', label: 'Warnings' },
];
const CHART_ROUTES = ['direct', 'tunnel', 'desync', 'proxy'];

let clearedAt = 0; // "Clear view" hides older changes on screen only (kept for this window session)

export function create(params) {
  let tab = TABS.some((t) => t.id === params.tab) ? params.tab : 'changes';
  const tabSlot = h('div');
  const panel = h('div', { role: 'tabpanel', class: 'tabpanel', tabIndex: -1 });
  const el = h('section', { class: 'page activity', 'aria-labelledby': 'act-h' },
    h('h1', { id: 'act-h', text: 'Activity' }), tabSlot, panel);

  function renderTabs() {
    const items = TABS.map((t) => (t.id === 'warnings' ? { ...t, count: ((S.snapshot && S.snapshot.Warnings) || []).length } : t));
    keepFocus(tabSlot, () => replace(tabSlot, tabs(items, tab, (id) => { tab = id; renderTabs(); render(); }, 'Activity views')));
    panel.setAttribute('aria-labelledby', 'tab-' + tab);
  }

  function render() {
    keepFocus(panel, () => replace(panel, ...({ changes, traffic, network, warnings })[tab]()));
  }

  function changes() {
    const recent = ((S.snapshot && S.snapshot.Recent) || []).filter((c) => Date.parse(c.At) > clearedAt).slice().reverse();
    const head = h('div', { class: 'row' }, h('p', { class: 'muted grow', text: 'Route changes the app made. Click one to open that site.' }),
      h('button', { class: 'btn small', type: 'button', text: 'Clear view', disabled: !recent.length, onclick: () => { clearedAt = Date.now(); render(); } }));
    if (!recent.length) return [head, h('p', { class: 'empty', text: 'No changes to show.' })];
    return [head, h('ol', { class: 'timeline' }, recent.map((c) => h('li', null,
      h('button', { class: 'link plain', type: 'button', onclick: () => go('sites', { domain: c.Domain, q: c.Domain }) },
        h('strong', { text: c.Domain }), ` moved from ${routeWord(c.From)} to ${routeWord(c.To)}`,
        h('span', { class: 'muted', text: ` · ${fmtTime(c.At)}` })))))];
  }

  function traffic() {
    const on = S.state === 'connected';
    const t = S.traffic;
    const stats = h('div', { class: 'stats' },
      stat('Downloaded', fmtBytes(t.downloadTotal)), stat('Uploaded', fmtBytes(t.uploadTotal)), stat('Connections', String(t.connections)));
    const note = on ? null : h('p', { class: 'muted', text: 'Live numbers appear while the app is on. Totals below are for today.' });
    const visible = CHART_ROUTES.filter((r) => r !== 'proxy' || routeTraffic(r) > 0);
    const max = Math.max(1, ...visible.flatMap((r) => S.series[r]));
    const charts = h('div', { class: 'charts' }, visible.map((r) => {
      const meta = ROUTES[r];
      const series = S.series[r];
      return h('figure', { class: 'chart' },
        h('figcaption', null, h('span', { class: 'dot ' + meta.dot, 'aria-hidden': 'true' }), h('strong', { text: meta.long }),
          h('span', { class: 'muted', text: ` · ${fmtBytes(routeTraffic(r))} today` })),
        bars(series, max, `${meta.long}: traffic per 2 seconds this session, last ${series.length} samples, latest ${fmtBytes(series[series.length - 1] || 0)}`, meta.dot));
    }));
    return [stats, note, charts];
  }

  function network() {
    const s = S.snapshot || {};
    const nets = S.networks.slice().reverse();
    return [
      h('dl', { class: 'facts wide' },
        h('dt', { text: 'Network' }), h('dd', { text: networkLabel(s.Network) }),
        h('dt', { text: 'Last check' }), h('dd', { text: S.checking ? 'Checking…' : `${fmtAgo(s.LastCheck)} (${fmtTime(s.LastCheck)})` }),
        h('dt', { text: 'Next check' }), h('dd', { text: `${fmtIn(s.NextCheck)} (${fmtTime(s.NextCheck)})` })),
      h('h2', { class: 'h3', text: 'Networks seen this session' }),
      nets.length ? h('ul', { class: 'history' }, nets.map((n) => h('li', null, networkLabel(n.label), h('span', { class: 'muted', text: ` · since ${fmtTime(n.since)}` }))))
        : h('p', { class: 'muted', text: 'None yet.' }),
      h('p', { class: 'muted small', text: 'Networks are shown by name only. The app never shows or stores your public address.' }),
    ];
  }

  function warnings() {
    const list = (S.snapshot && S.snapshot.Warnings) || [];
    if (!list.length) return [h('p', { class: 'empty', text: 'No warnings. Everything looks fine.' })];
    return [h('ul', { class: 'warnlist' }, list.map((w) => {
      const x = explainWarning(w);
      return h('li', { class: 'card' }, h('strong', { text: w }), h('p', { class: 'muted', text: x.why }), h('p', null, h('span', { class: 'label', text: 'What to do: ' }), x.todo));
    }))];
  }

  renderTabs();
  render();
  return {
    el,
    update(topic) {
      if (topic === 'traffic' && tab !== 'traffic') return;
      if (topic === 'state') renderTabs();
      render();
    },
  };
}

function stat(label, value) {
  return h('div', { class: 'stat card' }, h('span', { class: 'muted small', text: label }), h('strong', { class: 'big-num', text: value }));
}

function bars(series, max, label, dotClass) {
  const N = 30;
  const W = 300;
  const H = 60;
  const bw = W / N;
  const pad = N - series.length;
  return svg('svg', { class: 'bars ' + dotClass, viewBox: `0 0 ${W} ${H}`, role: 'img', 'aria-label': label, preserveAspectRatio: 'none' },
    svg('line', { x1: 0, y1: H - 0.5, x2: W, y2: H - 0.5, class: 'axis' }),
    ...series.map((v, i) => {
      const bh = v > 0 ? Math.max(2, (v / max) * (H - 4)) : 0;
      return svg('rect', { x: ((pad + i) * bw + 1).toFixed(1), y: (H - bh).toFixed(1), width: (bw - 2).toFixed(1), height: bh.toFixed(1), rx: 1.5 });
    }));
}
