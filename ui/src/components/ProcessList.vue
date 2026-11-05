<script setup lang="ts">
// The engine's processes and their states, from the snapshot (it is the same list as GET /processes).
import { computed } from 'vue';
import { processRows } from '../lib/helpModel';
import { useStore } from '../store';

const store = useStore();
const rows = computed(() => (store.S.engineDown ? [] : processRows(store.S.snapshot?.Processes)));
</script>

<template>
  <p v-if="!rows.length" class="muted">{{ store.S.engineDown ? 'The engine is not running.' : 'No process information yet.' }}</p>
  <ul v-else class="procs" aria-label="Engine processes">
    <li v-for="p in rows" :key="p.name">
      <span class="grow"><strong>{{ p.label }}</strong> <code v-if="p.label !== p.name" class="tech">{{ p.name }}</code></span>
      <span :class="['pill', p.tone]">{{ p.state }}</span>
      <span v-if="p.restarts" class="muted small">{{ p.restarts }}</span>
    </li>
  </ul>
</template>
