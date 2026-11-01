<script setup lang="ts">
// Home: the one-button screen (docs/UI.md 4, 5.1, 6).
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import { actions, extraRoutes, mainRoutes, pages, routes as routeCopy } from '../../design/copy';
import type { Route } from '../backend/types';
import Icon from '../components/Icon.vue';
import { errorInfo, isLearning } from '../lib/errors';
import { fmtAgo, fmtBytes, fill, pct, plural } from '../lib/format';
import { copyText } from '../lib/clipboard';
import { blockedCaveat, buttonModel, detourNotice, detourNoticeText, noDetour, startDetails, subAction } from '../lib/homeModel';
import { fanPaths } from '../lib/icons';
import { problemViews } from '../lib/problems';
import { serverText, serverErrorText } from '../lib/serverText';
import { toast } from '../lib/toast';
import { go } from '../router';
import { useStore } from '../store';
import { showAddServerDialog, showLogDialog, showSetupDialog } from '../ui';

const checkAgain = 'Check again';

const store = useStore();
const S = store.S;

const model = computed(() =>
  buttonModel({ engineDown: S.engineDown, startError: S.startError, state: S.state, error: S.error, snapshot: S.snapshot }),
);
const bigClass = computed(() => {
  const m = model.value;
  return ['big', { on: m.look === 'on', busy: m.look === 'busy' || m.look === 'busy-dim', dim: m.look === 'busy-dim', off: m.look === 'off', err: !!m.err && m.look === 'idle' }];
});
const action = computed(() => subAction(model.value, S.engineDown, S.startError));

const details = computed(() => startDetails(S.engineDown, S.startError));
async function copyDetails(): Promise<void> {
  if (!details.value) return;
  toast((await copyText(details.value)) ? pages.home.startDetails.copied : pages.home.startDetails.copyFailed);
}

// Something does not work although the app is on (snapshot.Problems): always visible while it lasts, no close button.
const problems = computed(() => (S.engineDown ? [] : problemViews(S.problems)));
const restarting = ref(false);
async function restartFromProblem(): Promise<void> {
  restarting.value = true;
  try {
    await store.restartEngine();
  } finally {
    restarting.value = false;
  }
}
async function copyProblem(text: string): Promise<void> {
  toast((await copyText(text)) ? pages.home.problems.copied : pages.home.problems.copyFailed);
}

// Start never waits for a detour server; this card only invites the user to add or pick one.
const notice = computed(() => detourNotice({ engineDown: S.engineDown, serversLoaded: S.serversLoaded, servers: S.servers }));
const running = computed(() => S.state === 'connected' && noDetour(S.snapshot));
const noticeText = computed(() => detourNoticeText(notice.value, running.value));
const blockedLine = computed(() => blockedCaveat(S.engineDown, S.snapshot));
const onlyServer = computed(() => (S.servers.length === 1 ? S.servers[0]! : null));

const warnings = computed(() => (S.engineDown ? [] : (S.snapshot?.Warnings ?? [])));
const failedSwitch = computed(() => (S.state === 'connected' && !S.engineDown ? errorInfo(S.error, S.state) : null));
const showExtras = computed(() => !S.engineDown && extraRoutes.some((r) => store.routeCount(r) > 0));

const totalTraffic = computed(() => Object.values(S.snapshot?.Traffic ?? {}).reduce((a, b) => a + (b ?? 0), 0));
const savingsLine = computed(() =>
  !S.engineDown && S.snapshot && totalTraffic.value > 0 ? fill(pages.home.savings, { savings: pct(S.snapshot.Savings) }) : '',
);

// Keeps "checked N min ago" fresh.
const now = ref(Date.now());
let tick: ReturnType<typeof setInterval> | undefined;
onMounted(() => {
  tick = setInterval(() => (now.value = Date.now()), 30000);
});
onBeforeUnmount(() => clearInterval(tick));

const netLabel = computed(() => (isLearning(S.snapshot?.Network) ? pages.home.learning : fill(pages.home.networkLine, { label: S.snapshot?.Network ?? '' })));
const checkedText = computed(() => (S.checking ? actions.checking : `checked ${fmtAgo(S.snapshot?.LastCheck ?? '', now.value)}`));

function cardCount(r: Route): string {
  return plural(pages.home.cardCount, store.routeCount(r));
}
function cardTraffic(r: Route): string {
  return r === 'block' ? pages.home.cardNeverOpened : fill(pages.home.cardTraffic, { bytes: fmtBytes(store.routeTraffic(r)) });
}

async function useServer(id: string, name: string): Promise<void> {
  try {
    const r = await store.activateServer(id);
    toast(r.reloaded ? serverText.switching(name) : serverText.nowActive(name));
  } catch (e) {
    toast(serverErrorText(e as { code?: string }, 'activate'));
  }
}
</script>

<template>
  <section class="page home" aria-labelledby="home-h">
    <h1 id="home-h" class="sr-only">{{ pages.nav.home }}</h1>

    <div v-if="problems.length" class="problems" role="alert" :aria-label="pages.home.problems.label">
      <div v-for="p in problems" :key="p.key" class="banner problem">
        <span class="banner-icon" aria-hidden="true">!</span>
        <div class="grow">
          <p class="problem-text">{{ p.text }}</p>
          <details v-if="p.detail" class="problem-details">
            <summary>{{ pages.home.problems.summary }}</summary>
            <p class="small">{{ pages.home.problems.hint }}</p>
            <pre class="code" tabindex="0" :aria-label="pages.home.problems.detailsLabel">{{ p.detail }}</pre>
            <button class="btn small" type="button" @click="copyProblem(p.detail)">{{ actions.copy }}</button>
          </details>
        </div>
        <button class="btn small" type="button" :disabled="restarting" @click="restartFromProblem">{{ actions.restartEngine }}</button>
      </div>
    </div>
    <div v-if="failedSwitch" class="banner" role="alert">
      <span class="banner-icon" aria-hidden="true">!</span>
      <span class="grow">{{ failedSwitch.sentence }}</span>
      <button class="link" type="button" @click="go('settings', { tab: 'detour' })">{{ actions.openDetour }}</button>
    </div>
    <div v-if="warnings.length" class="banner" role="status">
      <span class="banner-icon" aria-hidden="true">!</span>
      <span class="grow">{{ warnings[0] }}{{ warnings.length > 1 ? ` (+${warnings.length - 1} more)` : '' }}</span>
      <button class="link" type="button" @click="go('activity', { tab: 'warnings' })">{{ actions.details }}</button>
    </div>

    <div class="setup-slot">
      <section v-if="noticeText" class="setup-card" aria-labelledby="setup-h">
        <h2 id="setup-h">{{ noticeText.title }}</h2>
        <p class="muted">{{ noticeText.text }}</p>
        <div class="row gap wrap">
          <template v-if="notice === 'pick'">
            <button v-if="onlyServer" class="btn primary" type="button" @click="useServer(onlyServer.id, onlyServer.name)">{{ actions.makeActive }}: {{ onlyServer.name }}</button>
            <button v-else class="btn primary" type="button" @click="go('settings', { tab: 'detour' })">Choose in Settings</button>
          </template>
          <button v-else class="btn primary" type="button" @click="showAddServerDialog()">{{ actions.addServer }}</button>
        </div>
      </section>
    </div>

    <div class="hero">
      <button :class="bigClass" type="button" aria-keyshortcuts="Control+Enter" :disabled="model.disabled" :aria-label="model.name" @click="store.toggleConnection()">
        <Icon name="power" class="power" />
        <span class="big-label">{{ model.label }}</span>
      </button>
      <p class="subtitle" aria-live="polite">{{ model.sub }}</p>
      <div class="sub-action">
        <button v-if="action === 'restart-engine'" class="btn primary" type="button" @click="store.restartEngine()">{{ actions.restartEngine }}</button>
        <button v-else-if="action === 'setup'" class="btn" type="button" @click="showSetupDialog(S.error?.commands)">{{ actions.showSetup }}</button>
        <button v-else-if="action === 'log'" class="btn" type="button" @click="showLogDialog()">{{ actions.showLog }}</button>
        <button v-else-if="action === 'detour'" class="btn" type="button" @click="go('settings', { tab: 'detour' })">{{ actions.openDetour }}</button>
      </div>
      <details v-if="details" class="start-details">
        <summary>{{ pages.home.startDetails.summary }}</summary>
        <p class="muted">{{ pages.home.startDetails.hint }}</p>
        <pre class="code" tabindex="0" :aria-label="pages.home.startDetails.label">{{ details }}</pre>
        <button class="btn small" type="button" @click="copyDetails">{{ actions.copy }}</button>
      </details>
    </div>

    <template v-if="!S.engineDown">
      <svg class="fan" viewBox="0 0 600 60" preserveAspectRatio="none" aria-hidden="true" focusable="false">
        <path v-for="d in fanPaths" :key="d" :d="d" fill="none" vector-effect="non-scaling-stroke" />
      </svg>
      <div class="cards">
        <button v-for="r in mainRoutes" :key="r" class="card path" type="button" :aria-label="`${routeCopy[r].long}: ${cardCount(r)}, ${cardTraffic(r)}${r === 'tunnel' && blockedLine ? `. ${blockedLine}` : ''}. Show in Sites`" @click="go('sites', { route: r })">
          <span class="card-head"><span :class="['dot', `r-${r}`]" aria-hidden="true"></span><span class="card-title">{{ routeCopy[r].long }}</span></span>
          <span class="card-line muted">{{ routeCopy[r].line }}</span>
          <span v-if="r === 'tunnel' && blockedLine" class="card-line muted card-caveat">{{ blockedLine }}</span>
          <span class="card-nums"><strong>{{ cardCount(r) }}</strong><span class="muted">{{ cardTraffic(r) }}</span></span>
        </button>
      </div>
      <div v-if="showExtras" class="cards extras">
        <button v-for="r in extraRoutes.filter((x) => store.routeCount(x) > 0)" :key="r" class="card path small" type="button" :aria-label="`${routeCopy[r].long}: ${cardCount(r)}, ${cardTraffic(r)}. Show in Sites`" @click="go('sites', { route: r })">
          <span class="card-head"><span :class="['dot', `r-${r}`]" aria-hidden="true"></span><span class="card-title">{{ routeCopy[r].long }}</span></span>
          <span class="card-nums"><strong>{{ cardCount(r) }}</strong><span class="muted">{{ cardTraffic(r) }}</span></span>
        </button>
      </div>
      <p v-if="savingsLine" class="savings">{{ savingsLine }}</p>
      <div v-if="S.snapshot" class="netline">
        <button class="link plain" type="button" :aria-label="`${netLabel}, ${checkedText}. Show network details`" @click="go('activity', { tab: 'network' })">
          <span>{{ netLabel }}</span><span class="muted"> · {{ checkedText }}</span>
        </button>
        <button class="btn small" type="button" aria-keyshortcuts="Control+R" :disabled="S.checking" @click="store.checkNow()">{{ S.checking ? actions.checking : checkAgain }}</button>
      </div>
    </template>
  </section>
</template>
