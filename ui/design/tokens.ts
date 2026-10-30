// Obhod design tokens for TypeScript (Vue components, charts, the Tauri tray).
// Source of truth: tokens.json. For styling prefer the CSS variables in tokens.css;
// use these where CSS cannot reach (canvas/SVG charts, tray icon tint, tests).

export type Theme = 'light' | 'dark';
export type Route = 'direct' | 'tunnel' | 'desync' | 'proxy' | 'block';

export const colors = {
  light: {
    bg: '#ffffff',
    card: '#f1efe8',
    border: '#b4b2a9',
    text: '#2c2c2a',
    muted: '#5f5e5a',
    accent: '#534ab7',
    onAccent: '#ffffff',
    accentSoft: 'rgba(83, 74, 183, 0.14)',
    halo: 'rgba(83, 74, 183, 0.28)',
    warnBg: '#f7ecd2',
    warnText: '#5c430c',
    okBg: '#dcefe2',
    okText: '#1f5232',
    backdrop: 'rgba(0, 0, 0, 0.45)',
  },
  dark: {
    bg: '#1c1c1a',
    card: '#2c2c2a',
    border: '#5f5e5a',
    text: '#f1efe8',
    muted: '#b4b2a9',
    accent: '#7f77dd',
    onAccent: '#1c1c1a',
    accentSoft: 'rgba(127, 119, 221, 0.18)',
    halo: 'rgba(127, 119, 221, 0.32)',
    warnBg: '#3d3420',
    warnText: '#f0d9a0',
    okBg: '#213a2a',
    okText: '#b5e3c4',
    backdrop: 'rgba(0, 0, 0, 0.45)',
  },
} as const satisfies Record<Theme, Record<string, string>>;

/** Route colours; tunnel is the accent. */
export const routeColors = {
  light: { direct: '#3b7d55', tunnel: '#534ab7', desync: '#a86b12', proxy: '#3a6fa8', block: '#a8403f' },
  dark: { direct: '#6dbb8b', tunnel: '#7f77dd', desync: '#e0a84a', proxy: '#79aee3', block: '#e07c7a' },
} as const satisfies Record<Theme, Record<Route, string>>;

export const font = {
  sans: 'system-ui, -apple-system, "Segoe UI", Roboto, Ubuntu, sans-serif',
  mono: 'ui-monospace, "Cascadia Mono", Consolas, "DejaVu Sans Mono", monospace',
  basePx: 15,
  lineHeight: 1.45,
} as const;

export const radius = { xs: 4, sm: 6, md: 8, lg: 10, xl: 12, pill: 999 } as const;

export const motion = {
  pressMs: 120,
  toggleMs: 150,
  stateMs: 200,
  fadeMs: 300,
  pulseMs: 1400,
  toastVisibleMs: 2800,
  toastRemoveMs: 3200,
  hoverScale: 1.02,
} as const;

export const breakpoints = { mobileMax: 639, splitMax: 899 } as const;

export const size = {
  navWidth: 200,
  mainMax: 1100,
  homeMax: 820,
  sidepanel: 320,
  dialogMax: 680,
  bigButton: 168,
  bigHalo: 20,
  bigRing: 8,
  pulseMin: 12,
  pulseMax: 26,
} as const;
