import { test, expect } from '@grafana/plugin-e2e';
import { HARBOR_URL, ROBOT, secret, shot, TYPE } from './helpers';

test('the plugin is installed and listed', async ({ page }) => {
  await page.goto(`/plugins/${TYPE}`);
  await expect(page.getByRole('heading', { name: 'Harbor', exact: true })).toBeVisible({ timeout: 15_000 });
});

test('provisioned data source: Save & test reports the version and the visible projects', async ({
  gotoDataSourceConfigPage,
  page,
}) => {
  const configPage = await gotoDataSourceConfigPage('harbor-dev');
  // The first page load after Grafana starts can take more than 5 s.
  await expect(page.locator('#harbor-url')).toHaveValue(HARBOR_URL, { timeout: 15_000 });
  await expect(page.locator('#harbor-username')).toHaveValue(ROBOT);
  // A saved password is write-only: it is never sent back to the browser.
  await expect(page.locator('#harbor-password')).toBeDisabled();
  await expect(page.locator('#harbor-password')).toHaveValue('configured');
  await expect(configPage.saveAndTest()).toBeOK();
  await expect(configPage).toHaveAlert('success', {
    hasText: /Connected to Harbor v2\.[\w.-]+\. The account robot\$grafana can see 5 projects\./,
  });
  await shot(page, 'config', 'provisioned-ok');
});

test('an account with fewer permissions connects, and is told what it cannot read', async ({
  gotoDataSourceConfigPage,
  page,
}) => {
  const configPage = await gotoDataSourceConfigPage('harbor-limited');
  await expect(page.locator('#harbor-username')).toHaveValue('robot$grafana-limited', { timeout: 15_000 });
  await expect(configPage.saveAndTest()).toBeOK();
  await expect(configPage).toHaveAlert('success', {
    hasText:
      "Not readable with this login: project quotas (Quota: List (system)); the registry's disk (System volumes: Read (system)); vulnerabilities (Security hub: List and Read (system));",
  });
  await shot(page, 'config', 'limited-account');
});

test('a new data source: no login, a wrong secret, then the right one', async ({
  createDataSourceConfigPage,
  page,
}) => {
  const configPage = await createDataSourceConfigPage({ type: TYPE });
  await page.locator('#harbor-url').fill(HARBOR_URL);

  // Without a login Harbor shows its public projects, and the plugin says so.
  await expect(configPage.saveAndTest()).toBeOK();
  await expect(configPage).toHaveAlert('success', {
    hasText:
      'Anonymous access can see 3 projects. Without a login only public projects, repositories and artifacts are readable.',
  });
  await shot(page, 'config', 'anonymous');

  // Edits made right after Save & test can be undone by Grafana reloading the
  // saved settings, so each step repeats until its expected outcome is seen.
  const user = page.locator('#harbor-username');
  const pass = page.locator('#harbor-password');
  const setLogin = async (value: string) => {
    await user.fill(ROBOT);
    if (await pass.isDisabled()) {
      await page.getByRole('button', { name: 'Reset' }).first().click();
    }
    await expect(pass).toBeEditable();
    await pass.fill(value);
  };

  await page.waitForLoadState('networkidle');
  await expect(async () => {
    await setLogin('Not-the-secret-1');
    await expect(configPage.saveAndTest()).not.toBeOK();
    await expect(configPage).toHaveAlert('error', { hasText: 'Harbor rejected the user name or password.' });
  }).toPass({ timeout: 30_000 });
  // The password goes over http:// here, and the page says so.
  await expect(page.getByText('Password over plain HTTP')).toBeVisible();
  await shot(page, 'config', 'wrong-secret');

  await expect(async () => {
    await setLogin(secret());
    await expect(configPage.saveAndTest()).toBeOK();
  }).toPass({ timeout: 30_000 });
  await expect(configPage).toHaveAlert('success', { hasText: 'The account robot$grafana can see 5 projects.' });
});

test('a user name without a password is a configuration problem', async ({ createDataSourceConfigPage, page }) => {
  const configPage = await createDataSourceConfigPage({ type: TYPE });
  await page.locator('#harbor-url').fill(HARBOR_URL);
  await page.locator('#harbor-username').fill(ROBOT);
  await expect(configPage.saveAndTest()).not.toBeOK();
  await expect(configPage).toHaveAlert('error', {
    hasText: 'Configuration problem: enter the password or secret of "robot$grafana"',
  });
});

test('URL problems are explained', async ({ createDataSourceConfigPage, page }) => {
  const configPage = await createDataSourceConfigPage({ type: TYPE });
  const url = page.locator('#harbor-url');
  const check = async (value: string, message: string | RegExp) => {
    await page.waitForLoadState('networkidle');
    await expect(async () => {
      await url.fill(value);
      await expect(configPage.saveAndTest()).not.toBeOK();
      await expect(configPage).toHaveAlert('error', { hasText: message });
    }).toPass({ timeout: 30_000 });
  };

  await expect(configPage.saveAndTest()).not.toBeOK();
  await expect(configPage).toHaveAlert('error', { hasText: 'Configuration problem: the Harbor URL is required' });
  await check('proxy:8080', 'the Harbor URL must start with http:// or https://');
  await check('http://harbor.invalid:8080', 'Cannot reach Harbor at http://harbor.invalid:8080: host not found.');
  await check('http://proxy:8080/wrong-path', 'did not answer like Harbor. Check the URL');
  // Grafana itself is an HTTP server that is not Harbor.
  await check('http://localhost:3000', /did not answer like Harbor|asked for a login/);
  await shot(page, 'config', 'not-harbor');
});

test('the address of a Harbor page or of its API works as the URL', async ({ createDataSourceConfigPage, page }) => {
  const configPage = await createDataSourceConfigPage({ type: TYPE });
  await page.locator('#harbor-url').fill(`${HARBOR_URL}/harbor/projects/2/repositories`);
  await page.locator('#harbor-username').fill(ROBOT);
  await page.locator('#harbor-password').fill(secret());
  await expect(configPage.saveAndTest()).toBeOK();
  await expect(configPage).toHaveAlert('success', { hasText: 'Connected to Harbor v2.' });
});

test('TLS options appear when switched on', async ({ createDataSourceConfigPage, page }) => {
  await createDataSourceConfigPage({ type: TYPE });
  await expect(page.locator('#harbor-tls-ca')).toBeHidden();
  await page.locator('#harbor-tls-with-ca').check({ force: true });
  await expect(page.locator('#harbor-tls-ca')).toBeVisible();
  await page.locator('#harbor-tls-client-auth').check({ force: true });
  await expect(page.locator('#harbor-tls-client-cert')).toBeVisible();
  await expect(page.locator('#harbor-tls-client-key')).toBeVisible();
  await expect(page.locator('#harbor-tls-server-name')).toBeVisible();
  await expect(page.locator('#harbor-timeout')).toHaveAttribute('placeholder', '30');
});
