import React, { ChangeEvent } from 'react';
import {
  DataSourcePluginOptionsEditorProps,
  onUpdateDatasourceJsonDataOption,
  onUpdateDatasourceSecureJsonDataOption,
  updateDatasourcePluginResetOption,
} from '@grafana/data';
import { config } from '@grafana/runtime';
import {
  Alert,
  Field,
  Input,
  SecretInput,
  SecretTextArea,
  SecureSocksProxySettings,
  Stack,
  Switch,
  Text,
} from '@grafana/ui';
import { HarborOptions, HarborSecureJsonData } from '../types';

type Props = DataSourcePluginOptionsEditorProps<HarborOptions, HarborSecureJsonData>;

const WIDTH = 50;

export function ConfigEditor(props: Props) {
  const { options, onOptionsChange } = props;
  const { jsonData, secureJsonFields } = options;
  const plainHttp = options.url?.trim().toLowerCase().startsWith('http://');
  const passwordSet = !!secureJsonFields?.password || !!options.secureJsonData?.password;

  const setJson = <K extends keyof HarborOptions>(key: K, value: HarborOptions[K]) =>
    onOptionsChange({ ...options, jsonData: { ...jsonData, [key]: value } });

  const onTimeout = (e: ChangeEvent<HTMLInputElement>) => {
    const n = parseInt(e.currentTarget.value, 10);
    setJson('timeoutSeconds', Number.isFinite(n) ? n : undefined);
  };

  return (
    <Stack direction="column" gap={3}>
      <section>
        <Text element="h3">Connection</Text>
        <Field
          label="URL"
          description="The address you open Harbor at in a browser, for example https://harbor.example.com."
          required
        >
          <Input
            id="harbor-url"
            width={WIDTH}
            value={options.url || ''}
            placeholder="https://harbor.example.com"
            onChange={(e) => onOptionsChange({ ...options, url: e.currentTarget.value })}
          />
        </Field>
      </section>

      <section>
        <Text element="h3">Authentication</Text>
        <Field
          label="User name"
          description="A robot account for Grafana, with its full name such as robot$grafana, or a Harbor user. The plugin only reads. The README lists the permission each query needs, and Save & test reports any that are missing. Leave empty to read public projects only."
        >
          <Input
            id="harbor-username"
            width={WIDTH}
            autoComplete="off"
            placeholder="robot$grafana"
            value={jsonData.username || ''}
            onChange={onUpdateDatasourceJsonDataOption(props, 'username')}
          />
        </Field>
        <Field label="Password" description="The secret of the robot account, or the user's password.">
          <SecretInput
            id="harbor-password"
            width={WIDTH}
            autoComplete="new-password"
            isConfigured={!!secureJsonFields?.password}
            value={options.secureJsonData?.password || ''}
            onChange={onUpdateDatasourceSecureJsonDataOption(props, 'password')}
            onReset={() => updateDatasourcePluginResetOption(props, 'password')}
          />
        </Field>
        {plainHttp && passwordSet && (
          <Alert severity="warning" title="Password over plain HTTP">
            The URL uses http://, so the password is sent unencrypted. Use https:// unless Grafana and Harbor share a
            trusted network.
          </Alert>
        )}
      </section>

      {config.secureSocksDSProxyEnabled && (
        <section>
          <Text element="h3">Private network</Text>
          <SecureSocksProxySettings options={options} onOptionsChange={onOptionsChange} />
        </section>
      )}

      <section>
        <Text element="h3">TLS</Text>
        <Field
          label="Custom CA certificate"
          description="Verify Harbor's certificate against this CA (PEM), for a private or internal CA."
        >
          <Switch
            id="harbor-tls-with-ca"
            value={!!jsonData.tlsAuthWithCACert}
            onChange={(e) => setJson('tlsAuthWithCACert', e.currentTarget.checked)}
          />
        </Field>
        {jsonData.tlsAuthWithCACert && (
          <Field label="CA certificate">
            <SecretTextArea
              id="harbor-tls-ca"
              cols={60}
              rows={6}
              placeholder="-----BEGIN CERTIFICATE-----"
              isConfigured={!!secureJsonFields?.tlsCACert}
              onChange={onUpdateDatasourceSecureJsonDataOption(props, 'tlsCACert')}
              onReset={() => updateDatasourcePluginResetOption(props, 'tlsCACert')}
            />
          </Field>
        )}
        <Field
          label="Client certificate"
          description="Present a client certificate to Harbor (mutual TLS), if a proxy in front of it requires one."
        >
          <Switch
            id="harbor-tls-client-auth"
            value={!!jsonData.tlsAuth}
            onChange={(e) => setJson('tlsAuth', e.currentTarget.checked)}
          />
        </Field>
        {jsonData.tlsAuth && (
          <>
            <Field
              label="Server name"
              description="Expected name in the server's certificate, if it differs from the URL host."
            >
              <Input
                id="harbor-tls-server-name"
                width={WIDTH}
                value={jsonData.serverName || ''}
                onChange={onUpdateDatasourceJsonDataOption(props, 'serverName')}
              />
            </Field>
            <Field label="Client certificate (PEM)">
              <SecretTextArea
                id="harbor-tls-client-cert"
                cols={60}
                rows={6}
                placeholder="-----BEGIN CERTIFICATE-----"
                isConfigured={!!secureJsonFields?.tlsClientCert}
                onChange={onUpdateDatasourceSecureJsonDataOption(props, 'tlsClientCert')}
                onReset={() => updateDatasourcePluginResetOption(props, 'tlsClientCert')}
              />
            </Field>
            <Field label="Client key (PEM)">
              <SecretTextArea
                id="harbor-tls-client-key"
                cols={60}
                rows={6}
                placeholder="-----BEGIN PRIVATE KEY-----"
                isConfigured={!!secureJsonFields?.tlsClientKey}
                onChange={onUpdateDatasourceSecureJsonDataOption(props, 'tlsClientKey')}
                onReset={() => updateDatasourcePluginResetOption(props, 'tlsClientKey')}
              />
            </Field>
          </>
        )}
        <Field label="Skip TLS certificate verification" description="Insecure. Only for testing.">
          <Switch
            id="harbor-tls-skip-verify"
            value={!!jsonData.tlsSkipVerify}
            onChange={(e) => setJson('tlsSkipVerify', e.currentTarget.checked)}
          />
        </Field>
      </section>

      <section>
        <Text element="h3">Advanced</Text>
        <Field
          label="Query timeout (seconds)"
          description="How long one panel query may take. A search over several repositories makes several requests. Default 30."
        >
          <Input
            id="harbor-timeout"
            type="number"
            min={1}
            max={300}
            width={20}
            placeholder="30"
            value={jsonData.timeoutSeconds ?? ''}
            onChange={onTimeout}
          />
        </Field>
      </section>
    </Stack>
  );
}
