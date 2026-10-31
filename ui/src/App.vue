<script setup lang="ts">
// App shell: navigation, header status, shared dialogs, keyboard shortcuts (docs/UI.md section 8).
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { pages, actions } from '../design/copy';
import AddServerDialog from './components/AddServerDialog.vue';
import Icon from './components/Icon.vue';
import LogDialog from './components/LogDialog.vue';
import RemoveServerDialog from './components/RemoveServerDialog.vue';
import SetupDialog from './components/SetupDialog.vue';
import Toasts from './components/Toasts.vue';
import type { IconName } from './lib/icons';
import { toast } from './lib/toast';
import Activity from './pages/Activity.vue';
import Help from './pages/Help.vue';
import Home from './pages/Home.vue';
import Settings from './pages/Settings.vue';
import Sites from './pages/Sites.vue';
import { go, route, type PageName } from './router';
import { useStore } from './store';
import { showSetupDialog, ui } from './ui';

const store = useStore();
const S = store.S;
const main = ref<HTMLElement | null>(null);

// Dev-only controls. In a production build `import.meta.env.DEV` is false and the import is removed.
const DevPanel = import.meta.env.DEV ? defineAsyncComponent(() => import('./dev/DevPanel.vue')) : null;

const NAV: Array<{ page: PageName; icon: IconName; keys?: string }> = [
  { page: 'home', icon: 'home', keys: 'Control+1' },
  { page: 'sites', icon: 'sites', keys: 'Control+2' },
  { page: 'activity', icon: 'activity', keys: 'Control+3' },
  { page: 'settings', icon: 'settings', keys: 'Control+4' },
];

const statusWord = computed(() => {
  if (!S.loaded) return 'Connecting to the engine…';
  if (S.engineDown) return 'Engine not running';
  return { connected: 'On', connecting: 'Starting…', disconnecting: 'Stopping…' }[S.state as string] ?? (S.error ? 'Off (error)' : 'Off');
});
const statusClass = computed(() => (S.engineDown ? 'down' : S.state));

watch(
  () => route.page,
  (p) => {
    document.title = `${pages.nav[p]} · Obhod`;
  },
  { immediate: true },
);
watch(
  () => S.quit,
  (q) => document.body.classList.toggle('quit', q),
);

// A Start that needs the one-time setup opens the commands dialog by itself.
const offError = store.onNewError((info) => {
  if (info.code === 'needs_setup') showSetupDialog(info.commands);
});
onBeforeUnmount(offError);

function onKey(e: KeyboardEvent): void {
  if (document.querySelector('dialog[open]')) return; // dialogs handle their own keys (Esc closes)
  const mod = e.ctrlKey || e.metaKey;
  if (!mod || e.altKey) return;
  if (e.key === 'Enter') {
    e.preventDefault();
    void store.toggleConnection();
  } else if (['1', '2', '3', '4'].includes(e.key)) {
    e.preventDefault();
    go(NAV[Number(e.key) - 1]!.page);
    main.value?.focus();
  } else if (e.key.toLowerCase() === 'f') {
    e.preventDefault();
    go('sites');
    // The search box exists once the Sites page has rendered.
    void nextTick(() => {
      const box = document.getElementById('site-search') as HTMLInputElement | null;
      box?.focus();
      box?.select();
    });
  } else if (e.key.toLowerCase() === 'r') {
    e.preventDefault();
    if (!S.checking) {
      void store.checkNow();
      toast(actions.checkingToast);
    }
  } else if (e.key.toLowerCase() === 'q') {
    e.preventDefault();
    void store.quit();
  }
}
onMounted(() => document.addEventListener('keydown', onKey));
onBeforeUnmount(() => document.removeEventListener('keydown', onKey));

function closeAdd(s: import('./backend/types').Server | null): void {
  const cfg = ui.addServer;
  ui.addServer = null;
  if (s && cfg?.onAdded) setTimeout(() => cfg.onAdded!(s), 0); // after the dialog has returned focus
}
function closeRemove(removed: boolean): void {
  const cfg = ui.removeServer;
  ui.removeServer = null;
  if (removed && cfg?.onDone) setTimeout(cfg.onDone, 0);
}
</script>

<template>
  <a class="skip" href="#main" @click.prevent="main?.focus()">Skip to content</a>
  <div class="shell">
    <nav class="nav" aria-label="Main">
      <div class="brand" aria-hidden="true"><span class="brand-dot"></span>Obhod</div>
      <ul>
        <li v-for="n in NAV" :key="n.page">
          <a :href="`#/${n.page}`" :aria-current="route.page === n.page ? 'page' : undefined" :aria-keyshortcuts="n.keys">
            <Icon :name="n.icon" /><span>{{ pages.nav[n.page] }}</span>
          </a>
        </li>
        <li class="sep">
          <a href="#/help" :aria-current="route.page === 'help' ? 'page' : undefined">
            <Icon name="help" /><span>{{ pages.nav.help }}</span>
          </a>
        </li>
      </ul>
      <p :class="['nav-status', statusClass]" aria-live="polite">{{ statusWord }}</p>
    </nav>

    <main id="main" ref="main" tabindex="-1">
      <section v-if="S.quit" class="page quitpage">
        <h1>{{ pages.quit.done }}</h1>
        <button class="btn primary" type="button" @click="store.init()">{{ pages.quit.openAgain }}</button>
      </section>
      <Home v-else-if="route.page === 'home'" />
      <Sites v-else-if="route.page === 'sites'" />
      <Activity v-else-if="route.page === 'activity'" />
      <Settings v-else-if="route.page === 'settings'" />
      <Help v-else />
    </main>
  </div>

  <SetupDialog v-if="ui.setup" :commands="ui.setup" @close="ui.setup = null" />
  <LogDialog v-if="ui.log" :initial="ui.log" @close="ui.log = null" />
  <AddServerDialog v-if="ui.addServer" @close="closeAdd" />
  <RemoveServerDialog v-if="ui.removeServer" :server="ui.removeServer.server" @close="closeRemove" />
  <Toasts />
  <component :is="DevPanel" v-if="DevPanel" />
</template>
