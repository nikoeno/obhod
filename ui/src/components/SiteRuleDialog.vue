<script setup lang="ts">
// Add a site (domain field + path) or "Always use…" for one site (name fixed, path only).
// Errors stay inline; success closes the dialog with the saved rule and a toast.
import { onMounted, ref, useId } from 'vue';
import { actions, pages } from '../../design/copy';
import type { SiteAction, SiteRule } from '../backend/types';
import { createSiteRuleModel, ruleText, siteChoices } from '../lib/siteRules';
import { toast } from '../lib/toast';
import { useStore } from '../store';
import Dialog from './Dialog.vue';

const props = defineProps<{ domain?: string; current?: SiteAction }>();
const emit = defineEmits<{ close: [rule: SiteRule | null] }>();
const store = useStore();
const t = pages.sites.rule;
const model = createSiteRuleModel(store, toast, { fixedDomain: props.domain, action: props.current });
const uid = useId();
const input = ref<HTMLInputElement | null>(null);

onMounted(() => {
  // Dialog.vue opens the modal on the next tick; focus the field after that.
  setTimeout(() => input.value?.focus(), 0);
});

async function submit(): Promise<void> {
  const rule = await model.submit();
  if (rule) emit('close', rule);
}
</script>

<template>
  <Dialog :title="domain ? ruleText.title(domain) : t.addTitle" @close="emit('close', null)">
    <form :id="`${uid}-form`" @submit.prevent="submit">
      <div v-if="!domain" class="form-row">
        <label :for="`${uid}-site`">{{ t.siteLabel }}</label>
        <input
          :id="`${uid}-site`"
          ref="input"
          v-model="model.domain"
          type="text"
          class="field mono"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          aria-required="true"
          :aria-invalid="model.msg.kind === 'err' ? 'true' : undefined"
          :aria-describedby="`${uid}-help ${uid}-msg`"
          @input="model.edited()"
        />
        <p :id="`${uid}-help`" class="muted small">{{ t.siteHelp }}</p>
      </div>
      <fieldset class="choices" :aria-describedby="`${uid}-msg`">
        <legend>{{ t.pathLabel }}</legend>
        <label v-for="c in siteChoices" :key="c.action" class="choice">
          <input v-model="model.action" type="radio" :name="`${uid}-path`" :value="c.action" @change="model.edited()" />
          <span><span :class="['dot', c.dot]" aria-hidden="true"></span><strong>{{ c.word }}</strong> <span class="muted">{{ c.line }}</span></span>
        </label>
      </fieldset>
      <p :id="`${uid}-msg`" :class="['form-msg', model.msg.kind]" role="status" aria-live="polite">{{ model.msg.text }}</p>
    </form>
    <template #actions>
      <button class="btn primary" type="submit" :form="`${uid}-form`" :disabled="model.busy">{{ domain ? t.save : t.add }}</button>
      <button class="btn" type="button" @click="emit('close', null)">{{ actions.cancel }}</button>
    </template>
  </Dialog>
</template>
