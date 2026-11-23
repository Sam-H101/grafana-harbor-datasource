import { test, expect } from '@grafana/plugin-e2e';
import type { Page, Response } from '@playwright/test';
import { column, runQuery, shot } from './helpers';

interface Seen {
  body: string;
  result: {
    error?: string;
    frames?: Array<{ schema: { fields: Array<{ name: string }> }; data: { values: unknown[][] } }>;
  };
}

/** Collects every query result of the page. */
function watchQueries(page: Page): Seen[] {
  const seen: Seen[] = [];
  page.on('response', async (r: Response) => {
    if (!r.url().includes('/api/ds/query')) {
      return;
    }
    const body = r.request().postData() ?? '';
    try {
      const json = await r.json();
      for (const result of Object.values(json.results ?? {})) {
        seen.push({ body, result: result as Seen['result'] });
      }
    } catch {
      // The page navigated away before the body was read.
    }
  });
  return seen;
}

const PANELS = [
  'Totals',
  'Vulnerabilities',
  'Projects',
  'Component health',
  'Repositories',
  'Latest pushes',
  'Vulnerabilities by severity',
  'Replication runs',
  'Garbage collection runs',
  'Audit log',
  'Job queues',
  'Robot accounts',
];
const QUERY_TYPES = [
  'statistics',
  'securitySummary',
  'projects',
  'health',
  'repositories',
  'artifacts',
  'vulnerabilities',
  'replications',
  'garbageCollections',
  'auditLogs',
  'jobQueues',
  'robots',
];

test('the provisioned dashboard draws every panel from Harbor', async ({ gotoDashboardPage, page }) => {
  const seen = watchQueries(page);
  await gotoDashboardPage({ uid: 'harbor-overview' });

  await expect(page.getByRole('gridcell', { name: 'platform' }).first()).toBeVisible({ timeout: 30_000 });
  // Panels further down load when scrolled to.
  for (const title of PANELS) {
    const heading = page.getByRole('heading', { name: title, exact: true }).first();
    await heading.scrollIntoViewIfNeeded();
    await expect(heading).toBeVisible();
  }
  for (const type of QUERY_TYPES) {
    await expect
      .poll(() => seen.filter((s) => s.body.includes(`"queryType":"${type}"`)).length, { timeout: 30_000 })
      .toBeGreaterThan(0);
  }
  expect(seen.filter((s) => s.result.error).map((s) => s.result.error)).toEqual([]);
  // "All" projects: the artifacts of every project are listed.
  const artifacts = seen.find((s) => s.body.includes('"queryType":"artifacts"'))!;
  expect(artifacts.body).toContain('"projects":["apps","library","mirror","platform","sandbox"]');
  expect(column(artifacts.result.frames![0], 'tags')).toHaveLength(6);

  await page.getByRole('heading', { name: 'Totals', exact: true }).first().scrollIntoViewIfNeeded();
  await shot(page, 'dashboard', 'overview');
});

test('the project and severity variables filter the panels', async ({ gotoDashboardPage, page }) => {
  // The variable's own query.
  const projects = await runQuery(page, 'harbor-dev', { queryType: 'projects' });
  expect(column(projects, 'project')).toEqual(['apps', 'library', 'mirror', 'platform', 'sandbox']);

  const seen = watchQueries(page);
  await gotoDashboardPage({
    uid: 'harbor-overview',
    queryParams: new URLSearchParams({ 'var-project': 'apps', 'var-severity': 'High' }),
  });
  await expect(
    page.getByTestId('data-testid Dashboard template variables submenu Label Project').locator('..')
  ).toContainText('apps', {
    timeout: 30_000,
  });
  // The repository table holds the repositories of apps only.
  await expect(page.getByRole('gridcell', { name: 'nginx' }).first()).toBeVisible({ timeout: 30_000 });
  await expect(page.getByRole('gridcell', { name: 'busybox' })).toHaveCount(0);

  const heading = page.getByRole('heading', { name: 'Vulnerabilities by severity', exact: true }).first();
  await heading.scrollIntoViewIfNeeded();
  await expect
    .poll(() => seen.filter((s) => s.body.includes('"queryType":"vulnerabilities"')).length, { timeout: 30_000 })
    .toBeGreaterThan(0);
  const vulnerabilities = seen.find((s) => s.body.includes('"queryType":"vulnerabilities"'))!;
  expect(vulnerabilities.body).toContain('"projects":["apps"]');
  expect(vulnerabilities.body).toContain('"severity":"High"');
  const severities = column(vulnerabilities.result.frames![0], 'severity');
  expect(severities.length).toBeGreaterThan(5);
  expect(new Set(severities)).toEqual(new Set(['High']));
  expect(new Set(column(vulnerabilities.result.frames![0], 'project'))).toEqual(new Set(['apps']));
  await shot(page, 'dashboard', 'apps-only');
});

test('a query variable editor offers projects and repositories', async ({ page }) => {
  await page.goto('/dashboard/new?editview=variables');
  await page.getByTestId('data-testid Call to action button Add variable').click();
  const list = page.locator('#harbor-variable-list');
  await expect(list).toBeVisible({ timeout: 30_000 });
  await expect(list).toHaveValue('Projects');
  await expect(page.locator('#harbor-variable-project')).toBeHidden();
  // The preview lists the project names.
  await expect(page.getByText('sandbox').first()).toBeVisible({ timeout: 30_000 });

  await list.click();
  await page.getByRole('option', { name: /^Repositories/ }).click();
  await expect(page.locator('#harbor-variable-project')).toBeVisible();
  await expect(page.getByText('platform/alpine').first()).toBeVisible({ timeout: 30_000 });
  await shot(page, 'dashboard', 'variable-editor');
});

test('the repositories variable can follow a project variable', async ({ page }) => {
  // What the variable's query sends when its project is another variable's value.
  const frame = await runQuery(page, 'harbor-dev', {
    queryType: 'repositories',
    projects: ['apps'],
    sort: 'name',
    limit: 10000,
  });
  expect(column(frame, 'full_name')).toEqual(['apps/nginx', 'apps/redis']);
});

test('the audit log and pushes work as annotation queries', async ({ page }) => {
  const audit = await runQuery(page, 'harbor-dev', {
    queryType: 'auditLogs',
    operation: 'create',
    resourceType: 'project',
  });
  // The first field is the time of each annotation.
  expect(audit.schema.fields[0].name).toBe('time');
  const times = column(audit, 'time') as number[];
  expect(times.length).toBeGreaterThanOrEqual(3);
  for (const t of times) {
    expect(Math.abs(Date.now() - t)).toBeLessThan(31 * 24 * 3600 * 1000);
  }

  const pushes = await runQuery(page, 'harbor-dev', { queryType: 'artifacts', projects: ['platform'] });
  expect(pushes.schema.fields[0].name).toBe('pushed');
  expect(column(pushes, 'pushed')).toHaveLength(2);
});

test('the provisioned alert rules evaluate on the server', async ({ page }) => {
  test.setTimeout(150_000);
  const states: Record<string, string | undefined> = {};
  await expect
    .poll(
      async () => {
        const resp = await page.request.get('/api/prometheus/grafana/api/v1/rules');
        const groups = (await resp.json()).data?.groups ?? [];
        for (const rule of groups.flatMap(
          (g: { rules: Array<{ name: string; state: string; health: string }> }) => g.rules
        )) {
          states[rule.name] = `${rule.state}/${rule.health}`;
        }
        return states;
      },
      { timeout: 120_000, intervals: [3000] }
    )
    .toEqual({
      // One instance per component, all healthy.
      'Harbor component unhealthy': 'inactive/ok',
      // One instance per project with a quota, none of them near it.
      'Project quota above 90%': 'inactive/ok',
      // The sample images include old releases with critical findings.
      'Critical vulnerabilities found': 'firing/ok',
    });
});
