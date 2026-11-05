// A tiny hash router (no router library). `#/settings?tab=detour` -> page "settings", params {tab: "detour"}.

import { reactive } from 'vue';

export const PAGES = ['home', 'sites', 'activity', 'settings', 'help'] as const;
export type PageName = (typeof PAGES)[number];

export interface RouteState {
  page: PageName;
  params: Record<string, string>;
}

export function parseHash(hash: string): RouteState {
  const raw = hash.replace(/^#\/?/, '');
  const [name, query] = raw.split('?');
  const page = (PAGES as readonly string[]).includes(name ?? '') ? (name as PageName) : 'home';
  return { page, params: Object.fromEntries(new URLSearchParams(query ?? '')) };
}

export function formatHash(page: PageName, params?: Record<string, string>): string {
  const q = new URLSearchParams(params ?? {}).toString();
  return `#/${page}${q ? `?${q}` : ''}`;
}

export const route = reactive<RouteState>(parseHash(typeof location === 'undefined' ? '' : location.hash));

function sync(): void {
  const r = parseHash(location.hash);
  route.page = r.page;
  route.params = r.params;
}

if (typeof window !== 'undefined') window.addEventListener('hashchange', sync);

/** Navigate: go('sites', {route: 'tunnel'}) -> #/sites?route=tunnel */
export function go(page: PageName, params?: Record<string, string>): void {
  const hash = formatHash(page, params);
  if (location.hash === hash) sync();
  else location.hash = hash;
}
