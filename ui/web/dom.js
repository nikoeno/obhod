// Small DOM helpers. Data always goes in via textContent / attributes, never innerHTML.

const PROPS = new Set(['value', 'checked', 'disabled', 'hidden', 'selected', 'tabIndex', 'type', 'id', 'title', 'htmlFor', 'name', 'placeholder']);

function applyProps(el, props) {
  for (const [k, v] of Object.entries(props || {})) {
    if (v === undefined || v === null || v === false && !PROPS.has(k)) continue;
    if (k === 'class') el.setAttribute('class', Array.isArray(v) ? v.filter(Boolean).join(' ') : v);
    else if (k === 'text') el.textContent = String(v);
    else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2).toLowerCase(), v);
    else if (k === 'dataset') Object.assign(el.dataset, v);
    else if (PROPS.has(k) && !(el instanceof SVGElement)) el[k] = v;
    else el.setAttribute(k, v === true ? '' : String(v));
  }
}

function append(el, children) {
  for (const c of children.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
}

export function h(tag, props, ...children) {
  const el = document.createElement(tag);
  applyProps(el, props);
  append(el, children);
  return el;
}

const NS = 'http://www.w3.org/2000/svg';
export function svg(tag, props, ...children) {
  const el = document.createElementNS(NS, tag);
  applyProps(el, props);
  append(el, children);
  return el;
}

export function clear(el) { while (el.firstChild) el.firstChild.remove(); return el; }
export function replace(el, ...children) { clear(el); append(el, children); return el; }

// ---------- formatting ----------

export function fmtBytes(n) {
  if (!Number.isFinite(n) || n <= 0) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n >= 100 || i === 0 ? Math.round(n) : n.toFixed(1)} ${u[i]}`;
}

export function fmtAgo(iso) {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return 'never';
  const s = Math.round((Date.now() - t) / 1000);
  if (s < 0) return fmtIn(iso);
  if (s < 45) return 'just now';
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const hh = Math.round(m / 60);
  if (hh < 24) return `${hh} h ago`;
  return `${Math.round(hh / 24)} d ago`;
}

export function fmtIn(iso) {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return '–';
  const m = Math.round((t - Date.now()) / 60000);
  if (m <= 0) return 'now';
  if (m < 60) return `in ${m} min`;
  return `in ${Math.round(m / 60)} h`;
}

export function fmtTime(iso) {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return '–';
  return t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

export function pct(x) { return `${Math.round((x || 0) * 100)}%`; }

// ---------- toast ----------

let toastHost;
export function toast(msg) {
  if (!toastHost) {
    toastHost = h('div', { class: 'toasts', role: 'status', 'aria-live': 'polite' });
    document.body.append(toastHost);
  }
  const t = h('div', { class: 'toast', text: msg });
  toastHost.append(t);
  setTimeout(() => t.classList.add('out'), 2800);
  setTimeout(() => t.remove(), 3200);
}

// ---------- dialog ----------

export function openDialog({ title, body, actions = [], label }) {
  const titleId = 'dlg-' + Math.random().toString(36).slice(2);
  const dlg = h('dialog', { class: 'dialog', 'aria-labelledby': titleId, 'aria-label': label });
  const close = () => { dlg.close(); };
  dlg.addEventListener('close', () => dlg.remove());
  const btns = actions.map((a) => h('button', {
    class: a.primary ? 'btn primary' : 'btn',
    text: a.label,
    onclick: async () => { const keep = a.onClick ? await a.onClick() : false; if (!keep) close(); },
  }));
  if (!actions.length) btns.push(h('button', { class: 'btn primary', text: 'Close', onclick: close }));
  dlg.append(
    h('h2', { id: titleId, text: title }),
    h('div', { class: 'dialog-body' }, body),
    h('div', { class: 'dialog-actions' }, btns),
  );
  document.body.append(dlg);
  dlg.showModal();
  return { close, el: dlg };
}

// ---------- clipboard ----------

export async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const ta = h('textarea', { class: 'offscreen', 'aria-hidden': 'true' });
    ta.value = text;
    document.body.append(ta);
    ta.select();
    let ok = false;
    try { ok = document.execCommand('copy'); } catch { ok = false; }
    ta.remove();
    return ok;
  }
}

// ---------- storage (per-viewer settings only) ----------

export function loadSetting(key, fallback) {
  try {
    const v = localStorage.getItem('obhod.' + key);
    return v === null ? fallback : JSON.parse(v);
  } catch { return fallback; }
}
export function saveSetting(key, value) {
  try { localStorage.setItem('obhod.' + key, JSON.stringify(value)); } catch { /* storage unavailable */ }
}

// ---------- tabs ----------

/** Accessible tab strip. items: [{id, label, count?}]. Arrow keys move between tabs. */
export function tabs(items, active, onSelect, label) {
  const list = h('div', { class: 'tabs', role: 'tablist', 'aria-label': label });
  const btns = items.map((it) => h('button', {
    class: 'tab', role: 'tab', id: 'tab-' + it.id, type: 'button',
    'aria-selected': String(it.id === active), tabIndex: it.id === active ? 0 : -1,
    onclick: () => onSelect(it.id),
  }, it.dot ? h('span', { class: 'dot ' + it.dot, 'aria-hidden': 'true' }) : null, it.label,
  it.count !== undefined ? h('span', { class: 'count', text: it.count }) : null));
  list.addEventListener('keydown', (e) => {
    const i = btns.indexOf(document.activeElement);
    if (i < 0) return;
    let j = -1;
    if (e.key === 'ArrowRight') j = (i + 1) % btns.length;
    else if (e.key === 'ArrowLeft') j = (i - 1 + btns.length) % btns.length;
    else if (e.key === 'Home') j = 0;
    else if (e.key === 'End') j = btns.length - 1;
    if (j >= 0) { e.preventDefault(); onSelect(items[j].id); requestAnimationFrame(() => document.getElementById('tab-' + items[j].id)?.focus()); }
  });
  list.append(...btns);
  return list;
}

/** A toggle switch row. */
export function toggleRow({ id, label, hint, checked, onChange, disabled }) {
  const input = h('input', { type: 'checkbox', id, role: 'switch', class: 'switch', checked: !!checked, disabled: !!disabled, onchange: (e) => onChange(e.target.checked) });
  return h('div', { class: 'row setting' },
    h('div', { class: 'grow' }, h('label', { htmlFor: id, text: label }), hint ? h('p', { class: 'muted small', text: hint }) : null),
    input);
}
