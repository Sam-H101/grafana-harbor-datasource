import { test, expect } from '@grafana/plugin-e2e';

// Regenerates the screenshots in src/img/screenshots used by plugin.json.
// Not part of the normal suite:
//   devenv/run.sh bash -c 'DOCS_SCREENSHOTS=1 npx playwright test tests/e2e/docs-screenshots.spec.ts'
test.skip(!process.env.DOCS_SCREENSHOTS, 'set DOCS_SCREENSHOTS=1 to regenerate documentation screenshots');
test.use({ viewport: { width: 1440, height: 900 } });

const OUT = 'src/img/screenshots';

test('dashboard', async ({ gotoDashboardPage, page }) => {
  await gotoDashboardPage({ uid: 'harbor-overview' });
  await expect(page.getByRole('gridcell', { name: 'platform' }).first()).toBeVisible({ timeout: 30_000 });
  await expect(page.getByRole('gridcell', { name: 'healthy' }).first()).toBeVisible({ timeout: 30_000 });
  await page.waitForLoadState('networkidle');
  await page.screenshot({ path: `${OUT}/dashboard.png` });
});

test('query editor', async ({ gotoPanelEditPage, page }) => {
  // The vulnerabilities panel.
  await gotoPanelEditPage({ dashboard: { uid: 'harbor-overview' }, id: '7' });
  await expect(page.getByRole('gridcell', { name: /^CVE-/ }).first()).toBeVisible({ timeout: 30_000 });
  await page.waitForLoadState('networkidle');
  await page.screenshot({ path: `${OUT}/query-editor.png` });
});

test('config editor', async ({ gotoDataSourceConfigPage, page }) => {
  const configPage = await gotoDataSourceConfigPage('harbor-dev');
  await expect(configPage.saveAndTest()).toBeOK();
  await page.getByText('Connection', { exact: true }).scrollIntoViewIfNeeded();
  await page.screenshot({ path: `${OUT}/config-editor.png` });
});
