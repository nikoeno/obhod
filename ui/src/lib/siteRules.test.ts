import { describe, expect, it } from 'vitest';
import { BackendError } from '../backend/types';
import { makeStore } from '../test/helpers';
import { cleanSiteInput, createSiteRuleModel, hasRule, initialAction, ruleText, siteChoices, siteErrorText } from './siteRules';

const ok = (raw: string): string => {
  const r = cleanSiteInput(raw);
  if (!r.ok) throw new Error(`rejected: ${r.message}`);
  return r.domain;
};
const bad = (raw: string): string => {
  const r = cleanSiteInput(raw);
  if (r.ok) throw new Error(`accepted: ${r.domain}`);
  return r.message;
};

describe('cleanSiteInput', () => {
  it('keeps a plain name and lower-cases it', () => {
    expect(ok('example.com')).toBe('example.com');
    expect(ok('  Example.COM \n')).toBe('example.com');
    expect(ok('xn--e1afmkfd.xn--p1ai')).toBe('xn--e1afmkfd.xn--p1ai');
    expect(ok('пример.рф')).toBe('пример.рф');
    expect(ok('my_host.example')).toBe('my_host.example');
  });

  it('strips scheme, user info, path, query, fragment and port', () => {
    expect(ok('https://example.com/some/path?q=1#top')).toBe('example.com');
    expect(ok('HTTP://User:pw@Example.com:8443/x')).toBe('example.com');
    expect(ok('//example.com/')).toBe('example.com');
    expect(ok('example.com:443')).toBe('example.com');
    expect(ok('example.com/path')).toBe('example.com');
    expect(ok('example.com?x=1')).toBe('example.com');
    expect(ok('wss://sub.example.com')).toBe('sub.example.com');
  });

  it('keeps "www." only when it was typed', () => {
    expect(ok('www.example.com')).toBe('www.example.com');
    expect(ok('https://www.example.com/')).toBe('www.example.com');
    expect(ok('https://example.com/')).toBe('example.com');
  });

  it('drops a trailing dot and a leading wildcard or dot', () => {
    expect(ok('example.com.')).toBe('example.com');
    expect(ok('*.example.com')).toBe('example.com');
    expect(ok('.example.com')).toBe('example.com');
  });

  it('rejects empty input', () => {
    for (const raw of ['', '   ', 'https://', '/', '.', '*.']) expect(bad(raw), raw).toMatch(/Enter a site name/);
  });

  it('rejects IP addresses with their own message', () => {
    for (const raw of ['192.0.2.1', 'http://192.0.2.1:8080/x', '[2001:db8::1]', '[2001:db8::1]:443', '2001:db8::1']) {
      expect(bad(raw), raw).toMatch(/not an IP address/);
    }
  });

  it('rejects what is not a name', () => {
    for (const raw of ['two words.com', 'a..b.com', '-a.com', 'a-.com', 'a*b.com', 'a$b.example', 'host:name:x', `${'a'.repeat(64)}.com`, `${'a.'.repeat(130)}com`]) {
      expect(bad(raw), raw).toMatch(/not a plain site name/);
    }
  });

  it('never echoes the input in a message', () => {
    expect(bad('SecretSite!.example')).not.toMatch(/secret/i);
  });
});

describe('siteErrorText', () => {
  it('shows the fixed engine sentence for invalid_input and never the typed name', () => {
    expect(siteErrorText({ code: 'invalid_input', message: 'That is not a plain site name. Enter only the name, for example example.com.' })).toMatch(/plain site name/);
    expect(siteErrorText({ code: 'invalid_input' })).toMatch(/plain site name/);
  });
  it('words the other codes', () => {
    expect(siteErrorText({ code: 'limit_reached', message: 'x' })).toMatch(/500 rules/);
    expect(siteErrorText({ code: 'not_found' })).toMatch(/no rule for this site/);
    expect(siteErrorText({ code: 'bad_request' })).toMatch(/did not accept/);
    expect(siteErrorText({ code: 'engine_unreachable' })).toBe('The engine is not running.');
    expect(siteErrorText({ code: 'weird', message: 'leaky text' })).toBe('Something went wrong.');
    expect(siteErrorText(null)).toBe('Something went wrong.');
  });
});

describe('choices and helpers', () => {
  it('offers the four paths in user words', () => {
    expect(siteChoices.map((c) => [c.action, c.word])).toEqual([['direct', 'Everyday'], ['desync', 'Picky'], ['tunnel', 'Blocked'], ['block', 'Blocked by you']]);
    for (const c of siteChoices) expect(c.line).not.toMatch(/direct|desync|tunnel|proxy/i);
  });
  it('starts on the current path when it is a valid action', () => {
    expect(initialAction('tunnel')).toBe('tunnel');
    expect(initialAction('proxy')).toBe('direct');
    expect(initialAction(undefined)).toBe('direct');
  });
  it('knows a ruled row by its source or a stored rule', () => {
    expect(hasRule({ domain: 'a.example', source: 'user' }, [])).toBe(true);
    expect(hasRule({ domain: 'a.example', source: 'checked' }, [{ domain: 'a.example' }])).toBe(true);
    expect(hasRule({ domain: 'a.example', source: 'checked' }, [{ domain: 'b.example' }])).toBe(false);
  });
  it('words the toasts', () => {
    expect(ruleText.saved('a.example', 'tunnel', true)).toBe('a.example will always use Blocked.');
    expect(ruleText.saved('a.example', 'block', false)).toBe('a.example will now always use Blocked by you.');
    expect(ruleText.removed('a.example')).toBe('Your rule for a.example is gone. The app decides again.');
  });
});

describe('the rule dialog model', () => {
  it('cleans what was typed, saves it and toasts', async () => {
    const { store, toasts } = makeStore();
    await store.init();
    const m = createSiteRuleModel(store, (t) => toasts.push(t));
    m.domain = 'https://New-Site.example/page?x=1';
    m.action = 'tunnel';
    const rule = await m.submit();
    expect(rule).toMatchObject({ domain: 'new-site.example', action: 'tunnel' });
    expect(toasts.at(-1)).toBe('new-site.example will always use Blocked.');
    expect(store.S.sites.map((r) => r.domain)).toContain('new-site.example');
    expect(m.msg.kind).toBe('');
    expect(m.busy).toBe(false);
  });

  it('shows an inline message and sends nothing for a bad name', async () => {
    const { store, backend, toasts } = makeStore();
    await store.init();
    let calls = 0;
    const orig = backend.setSite;
    backend.setSite = (...a) => {
      calls++;
      return orig(...a);
    };
    const m = createSiteRuleModel(store, (t) => toasts.push(t));
    m.domain = '192.0.2.1';
    expect(await m.submit()).toBeNull();
    expect(m.msg).toEqual({ text: 'Enter the site name, not an IP address.', kind: 'err' });
    m.edited();
    expect(m.msg.kind).toBe('');
    m.domain = '';
    expect(await m.submit()).toBeNull();
    expect(calls).toBe(0);
    expect(toasts).toEqual([]);
  });

  it('uses the fixed domain of the side panel without cleaning it', async () => {
    const { store, toasts } = makeStore();
    await store.init();
    const m = createSiteRuleModel(store, (t) => toasts.push(t), { fixedDomain: 'news.example', action: 'block' });
    expect(await m.submit()).toMatchObject({ domain: 'news.example', action: 'block' });
    expect(toasts.at(-1)).toBe('news.example will always use Blocked by you.');
  });

  it('keeps the dialog open with the engine error inline', async () => {
    const { store, backend } = makeStore();
    await store.init();
    backend.setSite = async () => {
      throw new BackendError('limit_reached', 'The list of site rules is full. Remove a rule first.');
    };
    const m = createSiteRuleModel(store, () => undefined);
    m.domain = 'full.example';
    expect(await m.submit()).toBeNull();
    expect(m.msg).toEqual({ text: 'You have reached the limit of 500 rules. Remove one first.', kind: 'err' });
    expect(m.busy).toBe(false);
  });

  it('says "now" when the rule already existed', async () => {
    const { store, toasts } = makeStore();
    await store.init();
    const m = createSiteRuleModel(store, (t) => toasts.push(t));
    m.domain = 'again.example';
    await m.submit();
    m.action = 'block';
    await m.submit();
    expect(toasts.at(-1)).toBe('again.example will now always use Blocked by you.');
  });
});
