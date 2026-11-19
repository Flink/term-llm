import { afterEach, expect, it, vi } from 'vitest';
import { fireEvent, getByLabelText, getByRole, waitFor } from '@testing-library/preact';
import { ProxyClient, type ProxyRoute, type ProxyState } from '../src/api/proxy-client';
import type { UIContext } from './dom';
import { providerGroups } from './provider-groups';
import { routesView } from './routes';

const auth = { header: 'Authorization', prefix: 'Bearer ' };
const route = (name: string, upstream: string): ProxyRoute => ({
  name,
  local_prefix: `/${name}/`,
  upstream,
  caller_auth: auth,
  enabled: true,
  allow_insecure_http: false,
  set_headers: {},
});
const chat = { ...route('chatgpt', 'https://chatgpt.com/backend-api/codex'), account: 'personal' };
const usage = {
  ...route('chatgpt-backend', 'https://chatgpt.com/backend-api'),
  account: 'personal',
};
const media = { ...route('chatgpt-openai', 'https://api.openai.com/v1'), account: 'personal' };
const openai = route('openai', media.upstream);
const preset = (route: ProxyRoute, label: string, account_kind?: 'chatgpt') => ({
  id: route.name,
  label,
  upstream: route.upstream,
  auth,
  account_kind,
});

function snapshot(overrides: Partial<ProxyState> = {}): ProxyState {
  return {
    auth: 'token',
    mode: 'ui',
    revision: 7,
    csrf: 'csrf',
    admin_keys: [],
    accounts: [{ name: 'personal', kind: 'chatgpt', state: 'ready', generation: 1, updated: 0 }],
    secrets: [],
    routes: [chat, usage, media, openai],
    providers: [
      { id: 'chatgpt', label: 'ChatGPT', routes: [chat.name, usage.name, media.name] },
      { id: 'openai', label: 'OpenAI API', routes: [openai.name] },
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

it('persists an added route under its provider without changing other routes or grants', async () => {
  const { root, config, context } = fixture();
  const manage = root.querySelector<HTMLDetailsElement>('.provider-manage')!;
  manage.open = true;
  fireEvent.click(getByRole(manage, 'button', { name: 'Add route to ChatGPT' }));
  const form = manage.querySelector<HTMLElement>('.route-form')!;
  fireEvent.input(getByLabelText(form, 'Name'), { target: { value: 'extra' } });
  fireEvent.input(getByLabelText(form, 'Upstream base URL'), {
    target: { value: 'https://example.test' },
  });
  fireEvent.click(getByRole(form, 'button', { name: 'Save route' }));
  await waitFor(() => expect(config).toHaveBeenCalledOnce());
  const [revision, routes] = config.mock.calls[0];
  expect(revision).toBe(7);
  expect(routes.slice(0, 3)).toEqual(
    [chat, usage, media].map((r) => ({ ...r, provider: 'chatgpt' })),
  );
  expect(routes[3]).toBe(openai);
  expect(routes[4]).toMatchObject({ name: 'extra', provider: 'chatgpt', local_prefix: '/extra/' });
  expect(context.refresh).toHaveBeenCalledWith('route:extra');
});

it('keeps unmatched and partially projected legacy routes independently manageable', () => {
  const groups = providerGroups(
    snapshot({
      providers: [{ id: 'chatgpt', label: 'ChatGPT', routes: [chat.name, chat.name, 'unknown'] }],
    }),
  );
  expect(groups.map((group) => group.routes.map(({ name }) => name))).toEqual([
    [chat.name],
    [usage.name],
    [media.name],
    [openai.name],
  ]);
});
