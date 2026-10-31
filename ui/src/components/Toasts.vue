<script setup lang="ts">
import { toasts } from '../lib/toast';
</script>

<template>
  <div class="toasts" role="status" aria-live="polite">
    <div v-for="t in toasts" :key="t.id" :class="['toast', { out: t.out }]">{{ t.text }}</div>
  </div>
</template>
