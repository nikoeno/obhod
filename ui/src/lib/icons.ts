// Icons from ui/design/icons/*.svg as data (no SVG injection: components render these with
// Vue bindings). A test compares them with the SVG files so they cannot drift.

export interface IconEl {
  tag: 'path' | 'circle';
  attrs: Record<string, string>;
}

export interface IconDef {
  /** stroke width of the source SVG */
  stroke: number;
  els: IconEl[];
}

const p = (d: string): IconEl => ({ tag: 'path', attrs: { d } });
const c = (cx: number, cy: number, r: number): IconEl => ({ tag: 'circle', attrs: { cx: String(cx), cy: String(cy), r: String(r) } });

export const icons = {
  home: { stroke: 1.8, els: [p('M4 11 12 4l8 7v9h-5v-6H9v6H4z')] },
  sites: { stroke: 1.8, els: [c(12, 12, 8), p('M4 12h16M12 4c3 3 3 13 0 16M12 4c-3 3-3 13 0 16')] },
  activity: { stroke: 1.8, els: [p('M3 12h4l3-7 4 14 3-7h4')] },
  settings: {
    stroke: 1.8,
    els: [c(12, 12, 3), p('M12 3v3M12 18v3M3 12h3M18 12h3M5.6 5.6l2.1 2.1M16.3 16.3l2.1 2.1M5.6 18.4l2.1-2.1M16.3 7.7l2.1-2.1')],
  },
  help: { stroke: 1.8, els: [c(12, 12, 9), p('M9.5 9.5a2.5 2.5 0 1 1 3.5 2.3c-.7.3-1 .8-1 1.5V14M12 17.5v.01')] },
  power: { stroke: 2.2, els: [p('M12 3v8'), p('M6.3 7.2a8 8 0 1 0 11.4 0')] },
} as const satisfies Record<string, IconDef>;

export type IconName = keyof typeof icons;

/** The fan (design/icons/fan.svg): three curves from the button to the three path cards. */
export const fanPaths = ['M300 0 C300 34, 100 26, 100 60', 'M300 0 C300 34, 300 26, 300 60', 'M300 0 C300 34, 500 26, 500 60'] as const;
