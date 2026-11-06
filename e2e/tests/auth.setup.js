// @ts-check
import { test as setup, expect } from '@playwright/test';
import fs from 'node:fs';

const USER = process.env.WEBKVM_ADMIN_USER || 'admin';
const PASS = process.env.WEBKVM_ADMIN_PASSWORD;

setup('authenticate and persist session', async ({ page }) => {
  if (!PASS) {
    throw new Error('WEBKVM_ADMIN_PASSWORD is required (set it to the admin password)');
  }

  await page.goto('/');
  await page.waitForURL(/#\/login/);

  await page.fill('#login-user', USER);
  await page.fill('#login-pass', PASS);
  await page.click('button[type="submit"]');

  // After login the SPA lands on the fleet view (or the change-password
  // screen if must_change_password is set).
  await page.waitForURL(/#\/(vms|account|storage|networks|status)/, { timeout: 20000 });

  fs.mkdirSync('.auth', { recursive: true });
  await page.context().storageState({ path: '.auth/state.json' });
});
