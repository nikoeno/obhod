<script setup lang="ts">
// The engine log in a dialog (opened from error actions, docs/UI.md 5.5).
import { actions, pages } from '../../design/copy';
import type { LogName } from '../backend/types';
import Dialog from './Dialog.vue';
import LogViewer from './LogViewer.vue';

defineProps<{ initial: LogName }>();
const emit = defineEmits<{ close: [] }>();
</script>

<template>
  <Dialog :title="pages.help.logs" @close="emit('close')">
    <LogViewer :initial="initial" />
    <template #actions>
      <button class="btn primary" type="button" @click="emit('close')">{{ actions.close }}</button>
    </template>
  </Dialog>
</template>
