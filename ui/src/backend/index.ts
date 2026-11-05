import type { Backend } from './types';

/** True inside the Tauri webview. */
export function inTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window;
}

/**
 * Picks the backend. Outside Tauri only a dev server gets the in-memory fake; a production build
 * never contains it (the branch is dead code once `import.meta.env.DEV` is false).
 */
export async function createBackend(): Promise<Backend> {
  if (inTauri()) {
    const { createTauriBackend } = await import('./tauri');
    return createTauriBackend();
  }
  if (import.meta.env.DEV) {
    const { createFakeBackend } = await import('../dev/fakeBackend');
    return createFakeBackend();
  }
  throw new Error('Obhod must run inside its desktop app.');
}
