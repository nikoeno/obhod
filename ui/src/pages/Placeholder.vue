<script setup lang="ts">
// Stage A placeholder for pages that are ported in Stage B (Sites, Activity, Help).
defineProps<{ title: string; lead: string }>();
</script>

<template>
  <section class="page" aria-labelledby="ph-h">
    <h1 id="ph-h">{{ title }}</h1>
    <p class="muted lead">{{ lead }}</p>
    <p class="empty">This page is coming soon.</p>
  </section>
</template>
