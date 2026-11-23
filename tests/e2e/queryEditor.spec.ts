import { test, expect } from '@grafana/plugin-e2e';
import { column, DS_NAME, framesOf, nextQuery, pick, shot, typeCustom } from './helpers';

test.beforeEach(async ({ panelEditPage }) => {
  await panelEditPage.datasource.set(DS_NAME);
});

test('a new query lists the projects, and can show one number per project', async ({ panelEditPage, page }) => {
  const row = panelEditPage.getQueryEditorRow('A');
  await expect(row.locator('#harbor-query-type')).toHaveValue('Projects');
  await expect(row.locator('#harbor-projects')).toHaveAttribute('placeholder', 'Every project');

  let response = nextQuery(page, '"queryType":"projects"', '"projects":["platform"]');
  await pick(page, row.locator('#harbor-projects'), 'platform');
  let frames = await framesOf(await response);
  expect(column(frames[0], 'project')).toEqual(['platform']);
  expect(column(frames[0], 'storage_quota')).toEqual([500 * 1024 * 1024]);
  expect(column(frames[0], 'public')).toEqual([false]);

  await panelEditPage.setVisualization('Table');
  await expect(page.getByRole('gridcell', { name: 'platform' }).first()).toBeVisible({ timeout: 15_000 });
  await shot(page, 'query', 'projects');

  // One number per project with a quota, labelled for alert rules.
  response = nextQuery(page, '"value":"quotaPercent"');
  await row.getByRole('radio', { name: 'Quota used %', exact: true }).check({ force: true });
  frames = await framesOf(await response);
  expect(frames[0].schema.fields.map((f) => f.name)).toEqual(['time', 'quotaPercent']);
  expect(frames[0].schema.fields[1].config?.displayNameFromDS).toBe('platform');
});

test('repositories: filter by project and name, sorted, with a limit', async ({ panelEditPage, page }) => {
  const row = panelEditPage.getQueryEditorRow('A');
  let response = nextQuery(page, '"queryType":"repositories"');
  await pick(page, row.locator('#harbor-query-type'), 'Repositories');
  let frames = await framesOf(await response);
  expect(column(frames[0], 'full_name')).toHaveLength(5);
  await expect(row.locator('#harbor-limit')).toHaveValue('100');

  // Descending only shows once a sort is chosen.
  await expect(row.locator('#harbor-descending')).toBeHidden();
  response = nextQuery(page, '"sort":"name"');
  await pick(page, row.locator('#harbor-sort'), 'Name');
  frames = await framesOf(await response);
  expect(column(frames[0], 'full_name')).toEqual([
    'apps/nginx',
    'apps/redis',
    'library/busybox',
    'mirror/busybox',
    'platform/alpine',
  ]);

  response = nextQuery(page, '"descending":true');
  await row.locator('#harbor-descending').check({ force: true });
  frames = await framesOf(await response);
  expect(column(frames[0], 'full_name')).toEqual([
    'platform/alpine',
    'mirror/busybox',
    'library/busybox',
    'apps/redis',
    'apps/nginx',
  ]);

  response = nextQuery(page, '"repository":"busy"');
  await row.locator('#harbor-repository-contains').fill('busy');
  await row.locator('#harbor-repository-contains').press('Enter');
  frames = await framesOf(await response);
  expect(column(frames[0], 'project')).toEqual(['mirror', 'library']);

  // A limit below the number of matches says so.
  response = nextQuery(page, '"limit":1');
  await row.locator('#harbor-limit').fill('1');
  await row.locator('#harbor-limit').press('Enter');
  frames = await framesOf(await response);
  expect(column(frames[0], 'full_name')).toEqual(['mirror/busybox']);
  await expect(row.getByTestId('harbor-query-notice')).toContainText('Showing the first 1 repositories');

  await panelEditPage.setVisualization('Table');
  await expect(page.getByRole('gridcell', { name: 'busybox' }).first()).toBeVisible({ timeout: 15_000 });
  await shot(page, 'query', 'repositories');
});

test('artifacts need a project, and offer its repositories', async ({ panelEditPage, page }) => {
  const row = panelEditPage.getQueryEditorRow('A');
  await pick(page, row.locator('#harbor-query-type'), 'Artifacts');
  // Without a project the query is not sent, and the editor says why.
  await expect(row.getByTestId('harbor-query-hint')).toHaveText('Choose a project to list the artifacts of.');

  let response = nextQuery(page, '"queryType":"artifacts"', '"projects":["platform"]');
  await pick(page, row.locator('#harbor-projects'), 'platform');
  let frames = await framesOf(await response);
  expect(frames[0].schema.fields[0].name).toBe('pushed');
  expect((column(frames[0], 'tags') as string[]).sort()).toEqual(['3.10', '3.20, stable']);
  expect(column(frames[0], 'scan_status')).toEqual(['Success', 'Success']);
  for (const size of column(frames[0], 'size') as number[]) {
    expect(size).toBeGreaterThan(1_000_000);
  }

  // The repositories of the chosen project are offered by their short name.
  response = nextQuery(page, '"repository":"alpine"');
  await pick(page, row.locator('#harbor-repository'), 'alp', 'alpine');
  frames = await framesOf(await response);
  expect(column(frames[0], 'repository')).toEqual(['alpine', 'alpine']);

  await panelEditPage.setVisualization('Table');
  await expect(page.getByRole('gridcell', { name: '3.20, stable' }).first()).toBeVisible({ timeout: 15_000 });
  await shot(page, 'query', 'artifacts');
});

test('a repository or project that does not exist is reported by name', async ({ panelEditPage, page }) => {
  const row = panelEditPage.getQueryEditorRow('A');
  await pick(page, row.locator('#harbor-query-type'), 'Artifacts');
  let response = nextQuery(page, '"projects":["apps"]');
  await pick(page, row.locator('#harbor-projects'), 'apps');
  await response;

  response = nextQuery(page, '"repository":"no-such-repo"');
  await typeCustom(page, row.locator('#harbor-repository'), 'no-such-repo');
  expect(JSON.stringify(await (await response).json())).toContain('There is no repository apps/no-such-repo');

  response = nextQuery(page, '"queryType":"projects"', 'no-such-project');
  await pick(page, row.locator('#harbor-query-type'), 'Projects');
  await typeCustom(page, row.locator('#harbor-projects'), 'no-such-project');
  expect(JSON.stringify(await (await response).json())).toContain('No such project');
});

test('vulnerabilities: filter by severity, repository and CVE', async ({ panelEditPage, page }) => {
  const row = panelEditPage.getQueryEditorRow('A');
  let response = nextQuery(page, '"queryType":"vulnerabilities"');
  await pick(page, row.locator('#harbor-query-type'), 'Vulnerabilities', 'Vulnerabilities Findings');
  let frames = await framesOf(await response);
  expect(column(frames[0], 'cve').length).toBeGreaterThan(20);

  response = nextQuery(page, '"severity":"Critical"');
  await pick(page, row.locator('#harbor-severity'), 'Critical');
  frames = await framesOf(await response);
  expect(new Set(column(frames[0], 'severity'))).toEqual(new Set(['Critical']));

  // Every repository is offered by its full name.
  response = nextQuery(page, '"repository":"platform/alpine"');
  await pick(page, row.locator('#harbor-repository'), 'platform/alpine');
  frames = await framesOf(await response);
  expect(new Set(column(frames[0], 'repository'))).toEqual(new Set(['alpine']));
  expect(new Set(column(frames[0], 'project'))).toEqual(new Set(['platform']));
  const cve = column(frames[0], 'cve')[0] as string;
  expect(cve).toMatch(/^CVE-\d{4}-\d+$/);

  response = nextQuery(page, `"cve":"${cve}"`);
  await row.locator('#harbor-cve').fill(cve);
  await row.locator('#harbor-cve').press('Enter');
  frames = await framesOf(await response);
  expect(new Set(column(frames[0], 'cve'))).toEqual(new Set([cve]));

  await panelEditPage.setVisualization('Table');
  await expect(page.getByRole('gridcell', { name: cve }).first()).toBeVisible({ timeout: 15_000 });
  await shot(page, 'query', 'vulnerabilities');
});

test('totals: every number, or one of them for an alert rule', async ({ panelEditPage, page }) => {
  const row = panelEditPage.getQueryEditorRow('A');
  let response = nextQuery(page, '"queryType":"securitySummary"');
  await pick(page, row.locator('#harbor-query-type'), 'Vulnerability totals');
  let frames = await framesOf(await response);
  expect(frames[0].schema.fields.map((f) => f.name)).toEqual([
    'time',
    'critical',
    'high',
    'medium',
    'low',
    'unknown',
    'total',
    'fixable',
    'scanned_artifacts',
    'artifacts',
  ]);
  expect(column(frames[0], 'critical')[0] as number).toBeGreaterThan(2);

  response = nextQuery(page, '"measure":"critical"');
  await pick(page, row.locator('#harbor-measure'), 'Critical');
  frames = await framesOf(await response);
  expect(frames[0].schema.fields.map((f) => f.name)).toEqual(['time', 'critical']);

  // The number chosen for one query type is not carried to another.
  response = nextQuery(page, '"queryType":"statistics"');
  await pick(page, row.locator('#harbor-query-type'), 'Totals', 'Totals Projects');
  frames = await framesOf(await response);
  expect(column(frames[0], 'projects')).toEqual([5]);
  expect(column(frames[0], 'repositories')).toEqual([5]);
  expect(column(frames[0], 'storage_used')[0] as number).toBeGreaterThan(10_000_000);
  expect(column(frames[0], 'disk_free_percent')[0] as number).toBeGreaterThan(0);
  await panelEditPage.setVisualization('Stat');
  await shot(page, 'query', 'totals');
});

test('health: every component, the unhealthy ones, or a number each', async ({ panelEditPage, page }) => {
  const row = panelEditPage.getQueryEditorRow('A');
  let response = nextQuery(page, '"queryType":"health"');
  await pick(page, row.locator('#harbor-query-type'), 'Health');
  let frames = await framesOf(await response);
  expect(column(frames[0], 'component')).toEqual(
    expect.arrayContaining(['core', 'database', 'jobservice', 'registry', 'trivy'])
  );
  expect(new Set(column(frames[0], 'status'))).toEqual(new Set(['healthy']));

  response = nextQuery(page, '"unhealthyOnly":true');
  await row.locator('#harbor-unhealthy-only').check({ force: true });
  frames = await framesOf(await response);
  expect(column(frames[0], 'component')).toEqual([]);
  await expect(row.getByTestId('harbor-query-notice')).toHaveText('Every component is healthy.');

  response = nextQuery(page, '"value":"status"');
  await row.getByRole('radio', { name: 'Healthy (1 or 0)', exact: true }).check({ force: true });
  frames = await framesOf(await response);
  expect(frames[0].schema.fields.length).toBeGreaterThan(6);
  expect(frames[0].data.values[1]).toEqual([1]);
});

test('replications, garbage collection, job queues and robot accounts', async ({ panelEditPage, page }) => {
  const row = panelEditPage.getQueryEditorRow('A');
  let response = nextQuery(page, '"queryType":"replications"');
  await pick(page, row.locator('#harbor-query-type'), 'Replications');
  let frames = await framesOf(await response);
  expect(column(frames[0], 'rule')).toContain('mirror-library');

  // The rules are offered by name.
  response = nextQuery(page, '"policy":"mirror-library"');
  await pick(page, row.locator('#harbor-policy'), 'mirror-library');
  frames = await framesOf(await response);
  expect(new Set(column(frames[0], 'status'))).toEqual(new Set(['Succeed']));

  response = nextQuery(page, '"status":"Failed"');
  await row.getByRole('radio', { name: 'Failed', exact: true }).check({ force: true });
  frames = await framesOf(await response);
  expect(column(frames[0], 'rule')).toEqual([]);
  await expect(row.getByTestId('harbor-query-notice')).toHaveText('No matching replication runs.');

  response = nextQuery(page, '"queryType":"garbageCollections"');
  await pick(page, row.locator('#harbor-query-type'), 'Garbage collection');
  frames = await framesOf(await response);
  expect(column(frames[0], 'status')).toContain('Success');
  expect(column(frames[0], 'dry_run')).toContain(true);

  response = nextQuery(page, '"queryType":"jobQueues"');
  await pick(page, row.locator('#harbor-query-type'), 'Job queues');
  frames = await framesOf(await response);
  expect(column(frames[0], 'job_type')).toEqual(
    expect.arrayContaining(['GARBAGE_COLLECTION', 'IMAGE_SCAN', 'REPLICATION'])
  );

  response = nextQuery(page, '"queryType":"robots"');
  await pick(page, row.locator('#harbor-query-type'), 'Robot accounts');
  frames = await framesOf(await response);
  expect(column(frames[0], 'name')).toEqual(['robot$grafana', 'robot$grafana-limited']);
  await panelEditPage.setVisualization('Table');
  await expect(page.getByRole('gridcell', { name: 'robot$grafana-limited' }).first()).toBeVisible({ timeout: 15_000 });
  await shot(page, 'query', 'robots');
});

test('the audit log follows the time range and its filters', async ({ panelEditPage, page }) => {
  const row = panelEditPage.getQueryEditorRow('A');
  let response = nextQuery(page, '"queryType":"auditLogs"');
  await pick(page, row.locator('#harbor-query-type'), 'Audit log');
  let frames = await framesOf(await response);
  expect(frames[0].schema.fields[0].name).toBe('time');
  expect(column(frames[0], 'operation').length).toBeGreaterThan(10);

  response = nextQuery(page, '"resourceType":"robot"');
  await row.locator('#harbor-resource-type').fill('robot');
  await row.locator('#harbor-resource-type').press('Enter');
  frames = await framesOf(await response);
  expect(new Set(column(frames[0], 'resource_type'))).toEqual(new Set(['robot']));
  expect(column(frames[0], 'resource')).toEqual(expect.arrayContaining(['robot$grafana', 'robot$grafana-limited']));

  // A robot account's name has a dollar sign, which is not a dashboard variable.
  response = nextQuery(page, '"user":"robot$grafana"');
  await row.locator('#harbor-user').fill('robot$grafana');
  await row.locator('#harbor-user').press('Enter');
  frames = await framesOf(await response);
  expect(column(frames[0], 'user')).toEqual([]);

  await panelEditPage.setVisualization('Table');
  await shot(page, 'query', 'audit-log');
});

test('a limit out of range is explained instead of sent', async ({ panelEditPage, page }) => {
  const row = panelEditPage.getQueryEditorRow('A');
  const first = nextQuery(page, '"queryType":"repositories"');
  await pick(page, row.locator('#harbor-query-type'), 'Repositories');
  await first;
  await row.locator('#harbor-limit').fill('20000');
  await row.locator('#harbor-limit').press('Enter');
  await expect(row.getByTestId('harbor-query-hint')).toHaveText('The limit must be between 1 and 10000.');
});

test('a data source that may not read something says which permission is missing', async ({ panelEditPage, page }) => {
  await panelEditPage.datasource.set('Harbor (limited account)');
  const row = panelEditPage.getQueryEditorRow('A');
  let response = nextQuery(page, '"queryType":"projects"');
  await panelEditPage.refreshPanel();
  let frames = await framesOf(await response);
  expect(column(frames[0], 'project')).toHaveLength(5);
  await expect(row.getByTestId('harbor-query-notice')).toContainText(
    'Storage used and quota are blank because the account may not read quotas'
  );

  response = nextQuery(page, '"queryType":"robots"');
  await pick(page, row.locator('#harbor-query-type'), 'Robot accounts');
  expect(JSON.stringify(await (await response).json())).toContain(
    'The Harbor account is not allowed to read robot accounts. Give the robot account the permission Robot account: List (system)'
  );
});
