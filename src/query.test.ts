import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { createDataFrame, FieldType } from '@grafana/data';
import {
  defaultsFor,
  expandList,
  expandText,
  isVariable,
  missing,
  QUERY_TYPES,
  queryTypeInfo,
  toTextValueFrame,
  variableToQuery,
} from './query';
import { HarborQuery } from './types';

const backend = readFileSync(join(__dirname, '..', 'pkg', 'models', 'query.go'), 'utf8');

describe('query types', () => {
  it('are the ones the backend knows', () => {
    const known = [...backend.matchAll(/^\tQuery\w+\s+QueryType = "(\w+)"$/gm)].map((m) => m[1]).sort();
    expect(known.length).toBeGreaterThan(5);
    expect(QUERY_TYPES.map((t) => t.value).sort()).toEqual(known);
  });

  it('offer the numbers the backend returns', () => {
    const numbers = (name: string) => {
      const line = backend.match(new RegExp(`${name}\\s*= set\\(([^)]*)\\)`));
      return [...(line?.[1] ?? '').matchAll(/"(\w+)"/g)].map((m) => m[1]).sort();
    };
    const offered = (type: 'securitySummary' | 'statistics') =>
      (queryTypeInfo(type).measures ?? []).map((m) => m.value).sort();
    expect(offered('securitySummary')).toEqual(numbers('summaryMeasures'));
    expect(offered('statistics')).toEqual(numbers('statisticsMeasures'));

    const values = (type: 'projects' | 'jobQueues' | 'health') =>
      (queryTypeInfo(type).values ?? [])
        .map((v) => v.value)
        .filter(Boolean)
        .sort();
    expect(values('projects')).toEqual(numbers('projectValues'));
    expect(values('jobQueues')).toEqual(numbers('queueValues'));
    expect(values('health')).toEqual(numbers('healthValues'));
  });

  it('fall back to projects', () => {
    expect(queryTypeInfo(undefined).value).toBe('projects');
    expect(queryTypeInfo('nope' as never).value).toBe('projects');
  });
});

describe('defaultsFor', () => {
  const full: HarborQuery = {
    refId: 'A',
    hide: true,
    datasource: { uid: 'harbor' },
    queryType: 'vulnerabilities',
    projects: ['apps'],
    repository: 'apps/nginx',
    severity: 'Critical',
    cve: 'CVE-2021-3711',
    package: 'openssl',
    limit: 25,
    sort: 'pulls',
    descending: true,
    value: 'storage',
    measure: 'critical',
    unhealthyOnly: true,
    policy: 'mirror',
    status: 'Failed',
    user: 'admin',
  };

  it('keeps what the new type uses and drops the rest', () => {
    expect(defaultsFor('repositories', full)).toEqual({
      refId: 'A',
      hide: true,
      datasource: { uid: 'harbor' },
      queryType: 'repositories',
      projects: ['apps'],
      limit: 25,
      sort: 'pulls',
      descending: true,
    });
    expect(defaultsFor('robots', full)).toEqual({
      refId: 'A',
      hide: true,
      datasource: { uid: 'harbor' },
      queryType: 'robots',
    });
    expect(defaultsFor('projects', full)).toMatchObject({
      queryType: 'projects',
      projects: ['apps'],
      value: 'storage',
    });
    expect(defaultsFor('securitySummary', full)).toMatchObject({ queryType: 'securitySummary', measure: 'critical' });
    expect(defaultsFor('statistics', full).measure).toBeUndefined();
    expect(defaultsFor('health', full)).toMatchObject({ queryType: 'health', unhealthyOnly: true });
    expect(defaultsFor('health', full).value).toBeUndefined();
    expect(defaultsFor('replications', full)).toMatchObject({ policy: 'mirror', status: 'Failed', limit: 25 });
    expect(defaultsFor('auditLogs', full)).toMatchObject({ user: 'admin', limit: 25 });
  });

  it('keeps the repository only within one type, because it means something else to each', () => {
    expect(defaultsFor('vulnerabilities', full).repository).toBe('apps/nginx');
    expect(defaultsFor('artifacts', full).repository).toBeUndefined();
    expect(defaultsFor('repositories', full).repository).toBeUndefined();
  });

  it('gives lists a limit', () => {
    expect(defaultsFor('artifacts', { refId: 'A' })).toEqual({ refId: 'A', queryType: 'artifacts', limit: 100 });
    expect(defaultsFor('projects', { refId: 'A' })).toEqual({ refId: 'A', queryType: 'projects' });
  });
});

describe('missing', () => {
  it('lets complete queries through', () => {
    expect(missing({ refId: 'A', queryType: 'projects' })).toBeNull();
    expect(missing({ refId: 'A', queryType: 'artifacts', projects: ['$project'] })).toBeNull();
    expect(
      missing({ refId: 'A', queryType: 'artifacts', projects: ['apps'], repository: 'nginx', limit: 10000 })
    ).toBeNull();
  });

  it('asks for a project for artifacts', () => {
    expect(missing({ refId: 'A', queryType: 'artifacts' })).toBe('Choose a project to list the artifacts of.');
    expect(missing({ refId: 'A', queryType: 'artifacts', projects: [] })).toBe(
      'Choose a project to list the artifacts of.'
    );
    expect(missing({ refId: 'A', queryType: 'artifacts', projects: ['a', 'b'], repository: 'x' })).toMatch(
      /only when one project/
    );
  });

  it('checks the limit', () => {
    for (const limit of [0, -1, 10001, 1.5]) {
      expect(missing({ refId: 'A', queryType: 'repositories', limit })).toBe('The limit must be between 1 and 10000.');
    }
  });
});

describe('variables', () => {
  it('are told apart from a robot account name', () => {
    for (const v of ['$project', '${project}', '${project:csv}', '[[project]]', 'lib/$repo', '$a,$b']) {
      expect(isVariable(v)).toBe(true);
    }
    for (const v of ['robot$grafana', 'library', 'a$', '$1', '']) {
      expect(isVariable(v)).toBe(false);
    }
  });

  it('expand to several projects', () => {
    const replace = (v: string) => v.replace('$project', 'apps,platform').replace('$one', 'library');
    expect(expandList(['$project', 'apps', ' sandbox ', '$one'], replace)).toEqual([
      'apps',
      'platform',
      'sandbox',
      'library',
    ]);
    expect(expandList(undefined, replace)).toEqual([]);
    expect(expandList(['$none'], () => '')).toEqual([]);
  });

  it('leave text without variables alone', () => {
    const replace = jest.fn((v: string) => v.replace('$user', 'admin'));
    expect(expandText('robot$grafana', replace)).toBe('robot$grafana');
    expect(replace).not.toHaveBeenCalled();
    expect(expandText('$user', replace)).toBe('admin');
    expect(expandText(undefined, replace)).toBeUndefined();
  });

  it('build the query of a dashboard variable', () => {
    const replace = (v: string) => v.replace('$project', 'apps,platform');
    expect(variableToQuery({ refId: '' }, replace)).toEqual({
      query: { refId: 'variable', queryType: 'projects' },
      column: 'project',
    });
    expect(variableToQuery({ refId: 'v', list: 'repositories' }, replace)).toEqual({
      query: { refId: 'v', queryType: 'repositories', projects: [], sort: 'name', limit: 10000 },
      column: 'full_name',
    });
    expect(variableToQuery({ refId: 'v', list: 'repositories', project: '$project' }, replace).query.projects).toEqual([
      'apps',
      'platform',
    ]);
  });

  it('turn a result into options', () => {
    const frame = createDataFrame({
      refId: 'v',
      fields: [
        { name: 'project', type: FieldType.string, values: ['apps', 'apps', 'platform', ''] },
        { name: 'full_name', type: FieldType.string, values: ['apps/nginx', 'apps/redis', 'platform/alpine', null] },
      ],
    });
    const options = toTextValueFrame(frame, 'full_name');
    expect(options.fields.map((f) => f.name)).toEqual(['text', 'value']);
    expect(options.fields[0].values).toEqual(['apps/nginx', 'apps/redis', 'platform/alpine']);
    expect(toTextValueFrame(frame, 'project').fields[1].values).toEqual(['apps', 'platform']);
    expect(options.refId).toBe('v');
  });
});
