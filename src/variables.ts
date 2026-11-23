import { CustomVariableSupport, DataFrame, DataQueryRequest, DataQueryResponse } from '@grafana/data';
import { getTemplateSrv } from '@grafana/runtime';
import { Observable, map } from 'rxjs';
import { VariableQueryEditor } from './components/VariableQueryEditor';
import type { DataSource } from './datasource';
import { toTextValueFrame, variableToQuery } from './query';
import { HarborQuery, HarborVariableQuery } from './types';

export class HarborVariableSupport extends CustomVariableSupport<DataSource, HarborVariableQuery, HarborQuery> {
  editor = VariableQueryEditor;

  constructor(private readonly datasource: DataSource) {
    super();
  }

  query(request: DataQueryRequest<HarborVariableQuery>): Observable<DataQueryResponse> {
    const templateSrv = getTemplateSrv();
    const built = request.targets.map((t) =>
      variableToQuery(t, (v) => templateSrv.replace(v, request.scopedVars, 'csv'))
    );
    const column = built[0]?.column ?? 'project';
    return this.datasource
      .query({ ...request, targets: built.map((b) => b.query) })
      .pipe(map((resp) => ({ ...resp, data: resp.data.map((f: DataFrame) => toTextValueFrame(f, column)) })));
  }
}
