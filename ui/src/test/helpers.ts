import { createFakeBackend, type FakeOptions } from '../dev/fakeBackend';
import { createStore } from '../store';

/** Lets pending fake-backend timers (speed 0) and promise chains run. */
export const flush = (ms = 10): Promise<void> => new Promise((r) => setTimeout(r, ms));

export async function until(pred: () => boolean, ms = 500): Promise<void> {
  const t0 = Date.now();
  while (!pred()) {
    if (Date.now() - t0 > ms) throw new Error('condition not met in time');
    await flush(2);
  }
}

export function makeStore(opts: FakeOptions = {}) {
  const toasts: string[] = [];
  const backend = createFakeBackend({ speed: 0, traffic: false, ...opts });
  const store = createStore(backend, { toast: (t) => toasts.push(t) });
  return { store, backend, toasts };
}
