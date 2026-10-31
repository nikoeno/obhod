// Per-viewer UI settings in localStorage. Every access is wrapped: storage can be blocked or empty.
// (Production stores these on the Rust side later; the engine never sees them.)

const PREFIX = 'obhod.';

export function loadSetting<T>(key: string, fallback: T): T {
  try {
    const v = localStorage.getItem(PREFIX + key);
    return v === null ? fallback : (JSON.parse(v) as T);
  } catch {
    return fallback;
  }
}

export function saveSetting(key: string, value: unknown): void {
  try {
    localStorage.setItem(PREFIX + key, JSON.stringify(value));
  } catch {
    /* storage unavailable: the setting just does not persist */
  }
}
