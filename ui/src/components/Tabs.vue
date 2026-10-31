<script setup lang="ts">
// Accessible tab strip (design README, Tabs). Arrow keys, Home and End move between tabs.
import { nextTick, useId } from 'vue';

export interface TabItem { id: string; label: string; count?: number | string; dot?: string }

const props = defineProps<{ items: TabItem[]; modelValue: string; label: string }>();
const emit = defineEmits<{ 'update:modelValue': [id: string] }>();
const uid = useId();
const tabId = (id: string): string => `${uid}-tab-${id}`;

async function select(id: string): Promise<void> {
  emit('update:modelValue', id);
  await nextTick();
  document.getElementById(tabId(id))?.focus();
}

function onKey(e: KeyboardEvent): void {
  const i = props.items.findIndex((t) => t.id === props.modelValue);
  if (i < 0) return;
  let j = -1;
  const n = props.items.length;
  if (e.key === 'ArrowRight') j = (i + 1) % n;
  else if (e.key === 'ArrowLeft') j = (i - 1 + n) % n;
  else if (e.key === 'Home') j = 0;
  else if (e.key === 'End') j = n - 1;
  if (j >= 0) {
    e.preventDefault();
    void select(props.items[j]!.id);
  }
}
</script>

<template>
  <div class="tabs" role="tablist" :aria-label="label" @keydown="onKey">
    <button
      v-for="t in items"
      :id="tabId(t.id)"
      :key="t.id"
      class="tab"
      role="tab"
      type="button"
      :aria-selected="t.id === modelValue"
      :tabindex="t.id === modelValue ? 0 : -1"
      @click="emit('update:modelValue', t.id)"
    >
      <span v-if="t.dot" :class="['dot', t.dot]" aria-hidden="true"></span>{{ t.label }}<span v-if="t.count !== undefined" class="count">{{ t.count }}</span>
    </button>
  </div>
</template>
