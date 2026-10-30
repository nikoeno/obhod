// Sites: what the app decided for each site (docs/UI.md §5.2). Read-only in v1.
import { h, replace, tabs, fmtBytes, fmtAgo, fmtTime } from '../dom.js';
import { S, checkNow, routeCount } from '../store.js';
import { ROUTES, MAIN, EXTRA } from '../words.js';
import { keepFocus } from '../shell.js';

export function create(params) {
  let route = ROUTES[params.route] ? params.route : 'all';
  let query = params.q || '';
  let selected = params.domain || null;

  const tabSlot = h('div');
  const search = h('input', {
    type: 'search', id: 'site-search', class: 'search', placeholder: 'Search sites', 'aria-label': 'Search sites',
    autocomplete: 'off', spellcheck: 'false',
    oninput: () => { query = search.value; renderList(); },
  });
  search.value = query;
  const checkAll = h('button', { class: 'btn', type: 'button', text: 'Check all again', onclick: () => checkNow() });
  const toolbar = h('div', { class: 'toolbar' }, search, h('span', { class: 'grow' }), checkAll,
    h('button', { class: 'btn', type: 'button', disabled: true, 'aria-describedby': 'add-later', text: 'Add a site' }),
    h('span', { id: 'add-later', class: 'later', text: 'later' }));
  const status = h('p', { class: 'muted small', 'aria-live': 'polite' });
  const listSlot = h('div', { class: 'list-slot' });
  const panel = h('aside', { class: 'sidepanel', 'aria-label': 'Site details', hidden: true, tabIndex: -1 });

  const el = h('section', { class: 'page sites', 'aria-labelledby': 'sites-h' },
    h('h1', { id: 'sites-h', text: 'Sites' }),
    h('p', { class: 'muted lead', text: 'What the app decided for each site. It updates by itself.' }),
    tabSlot, toolbar, status,
    h('div', { class: 'split' }, listSlot, panel));

  function tabItems() {
    const items = [{ id: 'all', label: 'All', count: S.routes.length }];
    for (const r of MAIN) items.push({ id: r, label: ROUTES[r].word, dot: ROUTES[r].dot, count: routeCount(r) });
    for (const r of EXTRA) if (routeCount(r) > 0) items.push({ id: r, label: ROUTES[r].word, dot: ROUTES[r].dot, count: routeCount(r) });
    return items;
  }

  function renderTabs() {
    keepFocus(tabSlot, () => replace(tabSlot, tabs(tabItems(), route, (id) => { route = id; renderTabs(); renderList(); }, 'Filter by path')));
  }

  function rows() {
    const q = query.trim().toLowerCase();
    return S.routes.filter((r) => (route === 'all' || r.route === route) && (!q || r.domain.includes(q)));
  }

  function renderList() {
    checkAll.disabled = S.checking || S.engineDown;
    checkAll.textContent = S.checking && !S.checkingDomains ? 'Checking…' : 'Check all again';
    const learning = S.snapshot && !S.snapshot.Network;
    if (S.engineDown) { status.textContent = ''; replace(listSlot, h('p', { class: 'empty', text: 'The engine is not running.' })); return; }
    if (learning || (S.loaded && S.routes.length === 0)) {
      status.textContent = '';
      replace(listSlot, h('p', { class: 'empty', text: 'Still learning this network. Sites will appear in a few seconds.' }));
      return;
    }
    const list = rows();
    status.textContent = `${list.length} site${list.length === 1 ? '' : 's'}${query ? ` matching “${query}”` : ''}`;
    if (!list.length) { replace(listSlot, h('p', { class: 'empty', text: query ? `No sites match “${query}”.` : 'No sites on this path yet.' })); return; }
    const table = h('table', { class: 'sites-table' },
      h('caption', { class: 'sr-only', text: 'Sites and the path each one takes' }),
      h('thead', null, h('tr', null,
        h('th', { scope: 'col', text: 'Site' }), h('th', { scope: 'col', text: 'Path' }),
        h('th', { scope: 'col', text: 'Since' }), h('th', { scope: 'col', class: 'num', text: 'Today' }))),
      h('tbody', null, list.map((r) => h('tr', { class: r.domain === selected ? 'sel' : null },
        h('td', null, h('button', {
          class: 'link plain site', type: 'button', text: r.domain, 'aria-expanded': String(r.domain === selected),
          onclick: () => { selected = r.domain; renderList(); renderPanel(true); },
        })),
        h('td', null, h('span', { class: 'dot ' + ROUTES[r.route].dot, 'aria-hidden': 'true' }), ROUTES[r.route].word),
        h('td', { class: 'muted', text: fmtAgo(r.since) }),
        h('td', { class: 'num', text: r.route === 'block' ? '–' : fmtBytes(r.trafficToday) })))));
    keepFocus(listSlot, () => replace(listSlot, table));
  }

  function renderPanel(focus) {
    const r = selected && S.routes.find((x) => x.domain === selected);
    if (!r) { panel.hidden = true; return; }
    panel.hidden = false;
    const meta = ROUTES[r.route];
    const checkingThis = S.checking && S.checkingDomains && S.checkingDomains.includes(r.domain);
    const history = (r.history || []).slice().reverse();
    keepFocus(panel, () => replace(panel,
      h('div', { class: 'row' }, h('h2', { class: 'grow', text: r.domain }),
        h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Close details', text: '×', onclick: close })),
      h('dl', { class: 'facts' },
        h('dt', { text: 'Path' }), h('dd', null, h('span', { class: 'dot ' + meta.dot, 'aria-hidden': 'true' }), `${meta.long} `, h('code', { class: 'tech', text: r.route })),
        h('dt', { text: 'Since' }), h('dd', { text: `${fmtAgo(r.since)} (${fmtTime(r.since)})` }),
        h('dt', { text: 'Today' }), h('dd', { text: r.route === 'block' ? 'never opened' : fmtBytes(r.trafficToday) }),
        h('dt', { text: 'Last check' }), h('dd', { text: fmtAgo(r.lastCheck) })),
      h('h3', { text: 'History' }),
      history.length
        ? h('ul', { class: 'history' }, history.map((c) => h('li', null,
          `${ROUTES[c.From] ? ROUTES[c.From].word : c.From} → ${ROUTES[c.To] ? ROUTES[c.To].word : c.To} `,
          h('span', { class: 'muted', text: `· ${fmtTime(c.At)}` }), ' ', h('code', { class: 'tech', text: `${c.From} → ${c.To}` }))))
        : h('p', { class: 'muted small', text: 'No changes this session.' }),
      h('div', { class: 'stack' },
        h('button', { class: 'btn primary', type: 'button', text: checkingThis ? 'Checking…' : 'Check this site again', disabled: S.checking || S.engineDown, onclick: () => checkNow([r.domain]) }),
        h('button', { class: 'btn', type: 'button', disabled: true, 'aria-describedby': 'always-later', text: 'Always use…' }),
        h('span', { id: 'always-later', class: 'later', text: 'later: choosing a path per site is not available yet' }))));
    if (focus) panel.focus();
  }

  function close() {
    const was = selected;
    selected = null;
    panel.hidden = true;
    renderList();
    const btn = [...listSlot.querySelectorAll('button.site')].find((b) => b.textContent === was);
    if (btn) btn.focus();
  }

  renderTabs();
  renderList();
  renderPanel(false);

  return {
    el,
    update(topic) {
      if (topic === 'traffic' || topic === 'changes') return;
      renderTabs(); renderList(); renderPanel(false);
    },
    escape() { if (selected) { close(); return true; } return false; },
    focusSearch() { search.focus(); search.select(); },
  };
}

