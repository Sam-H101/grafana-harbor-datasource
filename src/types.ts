import { DataSourceJsonData } from '@grafana/data';
import { DataQuery } from '@grafana/schema';

export type QueryType =
  | 'projects'
  | 'repositories'
  | 'artifacts'
  | 'vulnerabilities'
  | 'securitySummary'
  | 'statistics'
  | 'health'
  | 'replications'
  | 'garbageCollections'
  | 'auditLogs'
  | 'jobQueues'
  | 'robots';

export type RepositorySort = '' | 'name' | 'pulls' | 'artifacts' | 'updated';
export type Severity = '' | 'Critical' | 'High' | 'Medium' | 'Low' | 'Unknown';
export type ReplicationStatus = '' | 'Succeed' | 'Failed' | 'InProgress' | 'Stopped';
/** A table (empty), or one number per row for alert rules, stats and gauges. */
export type QueryValue = '' | 'storage' | 'quotaPercent' | 'repositories' | 'pending' | 'latency' | 'status';

/** One query. The backend validates it (pkg/models/query.go). */
export interface HarborQuery extends DataQuery {
  queryType?: QueryType;
  /** Project names or dashboard variables. Empty: every project. */
  projects?: string[];
  /**
   * Repositories: text the name contains. Artifacts: a repository of the
   * chosen project. Vulnerabilities: a full name such as library/nginx.
   */
  repository?: string;
  severity?: string;
  cve?: string;
  package?: string;
  status?: ReplicationStatus;
  /** The name of a replication rule. */
  policy?: string;
  user?: string;
  operation?: string;
  resourceType?: string;
  sort?: RepositorySort;
  descending?: boolean;
  limit?: number;
  unhealthyOnly?: boolean;
  value?: QueryValue;
  /** Vulnerability totals and totals: one of the numbers instead of all. */
  measure?: string;
}

/** A dashboard variable of type Query. */
export interface HarborVariableQuery extends DataQuery {
  list?: 'projects' | 'repositories';
  /** Repositories only: the repositories of this project (or of a variable such as $project). */
  project?: string;
}

export interface HarborOptions extends DataSourceJsonData {
  username?: string;
  timeoutSeconds?: number;
  tlsAuth?: boolean;
  tlsAuthWithCACert?: boolean;
  tlsSkipVerify?: boolean;
  serverName?: string;
  enableSecureSocksProxy?: boolean;
}

export interface HarborSecureJsonData {
  password?: string;
  tlsCACert?: string;
  tlsClientCert?: string;
  tlsClientKey?: string;
}

/** A project as the backend lists it for the editors. */
export interface ProjectInfo {
  name: string;
  public: boolean;
}
