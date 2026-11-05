// Guards for the rules in ui/AGENTS.md: they fail when someone breaks them.
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative, sep } from 'node:path';
import { describe, expect, it } from 'vitest';
import { icons, fanPaths } from './lib/icons';

const ROOT = join(__dirname, '..');
const SRC = join(ROOT, 'src');

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, out);
    else out.push(p);
  }
  return out;
}

const sources = walk(SRC).filter((f) => /\.(ts|vue)$/.test(f) && !/\.test\.ts$/.test(f) && !f.includes(`${sep}test${sep}`));
const rel = (f: string): string => relative(ROOT, f).split(sep).join('/');

describe('source rules (ui/AGENTS.md)', () => {
  it('finds the sources', () => {
    expect(sources.length).toBeGreaterThan(20);
  });

  it('has no HTML injection, eval or document.write', () => {
    const bad = /\b(innerHTML|outerHTML|insertAdjacentHTML|document\.write|eval\(|new Function\()|v-html/;
    const hits = sources.filter((f) => bad.test(readFileSync(f, 'utf8'))).map(rel);
    expect(hits).toEqual([]);
  });

  it('never talks to the network itself (only Tauri commands)', () => {
    const bad = /\b(fetch\(|XMLHttpRequest|EventSource|WebSocket|sendBeacon)/;
    const hits = sources.filter((f) => bad.test(readFileSync(f, 'utf8'))).map(rel);
    expect(hits).toEqual([]);
  });

  it('has no external URLs, fonts or CDNs', () => {
    const hits: string[] = [];
    for (const f of sources) {
      for (const m of readFileSync(f, 'utf8').matchAll(/https?:\/\/[^\s'"`)]+/g)) {
        // XML namespaces and documentation comments are not requests.
        if (!/^http:\/\/www\.w3\.org\//.test(m[0])) hits.push(`${rel(f)}: ${m[0]}`);
      }
    }
    expect(hits).toEqual([]);
  });

  it('imports @tauri-apps/api only in the Tauri backend', () => {
    const hits = sources.filter((f) => /@tauri-apps\//.test(readFileSync(f, 'utf8'))).map(rel);
    expect(hits).toEqual(['src/backend/tauri.ts']);
  });

  it('mentions no token anywhere in the frontend code', () => {
    const hits = sources.filter((f) => /\b(token|bearer|authorization)\b/i.test(readFileSync(f, 'utf8'))).map(rel);
    expect(hits).toEqual([]);
  });

  it('reaches the fake backend only through the DEV-guarded dynamic import', () => {
    const importers = sources.filter((f) => /(from|import\()\s*'[^']*fakeBackend'/.test(readFileSync(f, 'utf8'))).map(rel);
    expect(importers).toEqual(['src/backend/index.ts']);
    const idx = readFileSync(join(SRC, 'backend', 'index.ts'), 'utf8');
    expect(idx).toMatch(/import\.meta\.env\.DEV[\s\S]*import\('\.\.\/dev\/fakeBackend'\)/);
  });

  it('does not depend on the removed prototype (ui/web, ui/mock)', () => {
    expect(existsSync(join(ROOT, 'web'))).toBe(false);
    expect(existsSync(join(ROOT, 'mock'))).toBe(false);
    const hits = sources.filter((f) => /ui\/(web|mock)\b/.test(readFileSync(f, 'utf8'))).map(rel);
    expect(hits).toEqual([]);
  });

  it('has the five pages and no placeholder', () => {
    const pagesDir = readdirSync(join(SRC, 'pages')).sort();
    expect(pagesDir).toEqual(['Activity.vue', 'Help.vue', 'Home.vue', 'Settings.vue', 'Sites.vue']);
  });

  it('shows no per-site traffic (the API has none, decision 6)', () => {
    const sites = readFileSync(join(SRC, 'pages', 'Sites.vue'), 'utf8') + readFileSync(join(SRC, 'lib', 'sitesModel.ts'), 'utf8');
    expect(sites).not.toMatch(/traffic/i);
  });

  it('keeps a restrictive Tauri capability and CSP', () => {
    const cap = JSON.parse(readFileSync(join(ROOT, 'src-tauri', 'capabilities', 'default.json'), 'utf8')) as { permissions: string[] };
    expect(cap.permissions.sort()).toEqual(['core:event:allow-listen', 'core:event:allow-unlisten']);
    const conf = JSON.parse(readFileSync(join(ROOT, 'src-tauri', 'tauri.conf.json'), 'utf8')) as { app: { security: { csp: string } } };
    const csp = conf.app.security.csp;
    expect(csp).toMatch(/default-src 'self'/);
    // http://ipc.localhost is Tauri's own IPC origin on Windows; nothing else may be remote.
    expect(csp.replace('http://ipc.localhost', '')).not.toMatch(/unsafe-eval|unsafe-inline|https?:|\*/);
  });

  it('pins every dependency to an exact version', () => {
    const pkg = JSON.parse(readFileSync(join(ROOT, 'package.json'), 'utf8')) as { dependencies: Record<string, string>; devDependencies: Record<string, string> };
    for (const [name, v] of Object.entries({ ...pkg.dependencies, ...pkg.devDependencies })) {
      expect(v, name).toMatch(/^\d+\.\d+\.\d+$/);
    }
    const cargo = readFileSync(join(ROOT, 'src-tauri', 'Cargo.toml'), 'utf8');
    for (const line of cargo.split('\n')) {
      const m = /^\s*[\w-]+\s*=\s*(\{[^}]*version\s*=\s*"([^"]+)"|"([^"]+)")/.exec(line);
      const v = m?.[2] ?? m?.[3];
      if (v && !/^(true|false|\d+)$/.test(v) && /^[\w.=<>^~*-]+$/.test(v) && /\d/.test(v) && !line.includes('rust-version') && !line.startsWith('version') && !line.startsWith('edition')) {
        expect(v, line.trim()).toMatch(/^=\d+\.\d+\.\d+$/);
      }
    }
  });
});

describe('icons match the design kit', () => {
  const svgOf = (name: string): string => readFileSync(join(ROOT, 'design', 'icons', `${name}.svg`), 'utf8');
  const pathsOf = (svg: string): string[] => [...svg.matchAll(/<path d="([^"]+)"/g)].map((m) => m[1]!);
  const circlesOf = (svg: string): string[] => [...svg.matchAll(/<circle cx="(\d+)" cy="(\d+)" r="(\d+)"/g)].map((m) => `${m[1]},${m[2]},${m[3]}`);

  for (const name of Object.keys(icons) as Array<keyof typeof icons>) {
    it(name, () => {
      const svg = svgOf(name);
      const def = icons[name];
      expect(def.els.filter((e) => e.tag === 'path').map((e) => e.attrs.d)).toEqual(pathsOf(svg));
      expect(def.els.filter((e) => e.tag === 'circle').map((e) => `${e.attrs.cx},${e.attrs.cy},${e.attrs.r}`)).toEqual(circlesOf(svg));
      expect(svg).toContain(`stroke-width="${def.stroke}"`);
    });
  }

  it('fan', () => {
    expect([...fanPaths]).toEqual(pathsOf(svgOf('fan')));
  });
});
