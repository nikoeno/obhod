// Which shared dialog is open. Dialogs are rendered once, in App.vue.

import { reactive } from 'vue';
import type { LogName, Server } from './backend/types';

export interface UiState {
  /** Setup commands to show (needs_setup). */
  setup: string[] | null;
  /** Engine log viewer, starting on this process. */
  log: LogName | null;
  /** Add server dialog; `onAdded` runs after it has closed. */
  addServer: { onAdded?: (s: Server) => void } | null;
  /** Remove confirmation for this server. */
  removeServer: { server: Server; onDone?: () => void } | null;
}

export const ui = reactive<UiState>({ setup: null, log: null, addServer: null, removeServer: null });

export function showSetupDialog(commands: string[] | undefined): void {
  if (ui.setup) return;
  ui.setup = commands ?? [];
}
export const showLogDialog = (name: LogName = 'splitter'): void => {
  ui.log = name;
};
export const showAddServerDialog = (onAdded?: (s: Server) => void): void => {
  ui.addServer = { onAdded };
};
export const confirmRemoveServer = (server: Server, onDone?: () => void): void => {
  ui.removeServer = { server, onDone };
};

/** On-screen view state that outlives a page (the window session): "Clear view" on Activity hides older changes only on screen. */
export const view = reactive({ changesClearedAt: 0 });
