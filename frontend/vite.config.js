import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import path from 'path';
import { readFileSync } from 'fs';

const pkg = JSON.parse(readFileSync(path.resolve('./package.json'), 'utf-8'));

export default defineConfig({
  define: {
    // Exposed to the bundle so lib/brand.js can read the package version
    // at build time without an extra API call.
    __APP_VERSION__: JSON.stringify(pkg.version),
  },
  plugins: [tailwindcss(), svelte()],
  resolve: {
    alias: {
      $lib: path.resolve('./src/lib'),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://localhost:8080', ws: true },
      '/console': { target: 'http://localhost:8080', ws: true },
      '/static': { target: 'http://localhost:8080' },
    },
  },
  build: {
    target: 'esnext',
  },
  optimizeDeps: {
    exclude: ['@novnc/novnc'],
    esbuild: {
      target: 'esnext',
    },
  },
});
