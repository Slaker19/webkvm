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
    chunkSizeWarningLimit: 600,
    rollupOptions: {
      output: {
        // Split the heavy third-party libraries out of the entry chunk.
        // Route components are already lazy (see routeLoaders in
        // App.svelte), but their shared vendor deps would otherwise all
        // land in index.js, which every visitor downloads before the
        // first paint. Keeping them separate means the browser can
        // cache them across app deploys (they change far less often
        // than our own code) and skip the ones a given page never
        // touches — e.g. a viewer who never opens a console pays for
        // neither xterm nor noVNC.
        manualChunks(id) {
          if (id.includes('node_modules/@xterm/')) {
            return 'vendor-xterm';
          }
          if (id.includes('node_modules/@novnc/')) {
            return 'vendor-novnc';
          }
          if (id.includes('node_modules/@lucide/svelte/')) {
            return 'vendor-lucide';
          }
          if (id.includes('node_modules/bits-ui/')) {
            return 'vendor-ui';
          }
          if (id.includes('node_modules/cronstrue/')) {
            return 'vendor-cron';
          }
          if (id.includes('node_modules/qrcode/')) {
            return 'vendor-qr';
          }
          if (id.includes('src/lib/i18n.svelte.js')) {
            return 'app-i18n';
          }
        },
      },
    },
  },
  optimizeDeps: {
    exclude: ['@novnc/novnc'],
    esbuild: {
      target: 'esnext',
    },
  },
  test: {
    // Vitest runs PURE functions only — node environment,
    // explicitly no jsdom in this version. Scope stays confined to
    // src/lib/utils/** so Svelte components (which need a DOM) are
    // never pulled into the test graph.
    //
    // *.dom.test.js is the deliberate exception: those mount real
    // components and need a DOM, so they run separately via
    // `npm run test:dom` (vitest.dom.config.js, happy-dom). They are
    // excluded here so this suite stays fast and DOM-free.
    environment: 'node',
    include: ['src/lib/**/*.test.js'],
    exclude: ['**/node_modules/**', '**/*.dom.test.js'],
  },
});
