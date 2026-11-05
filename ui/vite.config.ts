import { defineConfig } from 'vitest/config';
import vue from '@vitejs/plugin-vue';
import pkg from './package.json' with { type: 'json' };

// Fixed dev port: src-tauri/tauri.conf.json devUrl points at it.
export default defineConfig({
  plugins: [vue()],
  clearScreen: false,
  define: { __UI_VERSION__: JSON.stringify(pkg.version) },
  server: { port: 1420, strictPort: true, host: '127.0.0.1' },
  build: { target: 'es2022', sourcemap: false, chunkSizeWarningLimit: 600 },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
});
