import { afterEach, expect, it, vi } from 'vitest';
import {
  fireEvent,
  getByLabelText,
  getByRole,
  queryByRole,
  waitFor,
} from '@testing-library/preact';
import {
  ProxyClient,
  type ProxyAccount,
  type ProxyCaller,
  type ProxyProvider,
  type ProxyRoute,
  type ProxyState,
} from '../src/api/proxy-client';
import type { UIContext } from './dom';
import { routesView } from './routes';

const veniceRoute: ProxyRoute = {
  name: 'venice',
  local_prefix: '/venice/',
  upstream: 'https://api.venice.ai/api/v1',
  enabled: true,
  allow_insecure_http: false,
  caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
  set_headers: { Authorization: { secret: 'venice-key', prefix: 'Bearer ' } },
};

const chatgptRoutes: ProxyRoute[] = [
  {
    name: 'chatgpt',
    local_prefix: '/chatgpt/',
    upstream: 'https://chatgpt.com/backend-api/codex',
    enabled: true,
    allow_insecure_http: false,
    account: 'chatgpt',
    caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
    set_headers: {},
  },
  {
    name: 'chatgpt-backend',
    local_prefix: '/chatgpt-backend/',
    upstream: 'https://chatgpt.com/backend-api',
    enabled: true,
    allow_insecure_http: false,
    account: 'chatgpt',
    caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
    set_headers: {},
  },
  {
    name: 'chatgpt-openai',
    local_prefix: '/chatgpt-openai/',
    upstream: 'https://api.openai.com/v1',
    enabled: true,
    allow_insecure_http: false,
    account: 'chatgpt',
    caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
    set_headers: {},
  },
];

const claudeRoute: ProxyRoute = {
  name: 'claude-code',
  local_prefix: '/claude-code/',
  upstream: 'https://api.anthropic.com',
  enabled: true,
  allow_insecure_http: false,
  account: 'claude-code',
  caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
  set_headers: {},
};

const agyRoute: ProxyRoute = {
  name: 'agy-bin',
  local_prefix: '/agy-bin/',
  upstream: 'https://daily-cloudcode-pa.googleapis.com',
  enabled: true,
  allow_insecure_http: false,
  account: 'agy-bin',
  caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
  set_headers: {},
};

const accounts: ProxyAccount[] = [
  { name: 'chatgpt', kind: 'chatgpt', state: 'ready', generation: 1, updated: 1 },
  {
    name: 'claude-code',
    kind: 'claude-code',
    auth_mode: 'shared_vault',
    state: 'ready',
    generation: 1,
    updated: 1,
  },
  {
    name: 'agy-bin',
    kind: 'agy-bin',
    auth_mode: 'shared_vault',
    state: 'needs_login',
    generation: 1,
    updated: 1,
  },
];

const callers: ProxyCaller[] = [
  {
    id: 'c1',
    name: 'arch2',
    created: 1,
    revoked: false,
    grants: ['venice', 'chatgpt', 'chatgpt-backend', 'chatgpt-openai', 'claude-code'],
    created_by: '',
    external_id: '',
  },
];

const providers: ProxyProvider[] = [
  { id: 'venice', label: 'Venice', routes: ['venice'] },
  { id: 'claude-code', label: 'Claude Code plan', routes: ['claude-code'] },
  {
    id: 'chatgpt',
    label: 'ChatGPT',
    routes: ['chatgpt', 'chatgpt-backend', 'chatgpt-openai'],
  },
];

function setup(overrides: Partial<ProxyState> = {}) {
  const api = new ProxyClient(vi.fn());
  const config = vi.spyOn(api, 'config').mockResolvedValue();
  const clearNative = vi.spyOn(api, 'clearNative').mockResolvedValue();
  const setupNative = vi.spyOn(api, 'setupNative').mockResolvedValue({
    revision: 8,
    url: 'https://proxy.example/claude-code/',
    token: 'scoped-canary-worker',
    caller_id: 'w1',
  });
  const announce = vi.fn();
  const refresh = vi.fn().mockResolvedValue(undefined);
  let protectedToken = false;
  const context: UIContext = {
    run: async (action) => {
      try {
        await action();
        return true;
      } catch (error) {
        announce(error instanceof Error ? error.message : 'failed');
        return false;
      }
    },
    announce,
    refresh,
    protectToken: (shown) => {
      protectedToken = shown;
    },
    hasProtectedToken: () => protectedToken,
    navigate: vi.fn(),
  };
  const state: ProxyState = {
    auth: 'token',
    mode: 'ui',
    revision: 7,
    csrf: 'csrf',
    routes: [veniceRoute, claudeRoute, ...chatgptRoutes],
    providers,
    secrets: [{ name: 'venice-key', origin: 'https://api.venice.ai', source: 'stored' }],
    accounts,
    callers,
    admin_keys: [],
    ...overrides,
  };
  const root = routesView(state, api, context);
  document.body.append(root);
  return { root, api, config, clearNative, setupNative, announce, refresh };
}

function openProvider(root: HTMLElement, label: string): HTMLDetailsElement {
  const card = getByRole(root, 'article', { name: label });
  const manage = card.querySelector<HTMLDetailsElement>('details.provider-manage')!;
  manage.open = true;
  return manage;
}

afterEach(() => {
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

it('deletes a single-route API-key provider after confirmation and respects cancellation', async () => {
  const { root, config, clearNative, refresh, announce } = setup();
  const manage = openProvider(root, 'Venice');
  const remove = getByRole(manage, 'button', { name: 'Delete provider Venice' });
  const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);

  fireEvent.click(remove);
  expect(confirm).toHaveBeenCalledOnce();
  expect(config).not.toHaveBeenCalled();

  fireEvent.click(remove);
  await waitFor(() => expect(config).toHaveBeenCalledOnce());
  expect(confirm.mock.calls[0]?.[0]).toContain('Delete provider Venice?');
  expect(confirm.mock.calls[0]?.[0]).toContain('Removes route venice');
  expect(confirm.mock.calls[0]?.[0]).toContain('for arch2');
  expect(confirm.mock.calls[0]?.[0]).toContain('Stored API keys stay in Secrets.');
  expect(config).toHaveBeenCalledWith(7, [claudeRoute, ...chatgptRoutes]);
  expect(clearNative).not.toHaveBeenCalled();
  expect(refresh).toHaveBeenCalledOnce();
  expect(announce).toHaveBeenLastCalledWith(expect.stringContaining('Deleted provider Venice.'));
});

it('deletes every route of a multi-route provider in one revision-checked write', async () => {
  const { root, config, clearNative, refresh } = setup();
  const manage = openProvider(root, 'ChatGPT');
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);

  fireEvent.click(getByRole(manage, 'button', { name: 'Delete provider ChatGPT' }));
  await waitFor(() => expect(config).toHaveBeenCalledOnce());
  expect(confirm.mock.calls[0]?.[0]).toContain('Delete provider ChatGPT?');
  expect(confirm.mock.calls[0]?.[0]).toContain(
    'Removes 3 routes (chatgpt, chatgpt-backend, chatgpt-openai)',
  );
  expect(confirm.mock.calls[0]?.[0]).toContain(
    'Connected accounts stay under Unused accounts until removed.',
  );
  expect(config).toHaveBeenCalledWith(7, [veniceRoute, claudeRoute]);
  expect(clearNative).not.toHaveBeenCalled();
  expect(refresh).toHaveBeenCalledOnce();
});

it.each([
  ['Claude Code plan', 'claude', 'Claude', claudeRoute, 'claude-code'],
  ['Antigravity', 'agy', 'Antigravity', agyRoute, 'agy-bin'],
] as const)(
  'deletes native provider %s via clearNative after confirmation',
  async (label, launcher, shortName, route, id) => {
    const { root, config, clearNative, refresh, announce } = setup({
      routes: [veniceRoute, route],
      providers: [
        { id: 'venice', label: 'Venice', routes: ['venice'] },
        { id, label, routes: [route.name] },
      ],
    });
    const manage = openProvider(root, label);
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);

    fireEvent.click(getByRole(manage, 'button', { name: `Delete provider ${label}` }));
    await waitFor(() => expect(clearNative).toHaveBeenCalledExactlyOnceWith(launcher, 7));
    expect(confirm.mock.calls[0]?.[0]).toContain(`Delete provider ${label}?`);
    expect(confirm.mock.calls[0]?.[0]).toContain(
      `Removes all ${shortName} proxy routes, device access, and saved proxy sign-ins.`,
    );
    expect(config).not.toHaveBeenCalled();
    expect(refresh).toHaveBeenCalledOnce();
    expect(announce).toHaveBeenLastCalledWith(
      expect.stringContaining(`Deleted provider ${label}.`),
    );
  },
);

it('refuses provider deletion while a one-time launcher is displayed', async () => {
  const { root, clearNative, announce } = setup();
  const manage = openProvider(root, 'Claude Code plan');
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);

  fireEvent.click(getByRole(manage, 'button', { name: 'Add device' }));
  await waitFor(() => expect(getByLabelText(manage, 'Launcher command')).toBeVisible());

  fireEvent.click(getByRole(manage, 'button', { name: 'Delete provider Claude Code plan' }));
  expect(confirm).not.toHaveBeenCalled();
  expect(clearNative).not.toHaveBeenCalled();
  expect(announce).toHaveBeenLastCalledWith(
    'Finish saving or dismissing the one-time connection details first.',
  );
});

it('hides Delete provider in read-only file mode', () => {
  const { root } = setup({ mode: 'file' });
  for (const label of ['Venice', 'Claude Code plan', 'ChatGPT']) {
    const manage = openProvider(root, label);
    expect(queryByRole(manage, 'button', { name: `Delete provider ${label}` })).toBeNull();
  }
});

it('never renders nested disclosures inside Manage for any provider type', async () => {
  const { root } = setup();
  for (const label of ['Venice', 'Claude Code plan', 'ChatGPT']) {
    const manage = openProvider(root, label);
    expect(manage.querySelectorAll('details')).toHaveLength(0);
  }

  // Opening an inline route editor inside Manage still introduces zero nested disclosures.
  const veniceManage = openProvider(root, 'Venice');
  fireEvent.click(getByRole(veniceManage, 'button', { name: 'Edit venice' }));
  expect(veniceManage.querySelectorAll('details')).toHaveLength(0);
  expect(getByRole(veniceManage, 'button', { name: 'Delete route venice' })).toBeVisible();

  // Issuing a worker launcher inside Manage introduces zero nested disclosures.
  const claudeManage = openProvider(root, 'Claude Code plan');
  fireEvent.click(getByRole(claudeManage, 'button', { name: 'Add device' }));
  await waitFor(() => expect(getByLabelText(claudeManage, 'Launcher command')).toBeVisible());
  expect(claudeManage.querySelectorAll('details')).toHaveLength(0);
  const integration = claudeManage.querySelector<HTMLElement>('.native-integration')!;
  expect(getByLabelText(integration, 'Provider YAML')).toBeVisible();
});
