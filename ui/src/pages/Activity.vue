<script setup lang="ts">
// Activity: what happened recently (docs/UI.md 5.3). Changes, Traffic, Network, Warnings.
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { actions, pages } from '../../design/copy';
import Tabs from '../components/Tabs.vue';
import ProcessList from '../components/ProcessList.vue';
import { ACTIVITY_TABS, CHART_H, CHART_W, bars, changeRows, charts, networkFacts, networkRows, parseTab, trafficStats, warningRows, type ActivityTab } from '../lib/activityModel';
import { go, route } from '../router';
import { useStore } from '../store';
import { view } from '../ui';

const store = useStore();
const S = store.S;
const t = pages.activity;

const tab = ref<ActivityTab>(parseTab(route.params.tab));
watch(
  () => route.params.tab,
  (v) => {
    if (route.page === 'activity' && v) tab.value = parseTab(v);
  },
);
const tabModel = computed({ get: () => tab.value as string, set: (v: string) => (tab.value = parseTab(v)) });

const snapshot = computed(() => (S.engineDown ? null : S.snapshot));
const warnings = computed(() => warningRows(snapshot.value?.Warnings));
const tabs = computed(() => ACTIVITY_TABS.map((id) => ({ id, label: t.tabs[id], ...(id === 'warnings' ? { count: warnings.value.length } : {}) })));

// Changes
const changes = computed(() => changeRows(snapshot.value?.Recent, view.changesClearedAt));
function clearView(): void {
  view.changesClearedAt = Date.now();
}

// Traffic
const on = computed(() => S.state === 'connected' && !S.engineDown);
const stats = computed(() => trafficStats(S.traffic));
const chartList = computed(() => charts(snapshot.value?.Traffic, S.series));

// Network
const now = ref(Date.now());
let tick: ReturnType<typeof setInterval> | undefined;
onMounted(() => {
  tick = setInterval(() => (now.value = Date.now()), 30000);
});
onBeforeUnmount(() => clearInterval(tick));
const facts = computed(() => networkFacts(snapshot.value, S.checking, now.value));
const nets = computed(() => networkRows(S.networks));
</script>

<template>
  <section class="page activity" aria-labelledby="act-h">
    <h1 id="act-h">{{ t.title }}</h1>
    <Tabs v-model="tabModel" :items="tabs" label="Activity views" />

    <div class="tabpanel" role="tabpanel" tabindex="-1">
      <p v-if="S.engineDown" class="empty">The engine is not running.</p>

      <template v-else-if="tab === 'changes'">
        <div class="row">
          <p class="muted grow">{{ t.changesLead }}</p>
          <button class="btn small" type="button" :disabled="!changes.length" @click="clearView">{{ actions.clearView }}</button>
        </div>
        <p v-if="!changes.length" class="empty">{{ t.noChangesShown }}</p>
        <ol v-else class="timeline">
          <li v-for="c in changes" :key="c.key">
            <button class="link plain" type="button" :aria-label="`${c.text}. Show in Sites`" @click="go('sites', { domain: c.site, q: c.site })">
              <strong>{{ c.site }}</strong> moved from {{ c.from }} to {{ c.to }}<span class="muted"> · {{ c.time }}</span>
            </button>
          </li>
        </ol>
      </template>

      <template v-else-if="tab === 'traffic'">
        <div class="stats">
          <div v-for="s in stats" :key="s.label" class="stat card">
            <span class="muted small">{{ s.label }}</span>
            <strong class="big-num">{{ s.value }}</strong>
          </div>
        </div>
        <p v-if="!on" class="muted">{{ t.trafficLead }}</p>
        <div class="charts">
          <figure v-for="c in chartList" :key="c.route" class="chart">
            <figcaption>
              <span :class="['dot', c.dot]" aria-hidden="true"></span><strong>{{ c.title }}</strong><span class="muted"> · {{ c.today }}</span>
            </figcaption>
            <svg :class="['bars', c.dot]" :viewBox="`0 0 ${CHART_W} ${CHART_H}`" role="img" :aria-label="c.label" preserveAspectRatio="none">
              <line x1="0" :y1="CHART_H - 0.5" :x2="CHART_W" :y2="CHART_H - 0.5" class="axis" />
              <rect v-for="(b, i) in bars(c.values, c.max)" :key="i" :x="b.x" :y="b.y" :width="b.width" :height="b.height" rx="1.5" />
            </svg>
          </figure>
        </div>
      </template>

      <template v-else-if="tab === 'network'">
        <dl class="facts wide">
          <template v-for="f in facts" :key="f.term">
            <dt>{{ f.term }}</dt>
            <dd>{{ f.value }}</dd>
          </template>
        </dl>
        <h2 class="h3">{{ t.networksSeen }}</h2>
        <ul v-if="nets.length" class="history">
          <li v-for="(n, i) in nets" :key="i">{{ n.label }}<span class="muted"> · {{ n.since }}</span></li>
        </ul>
        <p v-else class="muted">{{ t.noNetworksYet }}</p>
        <p class="muted small">{{ t.networkPrivacy }}</p>
      </template>

      <template v-else>
        <p v-if="!warnings.length" class="empty">{{ t.noWarnings }}</p>
        <ul v-else class="warnlist">
          <li v-for="w in warnings" :key="w.text" class="card">
            <strong>{{ w.text }}</strong>
            <p class="muted">{{ w.why }}</p>
            <p><span class="label">{{ t.whatToDo }}</span>{{ w.todo }}</p>
          </li>
        </ul>
        <h2 class="h3">Engine processes</h2>
        <ProcessList />
      </template>
    </div>
  </section>
</template>
