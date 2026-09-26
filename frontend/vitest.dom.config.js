import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import path from 'path';

export default defineConfig({
  plugins: [svelte({ hot: false })],
  resolve: {
    conditions: ['browser'],
    alias: { $lib: path.resolve('./src/lib') },
  },
  test: {
    environment: 'happy-dom',
    include: ['src/lib/**/*.dom.test.js'],
    setupFiles: ['./src/test/domSetup.js'],
  },
});
