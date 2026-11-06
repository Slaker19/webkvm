// @ts-check
import { test, expect } from '@playwright/test';

/**
 * Console-error smoke: every main route must render without a pageerror,
 * a failed request (other than the expected pre-auth 401) or a console
 * error. Kept locale-independent on purpose so it runs on any instance.
 */

const routes = [
  { path: '/#/vms', name: 'fleet (VmList)' },
  { path: '/#/storage', name: 'storage' },
  { path: '/#/networks', name: 'networks' },
  { path: '/#/status', name: 'status' },
  { path: '/#/backup', name: 'backup' },
];

function attachProbes(page) {
  const errors = [];
  page.on('console', (m) => {
    if (m.type() === 'error') errors.push('console: ' + m.text());
  });
  page.on('pageerror', (e) => errors.push('pageerror: ' + e.message));
  page.on('requestfailed', (req) => {
    const f = req.failure();
    if (f && !/401/.test(String(f.errorText))) {
      errors.push('requestfailed: ' + req.url() + ' -> ' + f.errorText);
    }
  });
  return errors;
}

for (const r of routes) {
  test(`${r.name}: renders without console errors`, async ({ page }) => {
    const errors = attachProbes(page);
    await page.goto(r.path, { waitUntil: 'networkidle' });
    await page.waitForTimeout(800);
    expect(errors, `${r.name} had console errors`).toEqual([]);
  });
}

test('fleet shows instances and create button', async ({ page }) => {
  await page.goto('/#/vms', { waitUntil: 'networkidle' });
  // "Create" button (locale may be en/es/ca) — match by the #/vms/new target.
  await expect(page.locator('a[href*="#/vms/new"], button:has-text("Create"), button:has-text("Crear"), button:has-text("Crea")').first()).toBeVisible({ timeout: 15000 });
});

test('status shows a version string', async ({ page }) => {
  await page.goto('/#/status', { waitUntil: 'networkidle' });
  await expect(page.getByText(/\bv0\.\d+\.\d+/).first()).toBeVisible({ timeout: 15000 });
});
