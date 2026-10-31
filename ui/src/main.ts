import { createApp } from 'vue';
import '../design/tokens.css';
import './styles/app.css';
import App from './App.vue';
import { createBackend } from './backend';
import { toast } from './lib/toast';
import { createStore, provideStore } from './store';

async function main(): Promise<void> {
  const backend = await createBackend();
  const store = createStore(backend, { toast });
  const app = createApp(App);
  provideStore(app, store);
  app.mount('#app');
  // Starts the engine (UAC / pkexec prompt in the real app) and reads the first state.
  void store.init();
}

void main();
