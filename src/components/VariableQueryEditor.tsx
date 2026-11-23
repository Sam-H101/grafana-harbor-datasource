import React, { useEffect, useState } from 'react';
import { QueryEditorProps } from '@grafana/data';
import { Combobox, ComboboxOption, InlineField, Stack, Text } from '@grafana/ui';
import { DataSource } from '../datasource';
import { HarborOptions, HarborQuery, HarborVariableQuery } from '../types';

type Props = QueryEditorProps<DataSource, HarborQuery, HarborOptions, HarborVariableQuery>;

const listOptions: Array<ComboboxOption<NonNullable<HarborVariableQuery['list']>>> = [
  { value: 'projects', label: 'Projects', description: 'The project names' },
  { value: 'repositories', label: 'Repositories', description: 'The full repository names, such as library/nginx' },
];

/** The editor for a dashboard variable of type Query. */
export function VariableQueryEditor({ query, onChange, datasource }: Props) {
  const list = query.list ?? 'projects';
  const [projects, setProjects] = useState<Array<ComboboxOption<string>>>([]);
  useEffect(() => {
    let live = true;
    datasource.getProjects().then(
      (items) => live && setProjects(items.map((p) => ({ value: p.name, label: p.name }))),
      () => live && setProjects([])
    );
    return () => {
      live = false;
    };
  }, [datasource]);

  const set = (patch: Partial<HarborVariableQuery>) =>
    onChange({ ...query, refId: query.refId || 'variable', ...patch });

  // A new variable has no query yet: save the default, so that its values
  // are listed without the user having to change something first.
  useEffect(() => {
    if (!query.list) {
      onChange({ refId: 'variable', list: 'projects' });
    }
    // Once, when the editor opens.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <Stack direction="column" gap={1}>
      <InlineField label="List" labelWidth={14}>
        <Combobox
          id="harbor-variable-list"
          width={32}
          options={listOptions}
          value={list}
          onChange={(o) => set({ list: o.value })}
        />
      </InlineField>
      {list === 'repositories' && (
        <InlineField
          label="Project"
          labelWidth={14}
          tooltip="Only the repositories of this project. You can also type another variable such as $project. Leave empty for every project."
        >
          <Combobox
            id="harbor-variable-project"
            width={32}
            isClearable
            createCustomValue
            placeholder="Every project"
            options={projects}
            value={query.project || null}
            onChange={(o) => set({ project: o?.value?.trim() || undefined })}
          />
        </InlineField>
      )}
      <Text variant="bodySmall" color="secondary">
        Use the variable in a query by choosing it in the Projects or Repository field. A multi-value variable of
        projects selects several projects at once.
      </Text>
    </Stack>
  );
}
