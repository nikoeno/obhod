<script setup lang="ts">
// Read-only engine log viewer with a tab per process, Refresh and Copy (docs/UI.md 5.5).
// Used inside the log dialog and inline on Help.
import { computed, onMounted, ref, watch } from 'vue';
import { actions, processes } from '../../design/copy';
import type { LogName } from '../backend/types';
import { copyText } from '../lib/clipboard';
import { toast } from '../lib/toast';
import { useStore } from '../store';
import Tabs from './Tabs.vue';

const props = defineProps<{ initial: LogName }>();
const store = useStore();

const active = ref<LogName>(props.initial);
const lines = ref<string[]>([]);
const status = ref('');
const items = (Object.keys(processes) as LogName[]).map((id) => ({ id, label: processes[id] }));

const text = computed(() => (lines.value.length ? lines.value.join('\n') : '(empty)'));

async function load(): Promise<void> {
  const name = active.value;
  status.value = actions.loading;
  try {
    const r = await store.backend.getLogs(name);
    if (name !== active.value) return; // a newer tab was picked meanwhile
    lines.value = r.lines;
    status.value = `${r.lines.length} line${r.lines.length === 1 ? '' : 's'}`;
  } catch (e) {
    if (name !== active.value) return;
    lines.value = [];
    status.value = e instanceof Error && e.message ? e.message : 'Something went wrong.';
  }
}

async function copy(): Promise<void> {
  toast((await copyText(lines.value.join('\n'))) ? 'Log copied.' : 'Could not copy.');
}

onMounted(load);
watch(active, load);
</script>

<template>
  <div class="logviewer">
    <Tabs v-model="active" :items="items" label="Engine processes" />
    <pre class="code log" tabindex="0" role="tabpanel" aria-label="Engine log">{{ text }}</pre>
    <div class="row">
      <p class="muted small grow" aria-live="polite">{{ status }}</p>
      <button class="btn small" type="button" @click="load">{{ actions.refresh }}</button>
      <button class="btn small" type="button" @click="copy">{{ actions.copy }}</button>
    </div>
  </div>
</template>
