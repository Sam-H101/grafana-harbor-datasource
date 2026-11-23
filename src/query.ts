import { createDataFrame, DataFrame, Field, FieldType } from '@grafana/data';
import { HarborQuery, HarborVariableQuery, QueryType, QueryValue } from './types';

export interface ValueOption {
  label: string;
  value: QueryValue;
  description?: string;
}

export interface QueryTypeInfo {
  value: QueryType;
  label: string;
  description: string;
  /** The query can be narrowed to projects. */
  projects?: boolean;
  /** How the repository field is used, if it is. */
  repository?: 'contains' | 'inProject' | 'fullName';
  /** The query returns a list that a limit cuts short. */
  limit?: boolean;
  /** The table can be turned into one number per row. */
  values?: ValueOption[];
  /** The numbers the query returns, when one of them can be chosen for an alert rule. */
  measures?: Array<{ label: string; value: string }>;
  /** What a robot account needs to run it. */
  permission: string;
}

const TABLE: ValueOption = { label: 'Table', value: '', description: 'Every detail' };

export const QUERY_TYPES: QueryTypeInfo[] = [
  {
    value: 'projects',
    label: 'Projects',
    description: 'Every project with its repositories, storage used and quota',
    projects: true,
    values: [
      TABLE,
      { label: 'Storage used', value: 'storage', description: 'One number per project, for alert rules and gauges' },
      { label: 'Quota used %', value: 'quotaPercent', description: 'One number per project that has a quota' },
      { label: 'Repositories', value: 'repositories', description: 'One number per project' },
    ],
    permission: 'Project: List (system); Quota: List (system) for storage',
  },
  {
    value: 'repositories',
    label: 'Repositories',
    description: 'Repositories with their artifact and pull counts and last update',
    projects: true,
    repository: 'contains',
    limit: true,
    permission: 'Repository: List (project)',
  },
  {
    value: 'artifacts',
    label: 'Artifacts',
    description: 'Images and other artifacts: tags, size, push and pull times, scan result. Also for annotations',
    projects: true,
    repository: 'inProject',
    limit: true,
    permission: 'Artifact: List and Repository: List (project)',
  },
  {
    value: 'vulnerabilities',
    label: 'Vulnerabilities',
    description: 'Findings of the vulnerability scanner: CVE, severity, package, fixed version, where it was found',
    projects: true,
    repository: 'fullName',
    limit: true,
    permission: 'Security hub: List and Read (system)',
  },
  {
    value: 'securitySummary',
    label: 'Vulnerability totals',
    description: 'Vulnerabilities by severity, fixable ones, and how many artifacts were scanned',
    measures: [
      { label: 'Critical', value: 'critical' },
      { label: 'High', value: 'high' },
      { label: 'Medium', value: 'medium' },
      { label: 'Low', value: 'low' },
      { label: 'Unknown', value: 'unknown' },
      { label: 'Total', value: 'total' },
      { label: 'Fixable', value: 'fixable' },
      { label: 'Scanned artifacts', value: 'scanned_artifacts' },
      { label: 'Artifacts', value: 'artifacts' },
    ],
    permission: 'Security hub: List and Read (system)',
  },
  {
    value: 'statistics',
    label: 'Totals',
    description: 'Projects, repositories, storage used and the free space of the disk the registry stores on',
    measures: [
      { label: 'Projects', value: 'projects' },
      { label: 'Public projects', value: 'public_projects' },
      { label: 'Private projects', value: 'private_projects' },
      { label: 'Repositories', value: 'repositories' },
      { label: 'Storage used', value: 'storage_used' },
      { label: 'Disk size', value: 'disk_total' },
      { label: 'Disk free', value: 'disk_free' },
      { label: 'Disk free %', value: 'disk_free_percent' },
    ],
    permission: 'Project: List; Quota: List and System volumes: Read for storage (system)',
  },
  {
    value: 'health',
    label: 'Health',
    description: 'Whether each part of Harbor is healthy: core, database, registry, job service and others',
    values: [
      TABLE,
      { label: 'Healthy (1 or 0)', value: 'status', description: 'One number per component, for alert rules' },
    ],
    permission: 'none',
  },
  {
    value: 'replications',
    label: 'Replications',
    description: 'Runs of replication rules: status, duration, and how many tasks succeeded and failed',
    limit: true,
    permission: 'Replication: List and Replication policy: List (system)',
  },
  {
    value: 'garbageCollections',
    label: 'Garbage collection',
    description: 'Runs of garbage collection: status, duration and space freed',
    limit: true,
    permission: 'Garbage collection: List (system)',
  },
  {
    value: 'auditLogs',
    label: 'Audit log',
    description: 'Who did what in the time range of the dashboard. Also for annotations',
    limit: true,
    permission: 'Audit log: List (system)',
  },
  {
    value: 'jobQueues',
    label: 'Job queues',
    description: 'Background jobs waiting, by type: scans, replications, garbage collection and others',
    values: [
      TABLE,
      { label: 'Pending', value: 'pending', description: 'One number per job type, for alert rules' },
      { label: 'Latency', value: 'latency', description: 'How long the oldest job of each type has waited' },
    ],
    permission: 'Jobservice monitor: List (system)',
  },
  {
    value: 'robots',
    label: 'Robot accounts',
    description: 'System robot accounts, whether they are enabled and when they expire',
    permission: 'Robot account: List (system)',
  },
];

export function queryTypeInfo(type?: QueryType): QueryTypeInfo {
  return QUERY_TYPES.find((t) => t.value === type) ?? QUERY_TYPES[0];
}

export const SORTS = [
  { label: 'Default', value: '' as const, description: 'The order Harbor lists them in' },
  { label: 'Name', value: 'name' as const },
  { label: 'Pulls', value: 'pulls' as const },
  { label: 'Artifacts', value: 'artifacts' as const },
  { label: 'Last update', value: 'updated' as const },
];

export const SEVERITIES = ['Critical', 'High', 'Medium', 'Low', 'Unknown'];

export const REPLICATION_STATUSES = [
  { label: 'Any', value: '' as const },
  { label: 'Succeeded', value: 'Succeed' as const },
  { label: 'Failed', value: 'Failed' as const },
  { label: 'In progress', value: 'InProgress' as const },
  { label: 'Stopped', value: 'Stopped' as const },
];

export const DEFAULT_LIMIT = 100;
export const MAX_LIMIT = 10000;

export const DEFAULT_QUERY: Partial<HarborQuery> = { queryType: 'projects' };

/**
 * The query after choosing a type: settings the new type does not use are
 * dropped, so a saved query holds only what it means.
 */
export function defaultsFor(type: QueryType, query: HarborQuery): HarborQuery {
  const info = queryTypeInfo(type);
  const next: HarborQuery = { refId: query.refId, queryType: type };
  for (const key of ['datasource', 'hide', 'key'] as const) {
    if (query[key] !== undefined) {
      (next as unknown as Record<string, unknown>)[key] = query[key];
    }
  }
  if (info.projects && query.projects?.length) {
    next.projects = query.projects;
  }
  // A repository means something different to each query type; it is kept
  // only when the type stays the same.
  if (info.repository && query.repository && query.queryType === type) {
    next.repository = query.repository;
  }
  if (info.limit) {
    next.limit = query.limit ?? DEFAULT_LIMIT;
  }
  if (info.values && query.value && info.values.some((v) => v.value === query.value)) {
    next.value = query.value;
  }
  if (info.measures && query.measure && info.measures.some((m) => m.value === query.measure)) {
    next.measure = query.measure;
  }
  if (type === 'repositories' && query.sort) {
    next.sort = query.sort;
    next.descending = query.descending;
  }
  if (type === 'vulnerabilities') {
    for (const key of ['severity', 'cve', 'package'] as const) {
      if (query[key]) {
        next[key] = query[key];
      }
    }
  }
  if (type === 'replications') {
    if (query.policy) {
      next.policy = query.policy;
    }
    if (query.status) {
      next.status = query.status;
    }
  }
  if (type === 'auditLogs') {
    for (const key of ['user', 'operation', 'resourceType'] as const) {
      if (query[key]) {
        next[key] = query[key];
      }
    }
  }
  if (type === 'health' && query.unhealthyOnly) {
    next.unhealthyOnly = true;
  }
  return next;
}

/** What stops a query from running, or null. */
export function missing(query: HarborQuery): string | null {
  if (query.queryType === 'artifacts' && !query.projects?.length) {
    return 'Choose a project to list the artifacts of.';
  }
  if (query.queryType === 'artifacts' && query.repository && (query.projects?.length ?? 0) > 1) {
    return 'A repository can be named only when one project is chosen.';
  }
  if (query.limit !== undefined && (!Number.isInteger(query.limit) || query.limit < 1 || query.limit > MAX_LIMIT)) {
    return `The limit must be between 1 and ${MAX_LIMIT}.`;
  }
  return null;
}

/** A dashboard variable ($name, ${name}, [[name]]). A $ inside a word, as in robot$grafana, is not one. */
export function isVariable(value: string): boolean {
  return /(^|\W)\$[A-Za-z_]\w*|\$\{[A-Za-z_]\w*|\[\[\w+/.test(value);
}

/**
 * Replaces variables in a list. A multi-value variable becomes several
 * entries: `replace` must format values comma-separated.
 */
export function expandList(list: string[] | undefined, replace: (value: string) => string): string[] {
  const out: string[] = [];
  for (const entry of list ?? []) {
    const expanded = isVariable(entry) ? replace(entry) : entry;
    for (const v of expanded.split(',')) {
      const trimmed = v.trim();
      if (trimmed && !out.includes(trimmed)) {
        out.push(trimmed);
      }
    }
  }
  return out;
}

/** Replaces variables in one text, leaving text without variables untouched. */
export function expandText(value: string | undefined, replace: (value: string) => string): string | undefined {
  return value && isVariable(value) ? replace(value) : value;
}

/** The query a dashboard variable runs, and the column whose values become the options. */
export function variableToQuery(
  variable: HarborVariableQuery,
  replace: (v: string) => string
): { query: HarborQuery; column: string } {
  const refId = variable.refId || 'variable';
  if (variable.list === 'repositories') {
    const projects = expandList(variable.project ? [variable.project] : [], replace);
    return {
      query: { refId, queryType: 'repositories', projects, sort: 'name', limit: MAX_LIMIT },
      column: 'full_name',
    };
  }
  return { query: { refId, queryType: 'projects' }, column: 'project' };
}

/**
 * Turns a result into the text/value frame Grafana builds variable options
 * from. `column` is the field whose distinct values become the options.
 */
export function toTextValueFrame(frame: DataFrame, column: string): DataFrame {
  const field = frame.fields.find((f: Field) => f.name === column) ?? frame.fields[0];
  const seen = new Set<string>();
  for (const v of field?.values ?? []) {
    if (v !== null && v !== undefined && String(v) !== '') {
      seen.add(String(v));
    }
  }
  const values = [...seen];
  return createDataFrame({
    refId: frame.refId,
    fields: [
      { name: 'text', type: FieldType.string, values },
      { name: 'value', type: FieldType.string, values },
    ],
  });
}
