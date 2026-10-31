<script setup lang="ts">
// Remove with a confirmation. The active server cannot be removed while connected: the dialog says so.
import { actions, pages } from '../../design/copy';
import type { Server } from '../backend/types';
import { removeNeedsStop, serverErrorText, serverText } from '../lib/serverText';
import { toast } from '../lib/toast';
import { useStore } from '../store';
import Dialog from './Dialog.vue';

const props = defineProps<{ server: Server }>();
const emit = defineEmits<{ close: [removed: boolean] }>();
const store = useStore();
const t = pages.settings.detour.remove;

const mustStop = removeNeedsStop(props.server, store.S.state);

async function remove(): Promise<void> {
  try {
    await store.removeServer(props.server.id);
    toast(serverText.removed(props.server.name));
    emit('close', true);
  } catch (e) {
    toast(serverErrorText(e as { code?: string; message?: string }, 'remove'));
    emit('close', false);
  }
}
</script>

<template>
  <Dialog :title="mustStop ? t.activeTitle : serverText.removeTitle(server.name)" @close="emit('close', false)">
    <p>{{ mustStop ? t.activeBody : t.body }}</p>
    <template #actions>
      <template v-if="mustStop">
        <button class="btn primary" type="button" @click="emit('close', false)">{{ actions.close }}</button>
      </template>
      <template v-else>
        <button class="btn" type="button" @click="emit('close', false)">{{ actions.cancel }}</button>
        <button class="btn danger" type="button" @click="remove">{{ actions.remove }}</button>
      </template>
    </template>
  </Dialog>
</template>
