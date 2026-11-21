import { afterEach, expect, it, vi } from 'vitest';
import { fireEvent, getByLabelText, getByRole, waitFor } from '@testing-library/preact';
import { ProxyClient, type ProxyAdminRoute, type ProxyState } from '../src/api/proxy-client';
import type { UIContext } from './dom';
import { providerAccess } from './provider-access';
import { providerConfigSection, providerConfigSummary } from './provider-config';
import type { ProviderGroup } from './provider-groups';
import { routeForm } from './route-form';

const deepseekRoute: ProxyAdminRoute = {
  serves: [],
  profile: '',
  provider: 'cdck_deepseek',
  name: 'cdck_deepseek',
  local_prefix: '/cdck_deepseek/',
  upstream: 'https://snorlax1b.dub2.discourse.cloud:34567/v1',
  enabled: true,
  allow_insecure_http: false,
  caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
  set_headers: {},
  billing: 'local',
};

function baseState(overrides: Partial<ProxyState> = {}): ProxyState {
  return {
    defaults: null,
    auth: 'token',
    mode: 'ui',
    revision: 4,
    routes: [deepseekRoute],
    providers: [
      {
        family: 'inference',
        status: 'green',
        reason: '',
        id: 'cdck_deepseek',
        label: 'cdck_deepseek',
        routes: ['cdck_deepseek'],
      },
    ],
    secrets: [],
    callers: [],
    admin_keys: [],
    csrf: 'csrf-token',
    ...overrides,
  };
}

function uiContext(): {
  context: UIContext;
  refresh: ReturnType<typeof vi.fn>;
  announce: ReturnType<typeof vi.fn>;
} {
  const refresh = vi.fn().mockResolvedValue(undefined);
  const announce = vi.fn();
  const context: UIContext = {
    async run(action) {
      await action();
      return true;
    },
    refresh,
    announce,
    navigate: vi.fn(),
    protectToken: vi.fn(),
  };
  return { context, refresh, announce };
}

afterEach(() => {
  document.body.innerHTML = '';
  vi.restoreAllMocks();
});

it('summarizes configured provider metadata cleanly', () => {
  expect(providerConfigSummary(undefined)).toBe('None configured.');
  expect(
    providerConfigSummary({
      type: 'vllm',
      model: 'deepseek-ai/DeepSeek-V4-Flash',
      context_window: 200000,
      vision_via: 'cdck_qwen',
      models: [{ id: 'deepseek-ai/DeepSeek-V4-Flash', alias: 'deepseek-v4-flash' }],
    }),
  ).toContain('vllm · deepseek-ai/DeepSeek-V4-Flash · 1 model');
});

it('cleans pasted YAML and saves provider client config without dropping grants', async () => {
  const api = new ProxyClient(vi.fn() as unknown as typeof fetch);
  const parseSpy = vi.spyOn(api, 'parseProviderConfig').mockResolvedValue({
    name: 'cdck_deepseek',
    upstream: 'https://snorlax1b.dub2.discourse.cloud:34567/v1',
    config: { type: 'vllm', model: 'deepseek-ai/DeepSeek-V4-Flash', vision_via: 'cdck_qwen' },
    config_yaml: 'type: vllm\nmodel: deepseek-ai/DeepSeek-V4-Flash\nvision_via: cdck_qwen',
    stripped: ['api_key', 'base_url', 'enabled'],
  });
  const saveSpy = vi.spyOn(api, 'updateProviderConfig').mockResolvedValue({
    revision: 5,
    config: { type: 'vllm', model: 'deepseek-ai/DeepSeek-V4-Flash', vision_via: 'cdck_qwen' },
    config_yaml: 'type: vllm\nmodel: deepseek-ai/DeepSeek-V4-Flash\nvision_via: cdck_qwen',
  });
  const { context, refresh } = uiContext();
  const group: ProviderGroup = {
    family: 'inference',
    status: 'green',
    reason: '',
    id: 'cdck_deepseek',
    label: 'cdck_deepseek',
    routes: [deepseekRoute],
  };

  const section = providerConfigSection(group, baseState(), api, context, false);
  document.body.append(section);

  fireEvent.click(getByRole(section, 'button', { name: 'Add client config for cdck_deepseek' }));
  const textarea = getByLabelText(section, 'Provider YAML') as HTMLTextAreaElement;
  fireEvent.input(textarea, {
    target: { value: 'cdck_deepseek:\n  enabled: false\n  type: vllm\n  api_key: $(op-cache)\n' },
  });
  fireEvent.click(getByRole(section, 'button', { name: 'Clean pasted YAML' }));

  await waitFor(() => expect(parseSpy).toHaveBeenCalledTimes(1));
  expect(textarea.value).toBe(
    'type: vllm\nmodel: deepseek-ai/DeepSeek-V4-Flash\nvision_via: cdck_qwen',
  );
  expect(section.textContent).toContain('Stripped local-only fields: api_key, base_url, enabled');

  fireEvent.click(getByRole(section, 'button', { name: 'Save client config' }));
  await waitFor(() =>
    expect(saveSpy).toHaveBeenCalledWith(
      4,
      'cdck_deepseek',
      'type: vllm\nmodel: deepseek-ai/DeepSeek-V4-Flash\nvision_via: cdck_qwen',
    ),
  );
  expect(refresh).toHaveBeenCalledWith('route:cdck_deepseek');
});

it('imports pasted term-llm YAML in routeForm and submits provider_config_yaml atomically', async () => {
  const api = new ProxyClient(vi.fn() as unknown as typeof fetch);
  vi.spyOn(api, 'parseProviderConfig').mockResolvedValue({
    name: 'cdck_deepseek',
    upstream: 'https://snorlax1b.dub2.discourse.cloud:34567/v1',
    config: { type: 'vllm', model: 'deepseek-ai/DeepSeek-V4-Flash' },
    config_yaml: 'type: vllm\nmodel: deepseek-ai/DeepSeek-V4-Flash',
    stripped: ['api_key', 'base_url', 'enabled'],
  });
  const configSpy = vi.spyOn(api, 'config').mockResolvedValue(undefined);
  const { context } = uiContext();
  const emptyState = baseState({ routes: [], providers: [] });

  const form = routeForm(emptyState, api, context, vi.fn());
  document.body.append(form);

  const yamlArea = getByLabelText(form, 'Provider YAML (optional)') as HTMLTextAreaElement;
  fireEvent.input(yamlArea, {
    target: {
      value:
        'cdck_deepseek:\n  type: vllm\n  base_url: https://snorlax1b.dub2.discourse.cloud:34567/v1\n',
    },
  });
  fireEvent.click(getByRole(form, 'button', { name: 'Import pasted term-llm YAML' }));

  await waitFor(() =>
    expect((getByLabelText(form, 'Name') as HTMLInputElement).value).toBe('cdck_deepseek'),
  );
  expect((getByLabelText(form, 'Local prefix') as HTMLInputElement).value).toBe('/cdck_deepseek/');
  expect((getByLabelText(form, 'Upstream base URL') as HTMLInputElement).value).toBe(
    'https://snorlax1b.dub2.discourse.cloud:34567/v1',
  );

  fireEvent.click(getByRole(form, 'button', { name: 'Save route' }));
  await waitFor(() => expect(configSpy).toHaveBeenCalledTimes(1));
  expect(configSpy).toHaveBeenCalledWith(
    emptyState,
    expect.arrayContaining([expect.objectContaining({ name: 'cdck_deepseek' })]),
    { cdck_deepseek: { label: 'cdck_deepseek' } },
    { cdck_deepseek: 'type: vllm\nmodel: deepseek-ai/DeepSeek-V4-Flash' },
  );
});

it('shows term-llm discovery snippet and vision_via hint when creating a client for a configured provider', async () => {
  const api = new ProxyClient(vi.fn() as unknown as typeof fetch);
  vi.spyOn(api, 'createProviderCaller').mockResolvedValue({
    caller: {
      id: 'c1',
      name: 'laptop',
      created: Date.now(),
      revoked: false,
      grants: ['cdck_deepseek'],
      created_by: '',
      external_id: '',
    },
    token: 'scoped-client-token-0123456789',
  });
  const { context } = uiContext();
  const group: ProviderGroup = {
    family: 'inference',
    status: 'green',
    reason: '',
    id: 'cdck_deepseek',
    label: 'cdck_deepseek',
    routes: [deepseekRoute],
    config: {
      type: 'vllm',
      model: 'deepseek-ai/DeepSeek-V4-Flash',
      vision_via: 'cdck_qwen',
    },
  };

  const view = providerAccess(group, baseState(), api, context);
  document.body.append(view);

  fireEvent.input(getByLabelText(view, 'Client name'), { target: { value: 'laptop' } });
  fireEvent.click(getByRole(view, 'button', { name: 'Create client' }));

  await waitFor(() =>
    expect(view.querySelector('pre code')?.textContent).not.toContain('providers:'),
  );
  expect(view.textContent).toContain('routes vision via cdck_qwen');
});
