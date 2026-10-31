<script setup lang="ts">
// Sites: what the app decided for each site (docs/UI.md 5.2). Read-only in v1.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { actions, pages } from '../../design/copy';
import Tabs from '../components/Tabs.vue';
import { isLearning } from '../lib/errors';
import { fmtAgo } from '../lib/format';
import { countLine, effectiveFilter, emptyText, filterSites, historyRows, listView, panelFacts, parseFilter, siteTabs, sourceWord, routeWord, type SiteFilter } from '../lib/sitesModel';
import { route } from '../router';
import { useStore } from '../store';

const store = useStore();
const S = store.S;
const t = pages.sites;

const filter = ref<SiteFilter>(parseFilter(route.params.route));
const query = ref(route.params.q ?? '');
const selected = ref<string | null>(route.params.domain ?? null);
const panel = ref<HTMLElement | null>(null);
const listEl = ref<HTMLElement | null>(null);

// A link from Home (route) or Activity (domain, q) while this page is open. Only the parameters present are applied.
watch(
  () => route.params,
  (p) => {
    if (route.page !== 'sites') return;
    if (p.route !== undefined) filter.value = parseFilter(p.route);
    if (p.q !== undefined) query.value = p.q;
    if (p.domain !== undefined) selected.value = p.domain;
  },
);

const tabs = computed(() => siteTabs(S.routes));
const activeFilter = computed(() => effectiveFilter(filter.value, tabs.value));
const tabModel = computed({ get: () => activeFilter.value as string, set: (v: string) => (filter.value = parseFilter(v)) });
const rows = computed(() => filterSites(S.routes, activeFilter.value, query.value));
const view = computed(() =>
  listView({ engineDown: S.engineDown, learning: isLearning(S.snapshot?.Network), loaded: S.loaded, total: S.routes.length, shown: rows.value.length, query: query.value }),
);
const status = computed(() => (view.value === 'list' || view.value === 'no-match' || view.value === 'none-on-path' ? countLine(rows.value.length, query.value) : ''));

const site = computed(() => (selected.value ? (S.routes.find((r) => r.domain === selected.value) ?? null) : null));
const facts = computed(() => (site.value ? panelFacts(site.value) : null));
const history = computed(() => historyRows(site.value?.history));
const checkingThis = computed(() => !!site.value && S.checking && !!S.checkingDomains?.includes(site.value.domain));
const allChecking = computed(() => S.checking && !S.checkingDomains);

async function open(domain: string): Promise<void> {
  selected.value = domain;
  await nextTick();
  panel.value?.focus();
}

function close(): void {
  const was = selected.value;
  selected.value = null;
  void nextTick(() => {
    const btn = [...(listEl.value?.querySelectorAll<HTMLButtonElement>('button.site') ?? [])].find((b) => b.dataset.domain === was);
    btn?.focus();
  });
}

function onKey(e: KeyboardEvent): void {
  if (e.key === 'Escape' && selected.value && !document.querySelector('dialog[open]')) {
    e.preventDefault();
    close();
  }
}
onMounted(() => document.addEventListener('keydown', onKey));
onBeforeUnmount(() => document.removeEventListener('keydown', onKey));

// Keeps "since 5 min ago" fresh.
const now = ref(Date.now());
let tick: ReturnType<typeof setInterval> | undefined;
onMounted(() => {
  tick = setInterval(() => (now.value = Date.now()), 30000);
});
onBeforeUnmount(() => clearInterval(tick));
</script>

<template>
  <section class="page sites" aria-labelledby="sites-h">
    <h1 id="sites-h">{{ t.title }}</h1>
    <p class="muted lead">{{ t.lead }}</p>

    <Tabs v-model="tabModel" :items="tabs" label="Filter by path" />

    <div class="toolbar">
      <input id="site-search" v-model="query" type="search" class="search" :placeholder="t.search" :aria-label="t.search" aria-keyshortcuts="Control+F" autocomplete="off" spellcheck="false" />
      <span class="grow"></span>
      <button class="btn" type="button" aria-keyshortcuts="Control+R" :disabled="S.checking || S.engineDown" @click="store.checkNow()">{{ allChecking ? actions.checking : actions.checkAll }}</button>
      <button class="btn" type="button" disabled aria-describedby="add-later">{{ t.addSite }}</button>
      <span id="add-later" class="later">{{ actions.later }}</span>
    </div>
    <p class="muted small" aria-live="polite">{{ status }}</p>

    <div class="split">
      <div ref="listEl" class="list-slot">
        <p v-if="view !== 'list'" class="empty">{{ emptyText(view, query) }}</p>
        <table v-else class="sites-table">
          <caption class="sr-only">Sites and the path each one takes</caption>
          <thead>
            <tr>
              <th scope="col">{{ t.columns.site }}</th>
              <th scope="col">{{ t.columns.path }}</th>
              <th scope="col">{{ t.columns.since }}</th>
              <th scope="col">{{ t.columns.decided }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rows" :key="r.domain" :class="{ sel: r.domain === selected }">
              <td>
                <button class="link plain site" type="button" :data-domain="r.domain" :aria-expanded="r.domain === selected" @click="open(r.domain)">{{ r.domain }}</button>
              </td>
              <td><span :class="['dot', `r-${r.route}`]" aria-hidden="true"></span>{{ routeWord(r.route) }}</td>
              <td class="muted">{{ fmtAgo(r.since, now) }}</td>
              <td class="muted">{{ sourceWord(r.source) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <aside v-if="site && facts" ref="panel" class="sidepanel" aria-label="Site details" tabindex="-1">
        <div class="row">
          <h2 class="grow">{{ site.domain }}</h2>
          <button class="icon-btn" type="button" aria-label="Close details" @click="close">×</button>
        </div>
        <dl class="facts">
          <dt>{{ t.panel.path }}</dt>
          <dd><span :class="['dot', facts.dot]" aria-hidden="true"></span>{{ facts.long }} <code class="tech">{{ facts.engine }}</code></dd>
          <dt>{{ t.panel.since }}</dt>
          <dd>{{ facts.since }}</dd>
          <dt>{{ t.panel.decided }}</dt>
          <dd>{{ facts.decided }}</dd>
          <dt>{{ t.panel.lastCheck }}</dt>
          <dd>{{ facts.lastCheck }}</dd>
        </dl>
        <h3>{{ t.panel.history }}</h3>
        <ul v-if="history.length" class="history">
          <li v-for="(h, i) in history" :key="i">
            {{ h.text }} <span class="muted">· {{ h.time }}</span> <code class="tech">{{ h.tech }}</code>
          </li>
        </ul>
        <p v-else class="muted small">{{ pages.activity.noChanges }}</p>
        <div class="stack">
          <button class="btn primary" type="button" :disabled="S.checking || S.engineDown" @click="store.checkNow([site.domain])">{{ checkingThis ? actions.checking : actions.checkSite }}</button>
          <button class="btn" type="button" disabled aria-describedby="always-later">{{ t.panel.alwaysUse }}</button>
          <span id="always-later" class="later">{{ t.alwaysUseLater }}</span>
        </div>
      </aside>
    </div>
  </section>
</template>
