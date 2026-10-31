<script setup lang="ts">
import { icons, type IconName } from '../lib/icons';

defineProps<{ name: IconName }>();
</script>

<template>
  <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" :stroke-width="icons[name].stroke" stroke-linecap="round" stroke-linejoin="round">
    <component :is="el.tag" v-for="(el, i) in icons[name].els" :key="i" v-bind="el.attrs" />
  </svg>
</template>
