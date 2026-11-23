import { test, expect } from '@grafana/plugin-e2e';
import { column, HARBOR_PRIVATE_URL, ROBOT, runQuery, secret, shot, TYPE } from './helpers';

// The dev stack's Grafana has the secure socks proxy turned on, pointing at a
// plain SOCKS5 proxy (docker-compose.yaml). That is the mechanism behind
// Grafana Cloud's Private data source connect. "harbor-private" resolves only
// on a network Grafana is not on, so a request that reaches it went through
// the proxy.

test('a private Harbor is reachable only with the secure socks proxy on', async ({
  createDataSourceConfigPage,
  page,
}) => {
  const configPage = await createDataSourceConfigPage({ type: TYPE });
  await page.locator('#harbor-url').fill(HARBOR_PRIVATE_URL);
  await page.locator('#harbor-username').fill(ROBOT);
  await page.locator('#harbor-password').fill(secret());

  // Directly, Grafana cannot even resolve the name.
  await expect(configPage.saveAndTest()).not.toBeOK();
  await expect(configPage).toHaveAlert('error', {
    hasText: `Cannot reach Harbor at ${HARBOR_PRIVATE_URL}: host not found.`,
  });
  await expect(page.getByText('turn on Private data source connect').first()).toBeVisible();
  await shot(page, 'private-network', 'direct-fails');

  // With the proxy on, the same settings connect. The switch is Grafana's own
  // control for this; repeat the edit until the saved setting takes effect.
  const proxySwitch = page
    .getByRole('heading', { name: 'Private network' })
    .locator('xpath=ancestor::section[1]')
    .getByRole('switch');
  await page.waitForLoadState('networkidle');
  await expect(async () => {
    if (!(await proxySwitch.isChecked())) {
      await proxySwitch.check({ force: true });
    }
    await expect(configPage.saveAndTest()).toBeOK();
  }).toPass({ timeout: 45_000 });
  await expect(configPage).toHaveAlert('success', { hasText: 'The account robot$grafana can see 5 projects.' });
  await shot(page, 'private-network', 'proxy-ok');
});

test('queries on the provisioned private data source go through the proxy', async ({ page }) => {
  const health = await page.request.get('/api/datasources/uid/harbor-pdc/health');
  expect(health.ok()).toBeTruthy();
  expect((await health.json()).message).toContain('Connected to Harbor v2.');

  const frame = await runQuery(page, 'harbor-pdc', { queryType: 'repositories', projects: ['apps'], sort: 'name' });
  expect(column(frame, 'repository')).toEqual(['nginx', 'redis']);

  // The editor's dropdown lists load through it as well.
  const projects = await page.request.get('/api/datasources/uid/harbor-pdc/resources/projects');
  expect(await projects.json()).toHaveLength(5);
});
