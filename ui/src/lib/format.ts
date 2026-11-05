// Formatting rules from ui/design/README.md section 9.

/** 1024-based; one decimal below 100, none from 100 or for plain bytes; <=0 or invalid is "0 B". */
export function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

// The engine sends the zero time (year 1) for "unknown".
const KNOWN_AFTER = Date.UTC(2000, 0, 1);

function known(iso: string): number | null {
  const t = Date.parse(iso);
  return Number.isFinite(t) && t >= KNOWN_AFTER ? t : null;
}

/** Future time: "in 12 min", "in 2 h", "now" for <= 0, "–" when unknown. */
export function fmtIn(iso: string, now = Date.now()): string {
  const t = known(iso);
  if (t === null) return '–';
  const m = Math.round((t - now) / 60000);
  if (m <= 0) return 'now';
  if (m < 60) return `in ${m} min`;
  return `in ${Math.round(m / 60)} h`;
}

/** Past time: "just now", "5 min ago", "3 h ago", "2 d ago", "never" when unknown. */
export function fmtAgo(iso: string, now = Date.now()): string {
  const t = known(iso);
  if (t === null) return 'never';
  const s = Math.round((now - t) / 1000);
  if (s < 0) return fmtIn(iso, now);
  if (s < 45) return 'just now';
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h} h ago`;
  return `${Math.round(h / 24)} d ago`;
}

/** Clock time HH:MM in the user's locale, "–" when unknown. */
export function fmtTime(iso: string): string {
  const t = known(iso);
  if (t === null) return '–';
  return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

/** 0..1 fraction to "78%". */
export function pct(x: number | undefined | null): string {
  return `${Math.round((x || 0) * 100)}%`;
}

/** Fills `{name}` placeholders. Unknown names are left as they are. */
export function fill(template: string, vars: Record<string, string | number>): string {
  return template.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m));
}

/** "1 site" / "N sites" from a `{one, other}` pair. */
export function plural(forms: { one: string; other: string }, n: number): string {
  return fill(n === 1 ? forms.one : forms.other, { n });
}
