<script setup lang="ts">
// Add a detour server: paste field, optional name, Test (offline) and Add. Errors stay inline.
// The pasted text is only in the model while this dialog exists.
import { onBeforeUnmount, useId } from 'vue';
import { actions, pages } from '../../design/copy';
import type { Server } from '../backend/types';
import { createAddServerModel } from '../lib/addServerModel';
import { toast } from '../lib/toast';
import { useStore } from '../store';
import Dialog from './Dialog.vue';

const emit = defineEmits<{ close: [server: Server | null] }>();
const store = useStore();
const model = createAddServerModel(store, toast);
const t = pages.settings.detour.add;
const uid = useId();

async function add(): Promise<void> {
  const s = await model.add();
  if (s) emit('close', s);
}

onBeforeUnmount(() => model.clear());
</script>

<template>
  <Dialog :title="t.title" @close="emit('close', null)">
    <div class="form-row">
      <label :for="`${uid}-input`">{{ t.inputLabel }}</label>
      <textarea
        :id="`${uid}-input`"
        v-model="model.input"
        class="field mono"
        rows="5"
        autocomplete="off"
        autocapitalize="off"
        spellcheck="false"
        aria-required="true"
        :aria-invalid="model.msg.kind === 'err' ? 'true' : undefined"
        :aria-describedby="`${uid}-help ${uid}-msg`"
        @input="model.edited()"
      ></textarea>
      <p :id="`${uid}-help`" class="muted small">{{ t.inputHelp }}</p>
    </div>
    <div class="form-row">
      <label :for="`${uid}-name`">{{ t.nameLabel }}</label>
      <input :id="`${uid}-name`" v-model="model.name" type="text" class="field" maxlength="60" autocomplete="off" :aria-describedby="`${uid}-name-help`" />
      <p :id="`${uid}-name-help`" class="muted small">{{ t.nameHelp }}</p>
    </div>
    <p :id="`${uid}-msg`" :class="['form-msg', model.msg.kind]" role="status" aria-live="polite">{{ model.msg.text }}</p>
    <template #actions>
      <button class="btn" type="button" :disabled="model.busy" @click="model.test()">{{ actions.test }}</button>
      <button class="btn primary" type="button" :disabled="model.busy" @click="add">{{ actions.addServer }}</button>
      <button class="btn" type="button" @click="emit('close', null)">{{ actions.cancel }}</button>
    </template>
  </Dialog>
</template>
