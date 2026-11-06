// @ts-check
import { defineConfig, devices } from '@playwright/test';

const baseURL = process.env.WEBKVM_BASE_URL || 'https://192.168.1.40:8080';

/**
 * Smoke suite against a live WebKVM instance.
 *
 * Env:
 *   WEBKVM_BASE_URL        target (default: dev VM 101)
 *   WEBKVM_ADMIN_USER      username to authenticate (default: admin)
 *   WEBKVM_ADMIN_PASSWORD  password (required for the setup project)
 *
 * Run:  WEBKVM_ADMIN_PASSWORD=… npx playwright test
 */
export default defineConfig({
  testDir: './tests',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: [['list']],
  use: {
    baseURL,
    // Dev VM uses a self-signed cert.
    ignoreHTTPSErrors: true,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'setup', testMatch: /auth\.setup\.js/ },
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'], storageState: '.auth/state.json' },
      dependencies: ['setup'],
    },
  ],
});
