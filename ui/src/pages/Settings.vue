<script setup lang="ts">
// Settings (docs/UI.md 5.4). General and Advanced settings live in localStorage only for now
// (writing app.yaml from the UI is an open question, docs/UI.md section 12).
import { computed, onMounted, reactive, ref, useId, watch } from 'vue';
import { actions, pages } from '../../design/copy';
import type { Server, Version } from '../backend/types';
import Tabs from '../components/Tabs.vue';
import { choices, desyncErrorText, panelState, selectedId, siteCountText, siteCounts, statusView, outcomeText, triedLines } from '../lib/desyncModel';
import { problemViews } from '../lib/problems';
import { loadSetting, saveSetting } from '../lib/settings';
import { hostPort, MAX_SERVERS, protocolName, serverErrorText, serverNote, serversLocked, serverText } from '../lib/serverText';
import { toast } from '../lib/toast';
import { route } from '../router';
import { useStore } from '../store';
import { confirmRemoveServer, showAddServerDialog } from '../ui';

const store = useStore();
const S = store.S;
const t = pages.settings;
const uid = useId();

const TAB_IDS = ['general', 'detour', 'desync', 'advanced', 'about'] as const;
type TabId = (typeof TAB_IDS)[number];
const tabs = TAB_IDS.map((id) => ({ id, label: t.tabs[id] }));
const tab = ref<TabId>((TAB_IDS as readonly string[]).includes(route.params.tab ?? '') ? (route.params.tab as TabId) : 'general');
watch(
  () => route.params.tab,
  (v) => {
    if (v && (TAB_IDS as readonly string[]).includes(v)) tab.value = v as TabId;
  },
);

// ---------- general ----------
const general = reactive({
  startOnSignIn: loadSetting('startOnSignIn', true),
  autoConnect: loadSetting('autoConnect', false),
  closeToTray: loadSetting('closeToTray', true),
  notifications: loadSetting('notifications', true),
  language: loadSetting('language', 'en'),
});
function persist(key: keyof typeof general): void {
  saveSetting(key, general[key]);
}

// ---------- detour server ----------
const revealed = reactive(new Set<string>());
const pendingId = ref<string | null>(null);
const list = computed(() => S.servers);
const active = computed(() => list.value.find((x) => x.active));
const locked = computed(() => serversLocked(S.engineDown, S.state));
const full = computed(() => list.value.length >= MAX_SERVERS);
const note = computed(() => serverNote({ engineDown: S.engineDown, state: S.state, count: list.value.length }));

function toggleReveal(id: string): void {
  if (revealed.has(id)) revealed.delete(id);
  else revealed.add(id);
}

async function makeActive(x: Server): Promise<void> {
  pendingId.value = x.id;
  try {
    const r = await store.activateServer(x.id);
    toast(r.reloaded ? serverText.switching(x.name) : serverText.nowActive(x.name));
  } catch (e) {
    toast(serverErrorText(e as { code?: string }, 'activate'));
  } finally {
    pendingId.value = null;
  }
}

function focusId(id: string): void {
  setTimeout(() => document.getElementById(id)?.focus(), 0);
}
const onAdded = (s: Server): void => focusId(`${uid}-use-${s.id}`);

// ---------- picky sites (desync) ----------
const td = t.desync;
const dPanel = computed(() => panelState({ engineDown: S.engineDown, loaded: S.desyncLoaded, failed: S.desyncFailed }));
const dChoices = computed(() => choices(S.desyncPresets, S.desync));
const dSelected = computed(() => selectedId(S.desync));
const dAuto = computed(() => S.desync?.mode === 'auto');
const dCounts = computed(() => siteCounts(S.routes));
const dStatus = computed(() =>
  statusView({ info: S.desync, presets: S.desyncPresets, progress: S.desyncProgress, engineDown: S.engineDown, state: S.state, network: S.snapshot?.Network ?? '', routed: dCounts.value.routed }),
);
const dSites = computed(() => siteCountText(dCounts.value));
const dLast = computed(() => S.desync?.lastSearch ?? null);
const dOutcome = computed(() => (dLast.value ? outcomeText(dLast.value, S.desyncPresets) : ''));
const dTried = computed(() => (dLast.value ? triedLines(dLast.value, S.desyncPresets) : []));
const dProblems = computed(() => problemViews(S.problems).filter((p) => p.code === 'desync_failed'));
const dBusy = ref<string | null>(null);
const dSearchStarting = ref(false);
const dMessage = ref('');

async function chooseDesync(id: string): Promise<void> {
  if (id === dSelected.value || dBusy.value) return;
  dBusy.value = id;
  dMessage.value = '';
  try {
    await store.setDesync(id);
    toast(S.state === 'connected' ? td.applied : td.appliedOff);
  } catch (e) {
    dMessage.value = desyncErrorText(e as { code?: string });
    toast(dMessage.value);
  } finally {
    dBusy.value = null;
  }
}

async function searchAgain(): Promise<void> {
  dSearchStarting.value = true;
  dMessage.value = '';
  try {
    await store.searchDesync();
    toast(td.searchStarted);
  } catch (e) {
    dMessage.value = desyncErrorText(e as { code?: string });
    toast(dMessage.value);
  } finally {
    dSearchStarting.value = false;
  }
}

watch(
  tab,
  (v) => {
    if (v === 'desync' && !S.engineDown && !S.desyncLoaded) void store.refreshDesync();
  },
  { immediate: true },
);

// ---------- advanced ----------
const advanced = reactive({
  blockQuic: loadSetting('blockQuic', true),
  allowOtherQueryTypes: loadSetting('allowOtherQueryTypes', false),
  logLevel: loadSetting('logLevel', 'info'),
});
const restartNeeded = ref(false);
function advancedChanged(key: keyof typeof advanced): void {
  saveSetting(key, advanced[key]);
  restartNeeded.value = true;
}
async function restartNow(): Promise<void> {
  try {
    if (S.state === 'connected') {
      toast('Restarting…');
      await store.restartConnection();
    } else {
      toast('Saved. It applies the next time you press Start.');
    }
    restartNeeded.value = false;
  } catch (e) {
    toast(e instanceof Error && e.message ? e.message : 'Something went wrong.');
  }
}

// ---------- about ----------
const version = ref<Version | null>(null);
const versionError = ref('');
const uiVersion = ref('');
onMounted(async () => {
  try {
    uiVersion.value = await store.backend.uiVersion();
  } catch {
    uiVersion.value = __UI_VERSION__;
  }
});
watch(
  tab,
  async (v) => {
    if (v !== 'about') return;
    versionError.value = '';
    try {
      version.value = await store.backend.getVersion();
    } catch (e) {
      versionError.value = e instanceof Error && e.message ? e.message : 'Something went wrong.';
    }
  },
  { immediate: true },
);
</script>

<template>
  <section class="page settings" aria-labelledby="set-h">
    <h1 id="set-h">{{ pages.settings.title }}</h1>
    <Tabs v-model="tab" :items="tabs" label="Settings sections" />

    <div class="tabpanel" role="tabpanel" tabindex="-1">
      <!-- General -->
      <template v-if="tab === 'general'">
        <div class="row setting">
          <div class="grow"><label :for="`${uid}-signin`">{{ t.general.autostart }}</label></div>
          <input :id="`${uid}-signin`" v-model="general.startOnSignIn" type="checkbox" role="switch" class="switch" @change="persist('startOnSignIn')" />
        </div>
        <div class="row setting">
          <div class="grow"><label :for="`${uid}-auto`">{{ t.general.autoconnect }}</label></div>
          <input :id="`${uid}-auto`" v-model="general.autoConnect" type="checkbox" role="switch" class="switch" @change="persist('autoConnect')" />
        </div>
        <div class="row setting">
          <div class="grow">
            <label :for="`${uid}-tray`">{{ t.general.hideToTray.label }}</label>
            <p class="muted small">{{ t.general.hideToTray.hint }}</p>
          </div>
          <input :id="`${uid}-tray`" v-model="general.closeToTray" type="checkbox" role="switch" class="switch" @change="persist('closeToTray')" />
        </div>
        <div class="row setting">
          <div class="grow">
            <label :for="`${uid}-notify`">{{ t.general.notifications.label }}</label>
            <p class="muted small">{{ t.general.notifications.hint }}</p>
          </div>
          <input :id="`${uid}-notify`" v-model="general.notifications" type="checkbox" role="switch" class="switch" @change="persist('notifications')" />
        </div>
        <div class="row setting">
          <label class="grow" :for="`${uid}-lang`">{{ t.general.language }}</label>
          <select :id="`${uid}-lang`" v-model="general.language" @change="persist('language')">
            <option value="en">English</option>
            <option value="ru" disabled>Русский (later)</option>
          </select>
        </div>
      </template>

      <!-- Detour server -->
      <template v-else-if="tab === 'detour'">
        <div class="row setting">
          <div class="grow">
            <strong>{{ t.tabs.detour }}</strong>
            <p class="muted small">{{ t.detour.lead }}</p>
          </div>
          <span v-if="S.serversLoaded" :class="['pill', active ? 'ok' : 'warn']">{{ active ? t.detour.ready : t.detour.missing }}</span>
        </div>

        <p v-if="!S.serversLoaded" class="empty">{{ S.engineDown ? 'The engine is not running.' : actions.loading }}</p>
        <p v-else-if="!list.length" class="empty">{{ t.detour.none }}</p>
        <ul v-else class="servers" aria-label="Detour servers">
          <li v-for="x in list" :key="x.id" class="server">
            <div class="server-main">
              <div class="server-name">
                <span>{{ x.name }}</span>
                <span v-if="x.active" class="pill ok">{{ t.detour.active }}</span>
              </div>
              <div class="muted small">
                {{ protocolName(x.protocol) }} ·
                <span v-if="revealed.has(x.id)" class="tech">{{ hostPort(x) }}</span>
                <template v-else>{{ t.detour.addressHidden }}</template>
              </div>
            </div>
            <div class="server-actions">
              <button
                class="link"
                type="button"
                :aria-expanded="revealed.has(x.id)"
                :aria-label="`${revealed.has(x.id) ? t.detour.hideAddress : t.detour.showAddress} of ${x.name}`"
                @click="toggleReveal(x.id)"
              >{{ revealed.has(x.id) ? t.detour.hideAddress : t.detour.showAddress }}</button>
              <button
                v-if="!x.active"
                :id="`${uid}-use-${x.id}`"
                class="btn small"
                type="button"
                :aria-label="`${actions.makeActive}: ${x.name}`"
                :disabled="locked || pendingId === x.id"
                @click="makeActive(x)"
              >{{ pendingId === x.id ? 'Switching…' : actions.makeActive }}</button>
              <button
                class="btn small danger"
                type="button"
                :aria-label="`${actions.remove}: ${x.name}`"
                :disabled="locked"
                @click="confirmRemoveServer(x, () => focusId(`${uid}-add`))"
              >{{ actions.remove }}</button>
            </div>
          </li>
        </ul>

        <div class="row gap wrap">
          <button :id="`${uid}-add`" class="btn primary" type="button" :disabled="locked || full || !S.serversLoaded" :aria-describedby="`${uid}-note`" @click="showAddServerDialog(onAdded)">{{ actions.addServer }}</button>
          <span class="muted small">{{ serverText.count(list.length) }}</span>
        </div>
        <p :id="`${uid}-note`" class="muted small" aria-live="polite">{{ note }}</p>

        <div class="row setting">
          <div class="grow">
            <strong>{{ t.detour.proxy }}</strong>
            <p class="muted small">{{ t.detour.proxyHint }}</p>
          </div>
          <span class="later">{{ actions.later }}</span>
        </div>
      </template>

      <!-- Picky sites (desync) -->
      <template v-else-if="tab === 'desync'">
        <div class="row setting">
          <div class="grow">
            <strong>{{ t.tabs.desync }}</strong>
            <p class="muted small">{{ td.lead }}</p>
          </div>
        </div>

        <div v-for="p in dProblems" :key="p.key" class="banner problem" role="alert">
          <span class="banner-icon" aria-hidden="true">!</span>
          <span class="grow">{{ p.text }}</span>
        </div>

        <p v-if="dPanel === 'engine-down'" class="empty">{{ td.engineDown }}</p>
        <p v-else-if="dPanel === 'loading'" class="empty">{{ td.loading }}</p>
        <div v-else-if="dPanel === 'failed'" class="empty">
          <p>{{ td.loadFailed }}</p>
          <button class="btn small" type="button" @click="store.refreshDesync()">{{ td.retry }}</button>
        </div>
        <template v-else>
          <div class="presets" role="radiogroup" :aria-label="td.groupLabel" :aria-busy="!!dBusy">
            <label v-for="c in dChoices" :key="c.id" :class="['preset', { on: dSelected === c.id }]">
              <input type="radio" name="desync-preset" :value="c.id" :checked="dSelected === c.id" :disabled="!!dBusy || c.kind === 'custom'" @change="chooseDesync(c.id)" />
              <span class="preset-text">
                <span class="preset-name">{{ c.name }}<span v-if="dBusy === c.id" class="muted small"> · {{ td.saving }}</span></span>
                <span class="muted small">{{ c.description }}</span>
              </span>
            </label>
          </div>
          <p v-if="dMessage" class="small" role="alert">{{ dMessage }}</p>

          <section v-if="dAuto" class="auto-status" aria-labelledby="auto-h" aria-live="polite">
            <h2 id="auto-h" class="h3">{{ td.status.title }}</h2>
            <p>{{ dStatus.line }}</p>
            <progress v-if="dStatus.fraction !== null" class="bar" max="1" :value="dStatus.fraction" :aria-label="dStatus.line"></progress>
            <p v-if="dSites" class="muted small">{{ dSites }}</p>
            <template v-if="dLast && !dStatus.fraction">
              <p v-if="dOutcome" class="small">{{ dOutcome }}</p>
              <ul v-if="dTried.length" class="plainlist small muted" :aria-label="td.status.lastTitle">
                <li v-for="x in dTried" :key="x.key" :class="{ chosen: x.chosen }">{{ x.text }}</li>
              </ul>
            </template>
            <div class="row gap wrap">
              <button class="btn" type="button" :disabled="!dStatus.canSearch || dSearchStarting" :aria-describedby="`${uid}-why`" @click="searchAgain">{{ td.searchAgain }}</button>
              <span :id="`${uid}-why`" class="muted small">{{ dStatus.why }}</span>
            </div>
          </section>
        </template>
      </template>

      <!-- Advanced -->
      <template v-else-if="tab === 'advanced'">
        <div v-if="restartNeeded" class="banner" role="status">
          <span class="banner-icon" aria-hidden="true">!</span>
          <span class="grow">{{ t.advanced.restartNeeded }}</span>
          <button class="btn primary small" type="button" @click="restartNow">{{ actions.restartNow }}</button>
        </div>
        <div class="row setting">
          <div class="grow">
            <label :for="`${uid}-quic`">{{ t.advanced.blockQuic.label }}</label>
            <p class="muted small">{{ t.advanced.blockQuic.hint }}</p>
          </div>
          <input :id="`${uid}-quic`" v-model="advanced.blockQuic" type="checkbox" role="switch" class="switch" @change="advancedChanged('blockQuic')" />
        </div>
        <div class="row setting">
          <div class="grow">
            <label :for="`${uid}-dnsq`">{{ t.advanced.otherDns.label }}</label>
            <p class="muted small">{{ t.advanced.otherDns.hint }}</p>
          </div>
          <input :id="`${uid}-dnsq`" v-model="advanced.allowOtherQueryTypes" type="checkbox" role="switch" class="switch" @change="advancedChanged('allowOtherQueryTypes')" />
        </div>
        <div class="row setting">
          <label class="grow" :for="`${uid}-level`">{{ t.advanced.logLevel }}</label>
          <select :id="`${uid}-level`" v-model="advanced.logLevel" @change="advancedChanged('logLevel')">
            <option v-for="v in ['warn', 'info', 'debug']" :key="v" :value="v">{{ v }}</option>
          </select>
        </div>
        <div class="row setting">
          <div class="grow">
            <strong>{{ actions.restartEngine }}</strong>
            <p class="muted small">Starts the background engine again.</p>
          </div>
          <button class="btn" type="button" @click="store.restartEngine()">{{ actions.restartEngine }}</button>
        </div>
      </template>

      <!-- About -->
      <template v-else>
        <dl class="facts wide">
          <dt>{{ t.about.app }}</dt>
          <dd>{{ uiVersion }}</dd>
          <template v-if="version">
            <dt>{{ t.about.engine }}</dt>
            <dd>{{ version.splitter }}</dd>
            <dt>sing-box</dt>
            <dd>{{ version.singBox || 'not reported' }}</dd>
            <dt>Desync engine</dt>
            <dd>{{ version.desync || 'not reported' }}</dd>
          </template>
          <template v-else-if="versionError">
            <dt>Version</dt>
            <dd>{{ versionError }}</dd>
          </template>
          <template v-else>
            <dt>{{ actions.loading }}</dt>
            <dd></dd>
          </template>
        </dl>
        <h2 class="h3">{{ t.about.bundled }}</h2>
        <ul class="plainlist">
          <li>sing-box: GPL-3.0-or-later</li>
          <li>zapret2 (desync engine): MIT</li>
          <li>WinDivert (Windows): LGPL-3.0 / GPL-2.0</li>
          <li>cygwin1.dll (Windows): LGPL</li>
        </ul>
        <button class="btn" type="button" @click="toast('Not available yet. The release page will open in your browser.')">{{ actions.checkUpdates }}</button>
        <p class="muted small">{{ t.about.noAutoUpdate }}</p>
      </template>
    </div>

    <div class="quit-row">
      <button class="btn danger" type="button" @click="store.quit()">{{ actions.quit }}</button>
      <span class="muted small">{{ t.quitHint }}</span>
    </div>
  </section>
</template>
