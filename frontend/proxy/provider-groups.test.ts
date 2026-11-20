import { afterEach, expect, it, vi } from 'vitest';
import {
  fireEvent,
  getByLabelText,
  getByRole,
  queryByRole,
  waitFor,
} from '@testing-library/preact';
import { ProxyClient, type ProxyAdminRoute, type ProxyState } from '../src/api/proxy-client';
import type { UIContext } from './dom';
import { providerGroups } from './provider-groups';
import { routesView } from './routes';

const auth = { header: 'Authorization', prefix: 'Bearer ' };
const route = (name: string, upstream: string): ProxyAdminRoute => ({
  serves: [],
  profile: '',
  provider: name,
  name,
  local_prefix: `/${name}/`,
  upstream,
  caller_auth: auth,
  enabled: true,
  allow_insecure_http: false,
  set_headers: {},
});
const chat = {
  ...route('chatgpt', 'https://chatgpt.com/backend-api/codex'),
  profile: 'chatgpt',
  account: 'personal',
};
const usage = {
  ...route('chatgpt-backend', 'https://chatgpt.com/backend-api'),
  account: 'personal',
};
const media = { ...route('chatgpt-openai', 'https://api.openai.com/v1'), account: 'personal' };
const openai = route('openai', media.upstream);
const preset = (route: ProxyAdminRoute, label: string, account_kind?: 'chatgpt') => ({
  family: 'inference' as const,
  id: route.name,
  label,
  upstream: route.upstream,
  auth,
  account_kind,
});

function snapshot(overrides: Partial<ProxyState> = {}): ProxyState {
  return {
    defaults: null,
    auth: 'token',
    mode: 'ui',
    revision: 7,
    csrf: 'csrf',
    admin_keys: [],
    accounts: [{ name: 'personal', kind: 'chatgpt', state: 'ready', generation: 1, updated: 0 }],
    secrets: [],
    routes: [chat, usage, media, openai],
    providers: [
      {
        family: 'inference',
        status: 'green',
        reason: '',
        id: 'chatgpt',
        label: 'ChatGPT',
        routes: [chat.name, usage.name, media.name],
      },
      {
        family: 'inference',
        status: 'green',
        reason: '',
        id: 'openai',
        label: 'OpenAI API',
        routes: [openai.name],
      },
    ],
    presets: [
      preset(chat, 'ChatGPT plan', 'chatgpt'),
      preset(usage, 'Usage', 'chatgpt'),
      preset(media, 'Media', 'chatgpt'),
      preset(openai, 'OpenAI API'),
    ],
    callers: [
      {
        id: 'a',
        name: 'jarvis',
        created: 1,
        revoked: false,
        grants: [chat.name],
        created_by: '',
        external_id: '',
      },
      {
        id: 'b',
        name: 'revoked',
        created: 1,
        revoked: true,
        grants: [usage.name],
        created_by: '',
        external_id: '',
      },
    ],
    ...overrides,
  };
}

function fixture(state = snapshot()) {
  const api = new ProxyClient(vi.fn());
  const config = vi.spyOn(api, 'config').mockResolvedValue();
  const context: UIContext = {
    run: async (operation) => {
      await operation();
      return true;
    },
    refresh: vi.fn().mockResolvedValue(undefined),
    announce: vi.fn(),
    navigate: vi.fn(),
    protectToken: vi.fn(),
  };
  const root = routesView(state, api, context);
  document.body.append(root);
  return { root, config, context };
}

afterEach(() => document.body.replaceChildren());

it('shows one row per provider with one approval summary and header Manage', () => {
  const { root } = fixture();
  const chat = getByRole(root, 'article', { name: 'ChatGPT' });
  const api = getByRole(root, 'article', { name: 'OpenAI API' });
  expect(root.querySelectorAll('.provider-card')).toHaveLength(2);
  expect(chat.querySelectorAll('.provider-route-line')).toHaveLength(3);
  expect(api.querySelectorAll('.provider-route-line')).toHaveLength(1);
  expect(chat.querySelectorAll('.account-controls')).toHaveLength(1);
  expect(chat.querySelector('.provider-manage')).not.toHaveAttribute('open');
  expect(chat.querySelector('.provider-manage > summary')).toHaveTextContent('Manage');
  expect(api.querySelector('.provider-manage > summary')).toHaveTextContent('Manage');
  // Approval is summarized once for the provider instead of on each route line.
  expect(chat.querySelectorAll('.provider-approval-summary')).toHaveLength(1);
  expect(chat.querySelector('.provider-approval-summary')).toHaveTextContent('No clients approved');
  expect(chat.querySelector('.provider-approval-summary')).toHaveTextContent(
    '1 client needs approval',
  );
  for (const line of chat.querySelectorAll('.provider-route-line'))
    expect(line).not.toHaveTextContent(/jarvis|revoked/);
  expect(chat.querySelector('.provider-manage [aria-label="Edit chatgpt"]')).not.toBeVisible();
});

it('adds a missing companion to its provider without changing other routes or approvals', async () => {
  const state = snapshot({ routes: [chat, openai] });
  state.providers[0].routes = [chat.name];
  state.presets![0].companions = [usage.name];
  const { root, config, context } = fixture(state);
  const manage = root.querySelector<HTMLDetailsElement>('.provider-manage')!;
  manage.open = true;
  fireEvent.click(getByRole(manage, 'button', { name: 'Add Usage route' }));
  const form = manage.querySelector<HTMLElement>('.route-form')!;
  expect(getByLabelText(form, 'Name')).toHaveValue(usage.name);
  expect(getByLabelText(form, 'Trusted account')).toHaveValue(chat.account);
  fireEvent.click(getByRole(form, 'button', { name: 'Save route' }));
  await waitFor(() => expect(config).toHaveBeenCalledOnce());
  const [loaded, routes] = config.mock.calls[0];
  expect(loaded).toBe(state);
  expect(routes.slice(0, 2)).toEqual([chat, openai]);
  expect(routes[2]).toMatchObject({
    name: usage.name,
    profile: usage.name,
    provider: 'chatgpt',
    account: chat.account,
  });
  expect(context.refresh).toHaveBeenCalledWith(`route:${usage.name}`);
});

it('renders only providers and routes listed by the server', () => {
  const groups = providerGroups(
    snapshot({
      providers: [
        {
          family: 'inference',
          status: 'green',
          reason: '',
          id: 'chatgpt',
          label: 'ChatGPT',
          routes: [chat.name],
        },
      ],
    }),
  );
  expect(groups.map((group) => group.routes.map(({ name }) => name))).toEqual([[chat.name]]);
});

it('uses the server family, status, reason and route capabilities with independent pagination', () => {
  const inference = Array.from({ length: 11 }, (_, index) => {
    const id = `infer-${index + 1}`;
    return { ...route(id, openai.upstream), serves: [`providers.${id}.base_url`] };
  });
  const exa = {
    ...route('exa-mcp', 'https://mcp.exa.ai'),
    provider: 'exa',
    profile: 'exa-mcp',
    serves: ['search: exa_mcp'],
  };
  const jina = { ...route('jina', 'https://api.jina.ai'), serves: ['embed: jina'] };
  const groups: ProxyState['providers'] = [
    ...inference
      .map(
        (r) =>
          ({
            id: r.provider,
            label: r.name,
            routes: [r.name],
            family: 'inference',
            status: 'green',
            reason: '',
          }) as const,
      )
      .map((p) => ({ ...p, routes: [...p.routes] })),
    {
      id: 'exa',
      label: 'Exa',
      family: 'search',
      status: 'red',
      reason: 'Sign-in needed',
      routes: [exa.name],
    },
    {
      id: 'jina',
      label: 'Jina',
      family: 'embeddings',
      status: 'orange',
      reason: 'Partly disabled',
      routes: [jina.name],
    },
  ];
  const { root } = fixture(snapshot({ routes: [...inference, exa, jina], providers: groups }));
  const inferenceSection = getByRole(root, 'region', { name: 'Inference' });
  const search = getByRole(root, 'region', { name: 'Search' });
  expect(queryByRole(root, 'region', { name: 'Media' })).toBeNull();
  expect(getByRole(root, 'region', { name: 'Embeddings' })).toBeVisible();
  expect(inferenceSection.querySelectorAll('article')).toHaveLength(10);
  fireEvent.click(getByRole(inferenceSection, 'button', { name: 'Next' }));
  expect(inferenceSection.querySelectorAll('article')).toHaveLength(1);
  expect(getByRole(search, 'heading', { name: 'exa' })).toBeVisible();
  expect(search.querySelector('article')).toHaveTextContent('Sign-in needed');
  expect(getByRole(search, 'img', { name: 'Status: red, Sign-in needed' })).toHaveClass(
    'provider-dot-red',
  );
  expect(search.querySelector('.provider-route-line code')).toHaveTextContent('/exa-mcp/');
  expect(search.querySelector('.provider-route-line')).toHaveTextContent('search: exa_mcp');
});
