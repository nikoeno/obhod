<script setup lang="ts">
// Help and diagnostics (docs/UI.md 5.5): how it works, engine processes, logs, versions, links to settings,
// recovery steps. No diagnostics export: it is not designed yet (docs/UI.md section 12).
import { computed, onMounted, ref, watch } from 'vue';
import { actions, mainRoutes, pages, routes as routeCopy } from '../../design/copy';
import type { Version } from '../backend/types';
import LogViewer from '../components/LogViewer.vue';
import ProcessList from '../components/ProcessList.vue';
import { recoveryLead, recoverySteps, settingsLinks, versionRows } from '../lib/helpModel';
import { go } from '../router';
import { useStore } from '../store';

const store = useStore();
const S = store.S;
const t = pages.help;

const showLog = ref(false);

const version = ref<Version | null>(null);
const versionError = ref('');
const uiVersion = ref('');
const rows = computed(() => versionRows(uiVersion.value, S.engineDown ? null : version.value));

async function loadVersion(): Promise<void> {
  versionError.value = '';
  try {
    version.value = await store.backend.getVersion();
  } catch (e) {
    version.value = null;
    versionError.value = e instanceof Error && e.message ? e.message : 'Something went wrong.';
  }
}

onMounted(async () => {
  try {
    uiVersion.value = await store.backend.uiVersion();
  } catch {
    uiVersion.value = __UI_VERSION__;
  }
  if (!S.engineDown) void loadVersion();
});
// The engine came back (or was restarted): read the versions again.
watch(
  () => S.engineDown,
  (down) => {
    if (!down) void loadVersion();
  },
);
</script>

<template>
  <section class="page help" aria-labelledby="help-h">
    <h1 id="help-h">{{ t.title }}</h1>

    <h2>{{ t.howItWorks }}</h2>
    <p class="muted">{{ t.howItWorksText }}</p>
    <div class="cards">
      <div v-for="r in mainRoutes" :key="r" class="card path static">
        <span class="card-head"><span :class="['dot', `r-${r}`]" aria-hidden="true"></span><span class="card-title">{{ routeCopy[r].long }}</span></span>
        <span class="card-line muted">{{ routeCopy[r].line }}</span>
      </div>
    </div>

    <h2>Engine processes</h2>
    <ProcessList />

    <h2>{{ t.logs }}</h2>
    <div class="row gap wrap">
      <button class="btn" type="button" :aria-expanded="showLog" aria-controls="help-log" :disabled="S.engineDown" @click="showLog = !showLog">{{ showLog ? 'Hide engine log' : actions.showLog }}</button>
      <button class="btn" type="button" disabled aria-describedby="logs-later">{{ actions.openLogs }}</button>
      <span id="logs-later" class="later">{{ actions.later }}</span>
    </div>
    <div v-if="showLog && !S.engineDown" id="help-log"><LogViewer initial="splitter" /></div>

    <h2>Versions</h2>
    <dl class="facts wide">
      <template v-for="r in rows" :key="r.term">
        <dt>{{ r.term }}</dt>
        <dd>{{ r.value }}</dd>
      </template>
    </dl>
    <p v-if="versionError && !S.engineDown" class="muted small">{{ versionError }}</p>
    <p v-if="S.engineDown" class="muted small">The engine is not running, so its versions are not available.</p>

    <h2>Settings</h2>
    <div class="links">
      <button v-for="l in settingsLinks" :key="l.tab" class="btn" type="button" @click="go('settings', { tab: l.tab })">{{ l.label }}</button>
    </div>

    <h2>{{ t.recovery }}</h2>
    <p class="muted">{{ recoveryLead }}</p>
    <ol class="steps">
      <li v-for="(s, i) in recoverySteps" :key="i">
        <strong v-if="s.title">{{ s.title }}</strong>{{ s.text }}
        <pre v-if="s.code" class="code">{{ s.code }}</pre>
      </li>
    </ol>
  </section>
</template>
