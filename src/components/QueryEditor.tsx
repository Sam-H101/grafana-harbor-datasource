import React, { useEffect, useMemo, useState } from 'react';
import { QueryEditorProps } from '@grafana/data';
import { getTemplateSrv } from '@grafana/runtime';
import {
  Alert,
  Combobox,
  ComboboxOption,
  InlineField,
  InlineSwitch,
  Input,
  MultiCombobox,
  RadioButtonGroup,
  Stack,
  Text,
} from '@grafana/ui';
import { DataSource } from '../datasource';
import {
  DEFAULT_LIMIT,
  defaultsFor,
  isVariable,
  MAX_LIMIT,
  missing,
  QUERY_TYPES,
  queryTypeInfo,
  REPLICATION_STATUSES,
  SEVERITIES,
  SORTS,
} from '../query';
import { HarborOptions, HarborQuery, ProjectInfo, QueryType, RepositorySort } from '../types';

type Props = QueryEditorProps<DataSource, HarborQuery, HarborOptions>;
type Option = ComboboxOption<string>;

const LABEL_WIDTH = 14;

const queryTypeOptions: Option[] = QUERY_TYPES.map((t) => ({
  value: t.value,
  label: t.label,
  description: t.description,
}));
const sortOptions: Option[] = SORTS.map((s) => ({ ...s }));

/**
 * Loads something for the editor and reports a failure as text. `key`
 * reloads it when it changes; `enabled` delays it until it is needed.
 */
function useLoaded<T>(
  load: () => Promise<T>,
  datasource: DataSource,
  key = '',
  enabled = true
): { data?: T; error?: string } {
  const [state, setState] = useState<{ key: string; data?: T; error?: string }>({ key });
  useEffect(() => {
    if (!enabled) {
      return;
    }
    let live = true;
    load().then(
      (data) => live && setState({ key, data }),
      (err) => live && setState({ key, error: err?.data?.message ?? err?.message ?? 'The request failed.' })
    );
    return () => {
      live = false;
    };
    // `load` is a method of `datasource`; reload only when that or the key changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [datasource, key, enabled]);
  // What was loaded for another key is not shown.
  return state.key === key ? state : {};
}

/** The selected values as options, keeping ones that are not in the list (typed names, variables). */
function selected(values: string[] | undefined, options: Option[]): Option[] {
  return (values ?? []).map((v) => options.find((o) => o.value === v) ?? { value: v, label: v });
}

interface TextFieldProps {
  id: string;
  label: string;
  tooltip: string;
  placeholder: string;
  saved: string;
  width?: number;
  labelWidth?: number;
  onSave: (value: string) => void;
}

/** A text field that is saved, and the query run, when it loses focus or Enter is pressed. */
function TextField({
  id,
  label,
  tooltip,
  placeholder,
  saved,
  width = 24,
  labelWidth = LABEL_WIDTH,
  onSave,
}: TextFieldProps) {
  // The text follows the saved value when that changes from outside.
  const [draft, setDraft] = useState({ base: saved, text: saved });
  const text = draft.base === saved ? draft.text : saved;
  const save = () => {
    if (text.trim() !== saved) {
      onSave(text.trim());
    }
  };
  return (
    <InlineField label={label} labelWidth={labelWidth} tooltip={tooltip}>
      <Input
        id={id}
        width={width}
        placeholder={placeholder}
        value={text}
        onChange={(e) => setDraft({ base: saved, text: e.currentTarget.value })}
        onBlur={save}
        onKeyDown={(e) => e.key === 'Enter' && save()}
      />
    </InlineField>
  );
}

export function QueryEditor({ query, onChange, onRunQuery, datasource, data }: Props) {
  const info = queryTypeInfo(query.queryType);
  const projects = useLoaded<ProjectInfo[]>(() => datasource.getProjects(), datasource);

  // The repositories offered: those of the one chosen project for artifacts,
  // every repository for vulnerabilities. A project given by a variable is
  // not known here, so nothing is offered and the name is typed.
  const onlyProject = query.projects?.length === 1 && !isVariable(query.projects[0]) ? query.projects[0] : '';
  const repositoryScope = info.repository === 'inProject' ? onlyProject : '';
  const wantsRepositories = info.repository === 'fullName' || (info.repository === 'inProject' && !!onlyProject);
  const repositories = useLoaded<string[]>(
    () => datasource.getRepositories(repositoryScope),
    datasource,
    `${info.repository}:${repositoryScope}`,
    wantsRepositories
  );
  // Rule names need their own permission: ask only when they are wanted.
  const policies = useLoaded<string[]>(() => datasource.getPolicies(), datasource, '', info.value === 'replications');
  const [limit, setLimit] = useState<string | null>(null);

  // Dashboard variables can be chosen wherever a project or repository can.
  const variableOptions = useMemo<Option[]>(
    () =>
      getTemplateSrv()
        .getVariables()
        .map((v) => ({
          value: `$${v.name}`,
          label: `$${v.name}`,
          description: 'Dashboard variable',
          group: 'Variables',
        })),
    []
  );
  const projectOpts = useMemo<Option[]>(
    () => [
      ...variableOptions,
      ...(projects.data ?? []).map((p) => ({
        value: p.name,
        label: p.name,
        description: p.public ? 'Public' : 'Private',
        group: 'Projects',
      })),
    ],
    [projects.data, variableOptions]
  );
  const repositoryOpts = useMemo<Option[]>(
    () => [
      ...variableOptions,
      ...(repositories.data ?? []).map((full) => {
        // Artifacts take the name inside the project, vulnerabilities the full name.
        const name = info.repository === 'inProject' && onlyProject ? full.slice(onlyProject.length + 1) : full;
        return { value: name, label: name, group: 'Repositories' };
      }),
    ],
    [repositories.data, variableOptions, info.repository, onlyProject]
  );
  const policyOpts = useMemo<Option[]>(
    () => [
      ...variableOptions,
      ...(policies.data ?? []).map((p) => ({ value: p, label: p, group: 'Replication rules' })),
    ],
    [policies.data, variableOptions]
  );
  const severityOpts = useMemo<Option[]>(
    () => [...variableOptions, ...SEVERITIES.map((s) => ({ value: s, label: s, group: 'Severities' }))],
    [variableOptions]
  );

  // A query that has never been edited has no type yet: give it the defaults
  // (Grafana applies getDefaultQuery in some places only).
  useEffect(() => {
    if (!query.queryType) {
      onChange(defaultsFor('projects', query));
    }
    // Once, when the editor opens.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const update = (patch: Partial<HarborQuery>) => {
    onChange({ ...query, ...patch });
    onRunQuery();
  };
  const values = (opts: Option[]) => opts.map((o) => o.value.trim()).filter(Boolean);

  const onQueryType = (option: Option) => {
    onChange(defaultsFor(option.value as QueryType, query));
    onRunQuery();
  };

  const saveLimit = () => {
    if (limit === null) {
      return;
    }
    const n = parseInt(limit, 10);
    setLimit(null);
    if (Number.isFinite(n) && n !== query.limit) {
      update({ limit: n });
    }
  };

  const hint = missing(query);
  const notice = data?.series?.flatMap((s) => s.meta?.notices ?? []).map((n) => n.text)[0];
  const loadError =
    projects.error ??
    (wantsRepositories ? repositories.error : undefined) ??
    (info.value === 'replications' ? policies.error : undefined);

  const limitField = info.limit && (
    <InlineField label="Limit" labelWidth={8} tooltip={`The most rows to return, up to ${MAX_LIMIT}.`}>
      <Input
        id="harbor-limit"
        type="number"
        width={12}
        min={1}
        max={MAX_LIMIT}
        value={limit ?? String(query.limit ?? DEFAULT_LIMIT)}
        onChange={(e) => setLimit(e.currentTarget.value)}
        onBlur={saveLimit}
        onKeyDown={(e) => e.key === 'Enter' && saveLimit()}
      />
    </InlineField>
  );

  return (
    <Stack direction="column" gap={1}>
      <InlineField
        label="Query"
        labelWidth={LABEL_WIDTH}
        tooltip={`${info.description}. A robot account needs: ${info.permission}.`}
      >
        <Combobox
          id="harbor-query-type"
          width={40}
          options={queryTypeOptions}
          value={info.value}
          onChange={onQueryType}
        />
      </InlineField>

      {info.projects && (
        <InlineField
          label="Projects"
          labelWidth={LABEL_WIDTH}
          grow
          tooltip={
            info.value === 'artifacts'
              ? 'The projects to list artifacts of. You can also type a project name or choose a dashboard variable.'
              : 'Leave empty for every project. You can also type a project name or choose a dashboard variable.'
          }
        >
          <MultiCombobox
            id="harbor-projects"
            width="auto"
            minWidth={40}
            maxWidth={100}
            isClearable
            createCustomValue
            placeholder={info.value === 'artifacts' ? 'Choose a project' : 'Every project'}
            options={projectOpts}
            value={selected(query.projects, projectOpts)}
            onChange={(opts) => update({ projects: values(opts) })}
          />
        </InlineField>
      )}

      {info.repository === 'contains' && (
        <Stack direction="row" gap={1} wrap="wrap">
          <TextField
            id="harbor-repository-contains"
            label="Name contains"
            tooltip="Only repositories whose name contains this text."
            placeholder="Any name"
            saved={query.repository ?? ''}
            onSave={(repository) => update({ repository: repository || undefined })}
          />
          <InlineField
            label="Sort by"
            labelWidth={10}
            tooltip="The order of the rows, which decides what the limit keeps."
          >
            <Combobox
              id="harbor-sort"
              width={20}
              options={sortOptions}
              value={query.sort ?? ''}
              onChange={(o) => update({ sort: (o.value as RepositorySort) || undefined })}
            />
          </InlineField>
          {query.sort && (
            <InlineField label="Descending" labelWidth={12}>
              <InlineSwitch
                id="harbor-descending"
                value={!!query.descending}
                onChange={(e) => update({ descending: e.currentTarget.checked })}
              />
            </InlineField>
          )}
          {limitField}
        </Stack>
      )}

      {(info.repository === 'inProject' || info.repository === 'fullName') && (
        <Stack direction="row" gap={1} wrap="wrap">
          <InlineField
            label="Repository"
            labelWidth={LABEL_WIDTH}
            tooltip={
              info.repository === 'inProject'
                ? 'One repository of the chosen project. Leave empty for every repository of the projects.'
                : 'Only findings in this repository, by its full name such as library/nginx. Leave empty for every repository.'
            }
          >
            <Combobox
              id="harbor-repository"
              width={40}
              isClearable
              createCustomValue
              placeholder="Every repository"
              options={repositoryOpts}
              value={query.repository || null}
              onChange={(o) => update({ repository: o?.value?.trim() || undefined })}
            />
          </InlineField>
          {info.value === 'artifacts' && limitField}
        </Stack>
      )}

      {info.value === 'vulnerabilities' && (
        <Stack direction="row" gap={1} wrap="wrap">
          <InlineField
            label="Severity"
            labelWidth={LABEL_WIDTH}
            tooltip="Only findings of this severity. Leave empty for every severity."
          >
            <Combobox
              id="harbor-severity"
              width={20}
              isClearable
              createCustomValue
              placeholder="Any"
              options={severityOpts}
              value={query.severity || null}
              onChange={(o) => update({ severity: o?.value?.trim() || undefined })}
            />
          </InlineField>
          <TextField
            id="harbor-cve"
            label="CVE"
            labelWidth={8}
            tooltip="One vulnerability by its exact id, for example CVE-2021-3711: where it was found."
            placeholder="Any"
            width={20}
            saved={query.cve ?? ''}
            onSave={(cve) => update({ cve: cve || undefined })}
          />
          <TextField
            id="harbor-package"
            label="Package"
            labelWidth={10}
            tooltip="Only findings in this package, by its exact name, for example openssl."
            placeholder="Any"
            width={20}
            saved={query.package ?? ''}
            onSave={(pkg) => update({ package: pkg || undefined })}
          />
          {limitField}
        </Stack>
      )}

      {info.value === 'replications' && (
        <Stack direction="row" gap={1} wrap="wrap">
          <InlineField
            label="Rule"
            labelWidth={LABEL_WIDTH}
            tooltip="Only runs of this replication rule. Leave empty for every rule."
          >
            <Combobox
              id="harbor-policy"
              width={32}
              isClearable
              createCustomValue
              placeholder="Every rule"
              options={policyOpts}
              value={query.policy || null}
              onChange={(o) => update({ policy: o?.value?.trim() || undefined })}
            />
          </InlineField>
          <InlineField label="Status" labelWidth={8}>
            <RadioButtonGroup
              id="harbor-status"
              options={REPLICATION_STATUSES}
              value={query.status ?? ''}
              onChange={(status) => update({ status: status || undefined })}
            />
          </InlineField>
          {limitField}
        </Stack>
      )}

      {info.value === 'auditLogs' && (
        <Stack direction="row" gap={1} wrap="wrap">
          <TextField
            id="harbor-user"
            label="User"
            tooltip="Only what this user or robot account did, by its exact name."
            placeholder="Anyone"
            width={20}
            saved={query.user ?? ''}
            onSave={(user) => update({ user: user || undefined })}
          />
          <TextField
            id="harbor-operation"
            label="Operation"
            labelWidth={10}
            tooltip="Only this operation, for example create, delete, pull or push."
            placeholder="Any"
            width={16}
            saved={query.operation ?? ''}
            onSave={(operation) => update({ operation: operation || undefined })}
          />
          <TextField
            id="harbor-resource-type"
            label="Resource type"
            labelWidth={14}
            tooltip="Only this kind of resource, for example artifact, project, user or robot."
            placeholder="Any"
            width={16}
            saved={query.resourceType ?? ''}
            onSave={(resourceType) => update({ resourceType: resourceType || undefined })}
          />
          {limitField}
        </Stack>
      )}

      {info.value === 'garbageCollections' && limitField && (
        <Stack direction="row" gap={1} wrap="wrap">
          {limitField}
        </Stack>
      )}

      {info.value === 'health' && (
        <InlineField
          label="Unhealthy only"
          labelWidth={LABEL_WIDTH}
          tooltip="Only the components that are not healthy. Off: every component."
        >
          <InlineSwitch
            id="harbor-unhealthy-only"
            value={!!query.unhealthyOnly}
            onChange={(e) => update({ unhealthyOnly: e.currentTarget.checked })}
          />
        </InlineField>
      )}

      {info.measures && (
        <InlineField
          label="Number"
          labelWidth={LABEL_WIDTH}
          tooltip="Every number, for stat panels and tables, or one of them, for an alert rule or a gauge."
        >
          <Combobox
            id="harbor-measure"
            width={32}
            isClearable
            placeholder="All of them"
            options={info.measures}
            value={query.measure || null}
            onChange={(o) => update({ measure: o?.value || undefined })}
          />
        </InlineField>
      )}

      {info.values && (
        <InlineField
          label="Show"
          labelWidth={LABEL_WIDTH}
          tooltip="The table has every detail. For an alert rule, a stat or a gauge, choose one number per row instead."
        >
          <RadioButtonGroup
            id="harbor-value"
            options={info.values}
            value={query.value ?? ''}
            onChange={(value) => update({ value: value || undefined })}
          />
        </InlineField>
      )}

      {hint && (
        <Text color="info" data-testid="harbor-query-hint">
          {hint}
        </Text>
      )}
      {!hint && notice && (
        <Text color="info" data-testid="harbor-query-notice">
          {notice}
        </Text>
      )}
      {loadError && (
        <Alert severity="error" title="Could not load the lists from Harbor" data-testid="harbor-load-error">
          {loadError} Check the data source settings with Save &amp; test.
        </Alert>
      )}
    </Stack>
  );
}
