import { AnnotationSupport, CoreApp, DataSourceInstanceSettings, ScopedVars } from '@grafana/data';
import { DataSourceWithBackend, getTemplateSrv } from '@grafana/runtime';
import { DEFAULT_QUERY, expandList, expandText, missing } from './query';
import { HarborOptions, HarborQuery, ProjectInfo } from './types';
import { HarborVariableSupport } from './variables';

export class DataSource extends DataSourceWithBackend<HarborQuery, HarborOptions> {
  private projectList?: Promise<ProjectInfo[]>;
  private policyList?: Promise<string[]>;
  private repositoryLists = new Map<string, Promise<string[]>>();

  constructor(instanceSettings: DataSourceInstanceSettings<HarborOptions>) {
    super(instanceSettings);
    this.variables = new HarborVariableSupport(this);
    // Standard annotation support: the query editor is reused, and the
    // result's first time field and its text fields become the annotation.
    const annotations: AnnotationSupport<HarborQuery> = {
      getDefaultQuery: () => ({ queryType: 'auditLogs', limit: 100 }),
    };
    this.annotations = annotations;
  }

  getDefaultQuery(_: CoreApp): Partial<HarborQuery> {
    return DEFAULT_QUERY;
  }

  /** Incomplete queries are not sent: the editor says what is missing. */
  filterQuery(query: HarborQuery): boolean {
    return !query.hide && missing(query) === null;
  }

  applyTemplateVariables(query: HarborQuery, scopedVars: ScopedVars): HarborQuery {
    const templateSrv = getTemplateSrv();
    const text = (value?: string) => expandText(value, (v) => templateSrv.replace(v, scopedVars));
    return {
      ...query,
      // A variable set to All or to several values becomes several projects.
      projects: expandList(query.projects, (v) => templateSrv.replace(v, scopedVars, 'csv')),
      repository: text(query.repository),
      severity: text(query.severity),
      cve: text(query.cve),
      package: text(query.package),
      policy: text(query.policy),
      user: text(query.user),
      operation: text(query.operation),
      resourceType: text(query.resourceType),
    };
  }

  /** Projects for the editors' dropdowns, fetched once per data source instance. */
  getProjects(): Promise<ProjectInfo[]> {
    if (!this.projectList) {
      this.projectList = this.getResource<ProjectInfo[]>('projects').catch((err) => {
        this.projectList = undefined;
        throw err;
      });
    }
    return this.projectList;
  }

  /** Full repository names ("project/name") of one project, or of every project. */
  getRepositories(project = ''): Promise<string[]> {
    let list = this.repositoryLists.get(project);
    if (!list) {
      list = this.getResource<string[]>('repositories', project ? { project } : undefined).catch((err) => {
        this.repositoryLists.delete(project);
        throw err;
      });
      this.repositoryLists.set(project, list);
    }
    return list;
  }

  /** The names of the replication rules. */
  getPolicies(): Promise<string[]> {
    if (!this.policyList) {
      this.policyList = this.getResource<string[]>('policies').catch((err) => {
        this.policyList = undefined;
        throw err;
      });
    }
    return this.policyList;
  }
}
