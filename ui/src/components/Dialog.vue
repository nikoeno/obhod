<script setup lang="ts">
// A modal on the native <dialog>: Esc closes, focus returns to the opener (design README, Dialog).
import { nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue';

defineProps<{ title: string }>();
const emit = defineEmits<{ close: [] }>();

const el = ref<HTMLDialogElement | null>(null);
const titleId = useId();
let opener: Element | null = null;

onMounted(async () => {
  opener = document.activeElement;
  await nextTick();
  el.value?.showModal();
});

onBeforeUnmount(() => {
  if (el.value?.open) el.value.close();
  if (opener instanceof HTMLElement && document.contains(opener)) opener.focus();
});

function onClose(): void {
  emit('close');
}
</script>

<template>
  <dialog ref="el" class="dialog" :aria-labelledby="titleId" @close="onClose">
    <h2 :id="titleId">{{ title }}</h2>
    <div class="dialog-body"><slot /></div>
    <div class="dialog-actions"><slot name="actions" /></div>
  </dialog>
</template>
