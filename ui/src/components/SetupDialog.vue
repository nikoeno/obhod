<script setup lang="ts">
import { pages, actions, errors } from '../../design/copy';
import { copyText } from '../lib/clipboard';
import { toast } from '../lib/toast';
import Dialog from './Dialog.vue';

const props = defineProps<{ commands: string[] }>();
const emit = defineEmits<{ close: [] }>();

async function copy(): Promise<void> {
  toast((await copyText(props.commands.join('\n'))) ? 'Commands copied.' : 'Could not copy. Select the text instead.');
}
</script>

<template>
  <Dialog :title="errors.needs_setup!.sentence" @close="emit('close')">
    <p>{{ pages.help.setupDialog }}</p>
    <pre class="code" tabindex="0" aria-label="Setup commands">{{ commands.length ? commands.join('\n') : '(no commands were given)' }}</pre>
    <template #actions>
      <button class="btn primary" type="button" @click="copy">{{ actions.copyCommands }}</button>
      <button class="btn" type="button" @click="emit('close')">{{ actions.close }}</button>
    </template>
  </Dialog>
</template>
