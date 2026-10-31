<script setup lang="ts">
// DEV ONLY (Ctrl+Shift+D): scenario and data switches for the fake backend. Not in production builds.
import { onBeforeUnmount, onMounted, reactive, ref } from 'vue';
import type { DevFlag, Scenario } from '../backend/types';
import { useStore } from '../store';
import css from './devpanel.css?inline';

const store = useStore();
const dev = store.backend.dev;
const open = ref(false);

const SCENARIOS: Array<[Scenario, string]> = [
  ['normal', 'Normal'],
  ['no-server', 'No detour server (list is empty, Start fails)'],
  ['permission-denied', 'Permission denied on Start'],
  ['needs-setup', 'Linux: one-time setup needed'],
  ['crash', 'Connection drops after a few seconds'],
  ['engine-unreachable', 'Engine not running'],
];
const FLAGS: Array<[DevFlag, string]> = [
  ['warnings', 'Warnings'],
  ['extras', 'Proxy and blocked-by-you sites'],
  ['learning', 'Still learning (no network yet)'],
  ['reloadFails', 'Switching server while On fails'],
];

const scenario = ref<Scenario>(dev?.scenario() ?? 'normal');
const flags = reactive<Record<DevFlag, boolean>>(dev?.flags() ?? { warnings: false, extras: false, learning: false, reloadFails: false });

function pick(s: Scenario): void {
  scenario.value = s;
  dev?.setScenario(s);
  if (s === 'engine-unreachable') return;
  void store.refresh();
}
function flag(k: DevFlag, v: boolean): void {
  flags[k] = v;
  dev?.setFlag(k, v);
}

function onKey(e: KeyboardEvent): void {
  if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.key.toLowerCase() === 'd') {
    e.preventDefault();
    open.value = !open.value;
  } else if (e.key === 'Escape' && open.value && !document.querySelector('dialog[open]')) {
    open.value = false;
  }
}
// Constructable stylesheet: the CSS stays inside this dev-only module (it is not in the production CSS bundle).
const sheet = new CSSStyleSheet();
sheet.replaceSync(css);
onMounted(() => {
  document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  document.addEventListener('keydown', onKey);
});
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKey);
  document.adoptedStyleSheets = document.adoptedStyleSheets.filter((x) => x !== sheet);
});
</script>

<template>
  <aside v-if="dev && open" class="devpanel" aria-label="Fake backend controls">
    <div class="row"><strong class="grow">Fake backend (dev only)</strong>
      <button class="icon-btn" type="button" aria-label="Close controls" @click="open = false">×</button>
    </div>
    <fieldset>
      <legend>Scenario</legend>
      <label v-for="[id, label] in SCENARIOS" :key="id" class="dev-opt">
        <input type="radio" name="dev-scenario" :value="id" :checked="scenario === id" @change="pick(id)" />{{ label }}
      </label>
    </fieldset>
    <fieldset>
      <legend>Data</legend>
      <label v-for="[k, label] in FLAGS" :key="k" class="dev-opt">
        <input type="checkbox" :checked="flags[k]" @change="flag(k, ($event.target as HTMLInputElement).checked)" />{{ label }}
      </label>
    </fieldset>
    <p class="muted small">Ctrl+Shift+D toggles this panel.</p>
  </aside>
</template>
