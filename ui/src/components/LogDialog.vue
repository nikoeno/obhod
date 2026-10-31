<script setup lang="ts">
// Read-only engine log viewer with a tab per process and Copy (docs/UI.md 5.5).
import { computed, onMounted, ref, watch } from 'vue';
import { actions, pages, processes } from '../../design/copy';
import type { LogName } from '../backend/types';
import { copyText } from '../lib/clipboard';
import { toast } from '../lib/toast';
import { useStore } from '../store';
import Dialog from './Dialog.vue';
import Tabs from './Tabs.vue';

const props = defineProps<{ initial: LogName }>();
const emit = defineEmits<{ close: [] }>();
const store = useStore();

const active = ref<LogName>(props.initial);
const lines = ref<string[]>([]);
const status = ref('');
const items = (Object.keys(processes) as LogName[]).map((id) => ({ id, label: processes[id] }));

const text = computed(() => (lines.value.length ? lines.value.join('\n') : '(empty)'));

async function load(): Promise<void> {
  status.value = actions.loading;
  try {
    const r = await store.backend.getLogs(active.value);
    lines.value = r.lines;
    status.value = `${r.lines.length} line${r.lines.length === 1 ? '' : 's'}`;
  } catch (e) {
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
  <Dialog :title="pages.help.logs" @close="emit('close')">
    <div class="logviewer">
      <Tabs v-model="active" :items="items" label="Engine processes" />
      <pre class="code log" tabindex="0" role="tabpanel" aria-label="Engine log">{{ text }}</pre>
      <div class="row">
        <p class="muted small grow" aria-live="polite">{{ status }}</p>
        <button class="btn small" type="button" @click="load">{{ actions.refresh }}</button>
        <button class="btn small" type="button" @click="copy">{{ actions.copy }}</button>
      </div>
    </div>
    <template #actions>
      <button class="btn primary" type="button" @click="emit('close')">{{ actions.close }}</button>
    </template>
  </Dialog>
</template>
