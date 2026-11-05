// Per-site rules (docs/UI.md 5.2): cleaning of the typed or pasted site name, the four choices in
// user words, error sentences, and the logic of the rule dialog (separate from the component so it
// can be tested). The engine rejects anything but a plain name with 422, so cleaning is done here.

import { reactive } from 'vue';
import { pages, routes as routeCopy } from '../../design/copy';
import type { RouteInfo, SiteAction, SiteRule } from '../backend/types';
import type { Store } from '../store';
import { fill } from './format';

const t = pages.sites;

/** Order of the choices. */
export const SITE_ACTIONS: readonly SiteAction[] = ['direct', 'desync', 'tunnel', 'block'];

export const MAX_SITE_RULES = 500;

export interface SiteChoice { action: SiteAction; word: string; line: string; dot: string }

/** The four paths in user words (design/copy.ts `routes`); engine names stay out of sight. */
export const siteChoices: SiteChoice[] = SITE_ACTIONS.map((a) => ({ action: a, word: routeCopy[a].word, line: routeCopy[a].line, dot: `r-${a}` }));

export const actionWord = (a: string): string => routeCopy[a as SiteAction]?.word ?? a;

export type CleanResult = { ok: true; domain: string } | { ok: false; message: string };

const IPV4 = /^\d{1,3}(\.\d{1,3}){3}$/;
const LABEL = /^[\p{L}\p{N}_-]+$/u;

/**
 * Turns what the user typed or pasted into a plain domain name: scheme, user info, path, query,
 * fragment, port, trailing dot and a leading `*.` or `.` are dropped; the name is lower-cased. A
 * leading "www." is kept (it is a different name). Empty input, IP addresses and anything that is
 * not a name come back as an inline message; nothing is sent in that case.
 */
export function cleanSiteInput(raw: string): CleanResult {
  const err = (message: string): CleanResult => ({ ok: false, message });
  let s = raw.trim();
  if (!s) return err(t.ruleErrors.empty);
  if (/\s/.test(s)) return err(t.ruleErrors.notName);
  s = s.replace(/^[a-z][a-z0-9+.-]*:\/\//i, '').replace(/^\/\//, '');
  const cut = s.search(/[/?#]/);
  if (cut >= 0) s = s.slice(0, cut);
  const at = s.lastIndexOf('@');
  if (at >= 0) s = s.slice(at + 1);
  if (s.startsWith('[')) return err(t.ruleErrors.address);
  s = s.replace(/:\d*$/, '');
  if (s.includes(':')) return err(/^[0-9a-f:.]+$/i.test(s) ? t.ruleErrors.address : t.ruleErrors.notName);
  s = s.toLowerCase().replace(/^\*?\./, '').replace(/\.+$/, '');
  if (!s) return err(t.ruleErrors.empty);
  if (IPV4.test(s)) return err(t.ruleErrors.address);
  if (s.length > 253) return err(t.ruleErrors.notName);
  for (const label of s.split('.')) {
    if (!label || label.length > 63 || !LABEL.test(label) || label.startsWith('-') || label.endsWith('-')) return err(t.ruleErrors.notName);
  }
  return { ok: true, domain: s };
}

/**
 * Plain sentence for a failed site call. `invalid_input` carries the engine's (or the Rust side's)
 * fixed sentence, never the typed name; the rest are the UI's.
 */
export function siteErrorText(e: { code?: string; message?: string } | null | undefined): string {
  switch (e?.code) {
    case 'invalid_input':
      return e.message || t.ruleErrors.notName;
    case 'limit_reached':
      return t.ruleErrors.limit;
    case 'not_found':
      return t.ruleErrors.notFound;
    case 'bad_request':
      return t.ruleErrors.rejected;
    case 'engine_unreachable':
      return 'The engine is not running.';
    default:
      return 'Something went wrong.';
  }
}

/** True when the table row is decided by a rule of the user (or there is a stored rule for it). */
export function hasRule(r: Pick<RouteInfo, 'domain' | 'source'>, rules: readonly Pick<SiteRule, 'domain'>[]): boolean {
  return r.source === 'user' || rules.some((x) => x.domain === r.domain);
}

/** The choice shown first: the site's current path when it is one of the four, else Everyday. */
export function initialAction(route: string | undefined): SiteAction {
  return SITE_ACTIONS.find((a) => a === route) ?? 'direct';
}

export const ruleText = {
  title: (site: string) => fill(t.rule.useTitle, { site }),
  saved: (site: string, action: SiteAction, isNew: boolean) => fill(isNew ? t.rule.added : t.rule.changed, { site, path: actionWord(action) }),
  removed: (site: string) => fill(t.rule.removed, { site }),
};

export type MsgKind = '' | 'info' | 'err';

export interface SiteRuleModel {
  /** What the user typed (the "add" form only). */
  domain: string;
  action: SiteAction;
  busy: boolean;
  msg: { text: string; kind: MsgKind };
  /** Cleans and saves. Resolves with the saved rule (the dialog then closes), `null` when an inline message is shown. */
  submit(): Promise<SiteRule | null>;
  /** Hide an old message once the user edits something. */
  edited(): void;
}

/**
 * `fixedDomain` is set for "Always use…" in the side panel (the name is already clean); without it the
 * model cleans what was typed.
 */
export function createSiteRuleModel(
  store: Pick<Store, 'setSite'> & { S: { sites: readonly Pick<SiteRule, 'domain'>[] } },
  toast: (text: string) => void,
  opts: { fixedDomain?: string; action?: SiteAction } = {},
): SiteRuleModel {
  const m = reactive<SiteRuleModel>({
    domain: '',
    action: opts.action ?? 'direct',
    busy: false,
    msg: { text: '', kind: '' },
    async submit() {
      if (m.busy) return null;
      let domain = opts.fixedDomain;
      if (domain === undefined) {
        const c = cleanSiteInput(m.domain);
        if (!c.ok) {
          say(c.message, 'err');
          return null;
        }
        domain = c.domain;
        m.domain = domain; // shows what will be sent
      }
      const isNew = !store.S.sites.some((x) => x.domain === domain);
      m.busy = true;
      say(t.rule.saving, 'info');
      try {
        const rule = await store.setSite(domain, m.action);
        say('', '');
        toast(ruleText.saved(rule.domain, rule.action, isNew));
        return rule;
      } catch (e) {
        say(siteErrorText(e as { code?: string; message?: string }), 'err');
        return null;
      } finally {
        m.busy = false;
      }
    },
    edited() {
      if (m.msg.kind === 'err') say('', '');
    },
  });

  function say(text: string, kind: MsgKind): void {
    m.msg = { text, kind };
  }
  return m;
}
