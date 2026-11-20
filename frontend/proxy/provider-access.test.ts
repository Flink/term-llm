import { afterEach, expect, it, vi } from 'vitest';
import {
  fireEvent,
  getByLabelText,
  getByRole,
  queryByLabelText,
  queryByRole,
  waitFor,
} from '@testing-library/preact';
import {
  ProxyAPIError,
  ProxyClient,
  type ProxyAccount,
  type ProxyCaller,
  type ProxyAdminRoute,
  type ProxyRouteRule,
  type ProxyState,
} from '../src/api/proxy-client';
import type { UIContext } from './dom';
import { providerAccess } from './provider-access';
import type { ProviderGroup } from './provider-groups';

const auth = { header: 'Authorization', prefix: 'Bearer ' };
const route = (name: string, upstream: string): ProxyAdminRoute => ({
  serves: [],
  profile: '',
  provider: name,
  name,
  local_prefix: `/${name}/`,
  upstream,
  enabled: true,
  allow_insecure_http: false,
  caller_auth: auth,
  set_headers: {},
  billing: 'pay_as_you_go',
});

const openaiRoute = route('openai', 'https://api.openai.com/v1');
const chatgptRoute: ProxyAdminRoute = {
  ...route('chatgpt', 'https://chatgpt.com/backend-api/codex'),
  account: 'subscription',
};
const chatgptBackendRoute: ProxyAdminRoute = {
  ...route('chatgpt-backend', 'https://chatgpt.com/backend-api'),
  account: 'subscription',
};
const chatgptMediaRoute: ProxyAdminRoute = {
  ...route('chatgpt-openai', 'https://api.openai.com/v1'),
  account: 'subscription',
};
const claudeAccount: ProxyAccount = {
  name: 'claude-slot',
  kind: 'claude-bin',
  state: 'ready',
  auth_mode: 'shared_vault',
  generation: 1,
  updated: 1,
};
const claudeRoute: ProxyAdminRoute = {
  ...route('claude', 'https://api.anthropic.com'),
  account: claudeAccount.name,
};
const rules: ProxyRouteRule[] = [
  {
    path_prefix: '/',
    upstream: 'https://opencode.ai/zen/go/v1',
    caller_auth: auth,
    set_headers: {},
  },
  {
    path_prefix: '/messages/',
    upstream: 'https://opencode.ai/zen/go',
    caller_auth: { header: 'x-api-key', prefix: '' },
    set_headers: {},
  },
];
const unifiedRoute: ProxyAdminRoute = {
  ...route('opencode', ''),
  caller_auth: { header: '', prefix: '' },
  rules,
};

const openaiGroup: ProviderGroup = {
  family: 'inference',
  status: 'green',
  reason: '',
  id: 'openai',
  label: 'OpenAI API',
  routes: [openaiRoute],
};
const chatgptGroup: ProviderGroup = {
  family: 'inference',
  status: 'green',
  reason: '',
  id: 'chatgpt',
  label: 'ChatGPT plan',
  routes: [chatgptRoute, chatgptBackendRoute, chatgptMediaRoute],
};
const claudeGroup: ProviderGroup = {
  family: 'inference',
  status: 'green',
  reason: '',
  id: 'claude',
  label: 'Claude Code',
  routes: [claudeRoute],
};

function caller(overrides: Partial<ProxyCaller> = {}): ProxyCaller {
  return {
    id: 'laptop',
    name: 'Laptop',
    created: 1,
    revoked: false,
    grants: [openaiRoute.name],
    created_by: '',
    external_id: '',
    ...overrides,
  };
}

function snapshot(group: ProviderGroup, overrides: Partial<ProxyState> = {}): ProxyState {
  return {
    defaults: null,
    providers: [],
    auth: 'token',
    mode: 'ui',
    revision: 7,
    routes: group.routes,
    secrets: [],
    callers: [],
    admin_keys: [],
    accounts: [],
    csrf: 'csrf',
    ...overrides,
  };
}

const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
afterEach(() => {
  document.body.replaceChildren();
  vi.restoreAllMocks();
  if (clipboardDescriptor) Object.defineProperty(navigator, 'clipboard', clipboardDescriptor);
  else Reflect.deleteProperty(navigator, 'clipboard');
});

function fixture(
  group: ProviderGroup,
  overrides: Partial<ProxyState> = {},
  contextOverrides: Partial<UIContext> = {},
) {
  const state = snapshot(group, overrides);
  const api = new ProxyClient(vi.fn());
  const createProviderCaller = vi.spyOn(api, 'createProviderCaller');
  const updateProviderApprovals = vi.spyOn(api, 'updateProviderApprovals').mockResolvedValue();
  const updateCaller = vi.spyOn(api, 'updateCaller').mockResolvedValue();
  const createCaller = vi.spyOn(api, 'createCaller').mockResolvedValue({
    token: 'unused',
    caller: caller(),
  });
  const announce = vi.fn();
  const protectToken = vi.fn();
  const refresh = vi.fn(async () => state as ProxyState | undefined);
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
    refresh,
    announce,
    navigate: vi.fn(),
    protectToken,
    hasProtectedToken: () => false,
    ...contextOverrides,
  };
  const root = providerAccess(group, state, api, context);
  document.body.append(root);
  return {
    api,
    root,
    context,
    announce,
    refresh,
    protectToken,
    createProviderCaller,
    updateProviderApprovals,
    updateCaller,
    createCaller,
  };
}

it('renders client approval controls directly without an extra expand toggle', () => {
  const { root } = fixture(openaiGroup);
  expect(root.querySelector('details')).toBeNull();
  expect(root.querySelector<HTMLElement>('.route-access-panel')!.hidden).toBe(false);
  expect(root).toHaveTextContent('No clients approved yet.');
  expect(root).not.toHaveTextContent('Add existing client');
  expect(queryByLabelText(root, 'Client')).toBeNull();
  expect(getByLabelText(root, 'Client name')).toBeVisible();
});

it('lists approved clients once and removes the whole provider approval without touching their token', async () => {
  const active = caller({ id: 'lap', name: 'Laptop', grants: [openaiRoute.name, 'docs'] });
  const revoked = caller({ id: 'old', name: 'Old laptop', revoked: true });
  const { root, updateProviderApprovals, updateCaller, refresh, announce } = fixture(openaiGroup, {
    callers: [active, revoked],
  });
  expect(root.querySelectorAll('.route-access-client')).toHaveLength(1);
  expect(root.querySelector('.route-access-client')).toHaveTextContent('Laptop');

  const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
  const remove = getByRole(root, 'button', { name: /Remove Laptop’s access to OpenAI API/ });
  fireEvent.click(remove);
  expect(updateProviderApprovals).not.toHaveBeenCalled();
  fireEvent.click(remove);
  await waitFor(() =>
    expect(updateProviderApprovals).toHaveBeenCalledWith(
      'lap',
      [{ id: 'openai', approved: false }],
      7,
    ),
  );
  expect(confirm).toHaveBeenLastCalledWith(
    expect.stringContaining('Access to other providers and its token stay unchanged.'),
  );
  await waitFor(() => expect(refresh).toHaveBeenCalledWith('route:openai'));
  expect(announce).toHaveBeenLastCalledWith('Removed Laptop’s access to OpenAI API.');
  expect(updateCaller).not.toHaveBeenCalled();
  // The revoked client is neither counted nor offered another approval.
  expect(root).not.toHaveTextContent('Old laptop');
});

it('approves every route of a provider for an existing client in one explicit write', async () => {
  const docs = caller({ id: 'docs', name: 'Docs app', grants: ['docs'] });
  const revoked = caller({ id: 'retired', name: 'Retired', revoked: true, grants: [] });
  const { root, createProviderCaller, updateProviderApprovals, refresh, announce } = fixture(
    chatgptGroup,
    { callers: [docs, revoked] },
  );
  const choice = getByLabelText(root, 'Client') as HTMLSelectElement;
  expect([...choice.options].map((option) => option.textContent)).toEqual([
    'Choose a client',
    'Docs app',
  ]);
  expect(choice.value).toBe('');
  fireEvent.click(getByRole(root, 'button', { name: 'Approve access' }));
  await waitFor(() => expect(announce).toHaveBeenCalledWith('Choose an existing client.'));
  expect(updateProviderApprovals).not.toHaveBeenCalled();
  fireEvent.change(choice, { target: { value: 'docs' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Approve access' }));
  await waitFor(() =>
    expect(updateProviderApprovals).toHaveBeenCalledWith(
      'docs',
      [{ id: 'chatgpt', approved: true }],
      7,
    ),
  );
  expect(refresh).toHaveBeenCalledWith('route:chatgpt');
  expect(announce).toHaveBeenLastCalledWith(
    'Approved Docs app for ChatGPT plan. Its token and other approvals are unchanged.',
  );
  // Approval covers the provider; it never reissues or rewrites the client.
  expect(createProviderCaller).not.toHaveBeenCalled();
});

it('shows legacy partial approval as needing approval and covers all routes only on an explicit click', async () => {
  const laptop = caller({ id: 'lap', name: 'Laptop', grants: ['chatgpt'] });
  const { root, updateProviderApprovals, refresh, announce } = fixture(chatgptGroup, {
    callers: [laptop],
  });
  expect(root.querySelector('.route-access-client')).toHaveTextContent('Laptop');
  expect(root.querySelector('.route-access-client')).toHaveTextContent('Needs approval');
  const choice = getByLabelText(root, 'Client') as HTMLSelectElement;
  expect([...choice.options].map((option) => option.textContent)).toEqual([
    'Choose a client',
    'Laptop (needs approval)',
  ]);
  // Existing route grants stay exactly as they are until the operator covers
  // the whole provider explicitly.
  expect(updateProviderApprovals).not.toHaveBeenCalled();
  fireEvent.change(choice, { target: { value: 'lap' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Approve access' }));
  await waitFor(() =>
    expect(updateProviderApprovals).toHaveBeenCalledWith(
      'lap',
      [{ id: 'chatgpt', approved: true }],
      7,
    ),
  );
  expect(refresh).toHaveBeenCalledWith('route:chatgpt');
  expect(announce).toHaveBeenLastCalledWith(
    'Approved Laptop for ChatGPT plan. Its token and other approvals are unchanged.',
  );
});

it('creates a provider-scoped client, shows the token once, and guards dismissal', async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
  const storageWrite = vi.spyOn(Storage.prototype, 'setItem');
  const issued = caller({ id: 'phone', name: 'Phone', grants: [openaiRoute.name] });
  const { root, createProviderCaller, updateProviderApprovals, refresh, protectToken, announce } =
    fixture(openaiGroup);
  createProviderCaller.mockResolvedValue({ token: 'sk-tlp-once-only', caller: issued });
  // The first refresh is the preserveView reconciliation; it must not replace
  // the one-time token with a re-rendered view.
  refresh.mockResolvedValueOnce(snapshot(openaiGroup, { callers: [issued] }));
  fireEvent.input(getByLabelText(root, 'Client name'), { target: { value: 'Phone' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Create client' }));

  const token = await waitFor(() => getByLabelText(root, 'Client token') as HTMLInputElement);
  expect(createProviderCaller).toHaveBeenCalledExactlyOnceWith('Phone', ['openai'], 7);
  expect(updateProviderApprovals).not.toHaveBeenCalled();
  expect(token.value).toBe('sk-tlp-once-only');
  expect(token.readOnly).toBe(true);
  expect(token.className).toBe('caller-token');
  expect(token).toHaveFocus();
  expect(protectToken.mock.calls).toEqual([[true, true], [true]]);
  // The secret is never repeated in guidance text, markup, or browser storage.
  expect(root.textContent).not.toContain('sk-tlp-once-only');
  expect(root.querySelector('pre:not(:has(code))')).toHaveTextContent('$TOKEN');
  expect(root).toHaveTextContent(`Base URL: ${location.origin}/openai/`);
  expect(root).toHaveTextContent('set TOKEN in your shell to this client token');
  expect(root).toHaveTextContent('Do not set a global HTTP_PROXY or HTTPS_PROXY');
  expect(root).toHaveTextContent('Access is limited to the current routes for OpenAI API.');
  expect(storageWrite).not.toHaveBeenCalled();
  expect(announce).toHaveBeenLastCalledWith(expect.stringContaining('Copy the token now'));

  fireEvent.click(getByRole(root, 'button', { name: 'Copy token' }));
  await waitFor(() => expect(writeText).toHaveBeenCalledWith('sk-tlp-once-only'));

  // A successful reconciliation must not replace the only copy of the token.
  await waitFor(() => expect(refresh).toHaveBeenCalledWith(undefined, true));
  expect(token.value).toBe('sk-tlp-once-only');

  fireEvent.click(getByRole(root, 'button', { name: 'I saved it — dismiss' }));
  await waitFor(() => expect(refresh).toHaveBeenCalledWith('caller:phone'));
  expect(token.value).toBe('');
  expect(protectToken).toHaveBeenLastCalledWith(false);
  expect(root).toHaveTextContent('Phone');
});

it('creates one provider-scoped client whose details cover every route in the group', async () => {
  const issued = caller({ id: 'phone', name: 'Phone', grants: ['chatgpt'] });
  const { root, createProviderCaller, updateProviderApprovals } = fixture(chatgptGroup);
  createProviderCaller.mockResolvedValue({ token: 'sk-tlp-chatgpt', caller: issued });
  fireEvent.input(getByLabelText(root, 'Client name'), { target: { value: 'Phone' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Create client' }));
  await waitFor(() => getByLabelText(root, 'Client token'));
  expect(createProviderCaller).toHaveBeenCalledExactlyOnceWith('Phone', ['chatgpt'], 7);
  expect(updateProviderApprovals).not.toHaveBeenCalled();
  expect(root).toHaveTextContent(`Base URL: ${location.origin}/chatgpt/`);
  expect(root).toHaveTextContent(`Base URL: ${location.origin}/chatgpt-backend/`);
  expect(root).toHaveTextContent(`Base URL: ${location.origin}/chatgpt-openai/`);
  expect(root).toHaveTextContent('Access is limited to the current routes for ChatGPT plan.');
});

async function issueProviderToken(
  selected: ProxyAdminRoute,
  label = selected.name,
): Promise<HTMLElement> {
  const group: ProviderGroup = {
    family: 'inference',
    status: 'green',
    reason: '',
    id: selected.name,
    label,
    routes: [selected],
  };
  const { root, createProviderCaller } = fixture(group);
  createProviderCaller.mockResolvedValue({
    token: 'sk-tlp-route-scoped',
    caller: caller({ id: 'phone', name: 'Phone', grants: [selected.name] }),
  });
  fireEvent.input(getByLabelText(root, 'Client name'), { target: { value: 'Phone' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Create client' }));
  await waitFor(() => getByLabelText(root, 'Client token'));
  return root;
}

it.each([
  [chatgptRoute, 'ChatGPT plan'],
  [route('venice', 'https://api.venice.ai/api/v1'), 'Venice'],
] as const)(
  'shows safe term-llm YAML alongside curl for canonical $name',
  async (selected, label) => {
    const root = await issueProviderToken(selected, label);
    expect(getByRole(root, 'heading', { name: 'term-llm client configuration' })).toBeVisible();
    expect(root.querySelector('pre code')?.textContent).toBe(
      `proxy:\n  url: ${location.origin}\n  token: file:///run/secrets/proxy-client-token\n  providers: [${selected.name}]`,
    );
    expect(root).toHaveTextContent('Other providers stay direct.');
    expect(root).toHaveTextContent(`Base URL: ${location.origin}${selected.local_prefix}`);
    expect(root.querySelector('pre:not(:has(code))')).toHaveTextContent('$TOKEN');
    expect(root.textContent).not.toContain('sk-tlp-route-scoped');
    expect(root).toHaveTextContent(`Access is limited to the current routes for ${label}.`);
  },
);

it.each([
  { ...chatgptRoute, name: 'family', local_prefix: '/family/' },
  { ...chatgptRoute, local_prefix: '/custom/' },
  { ...chatgptRoute, upstream: 'https://custom.example/codex' },
])('uses stored IDs even for noncanonical $name ($local_prefix → $upstream)', async (selected) => {
  const root = await issueProviderToken(selected);
  expect(root.querySelector('pre code')).toHaveTextContent(`providers: [${selected.name}]`);
  expect(root).toHaveTextContent(`Base URL: ${location.origin}${selected.local_prefix}`);
  expect(root.querySelector('pre:not(:has(code))')).toHaveTextContent('$TOKEN');
});

it('shows every subpath header and one shared token for a unified route', async () => {
  const group: ProviderGroup = {
    family: 'inference',
    status: 'green',
    reason: '',
    id: 'opencode',
    label: 'opencode',
    routes: [unifiedRoute],
  };
  const { root, createProviderCaller } = fixture(group);
  createProviderCaller.mockResolvedValue({
    token: 'sk-tlp-unified',
    caller: caller({ id: 'phone', name: 'Phone', grants: ['opencode'] }),
  });
  fireEvent.input(getByLabelText(root, 'Client name'), { target: { value: 'Phone' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Create client' }));
  await waitFor(() => expect(getByLabelText(root, 'Client token')).toBeTruthy());
  expect(createProviderCaller).toHaveBeenCalledExactlyOnceWith('Phone', ['opencode'], 7);
  expect(root).toHaveTextContent('/opencode/messages/ · client x-api-key');
  expect(root).toHaveTextContent('one client token and one grant cover every subpath');
  expect(root.querySelector('pre:not(:has(code))')).toHaveTextContent(
    `${location.origin}/opencode/`,
  );
  expect(root.querySelector('pre:not(:has(code))')).toHaveTextContent('$TOKEN');
});

it('points native Claude providers at the device launcher instead of a generic client form', () => {
  const state = {
    accounts: [claudeAccount],
    callers: [caller({ id: 'mac', name: 'Mac', grants: ['claude'] })],
  };
  const { root, createProviderCaller } = fixture(claudeGroup, state);
  expect(root).toHaveTextContent(
    'Claude Code uses a device launcher instead of a generic API URL. Use Add device below to connect a new device.',
  );
  expect(queryByLabelText(root, 'Client name')).toBeNull();
  expect(queryByRole(root, 'button', { name: 'Create client' })).toBeNull();
  expect(queryByLabelText(root, 'Client')).toBeNull();
  expect(root.querySelector('pre:not(:has(code))')).toBeNull();
  expect(root).not.toHaveTextContent(`Base URL: ${location.origin}`);
  // Existing devices stay visible and revocable without touching other approvals.
  expect(getByRole(root, 'button', { name: /Remove Mac’s access to Claude Code/ })).toBeVisible();
  expect(createProviderCaller).not.toHaveBeenCalled();
});

it('explains file-managed providers without offering generic connection details', () => {
  const { root } = fixture(openaiGroup, { mode: 'file' });
  expect(root).toHaveTextContent(
    'Routes are file-managed. Approvals are editable, but a configuration reload may drop incompatible grants.',
  );
  expect(root.querySelector('pre:not(:has(code))')).toBeNull();
});

it('warns that removing shared Claude access leaves the subscription and other clients intact', async () => {
  const { root, updateProviderApprovals, updateCaller, refresh } = fixture(claudeGroup, {
    accounts: [claudeAccount],
    callers: [caller({ id: 'mac', name: 'Mac', grants: ['claude'] })],
  });
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
  fireEvent.click(getByRole(root, 'button', { name: /Remove Mac’s access to Claude Code/ }));
  await waitFor(() =>
    expect(updateProviderApprovals).toHaveBeenCalledWith(
      'mac',
      [{ id: 'claude', approved: false }],
      7,
    ),
  );
  expect(confirm).toHaveBeenLastCalledWith(
    expect.stringContaining('The shared Claude sign-in and other clients’ access are unchanged.'),
  );
  await waitFor(() => expect(refresh).toHaveBeenCalledWith('route:claude'));
  expect(updateCaller).not.toHaveBeenCalled();
});

it('refuses creation, provider approval, and removal while another token is on screen', async () => {
  const { root, createProviderCaller, updateProviderApprovals, refresh, announce } = fixture(
    openaiGroup,
    {
      callers: [
        caller({ id: 'lap', name: 'Laptop' }),
        caller({ id: 'docs', name: 'Docs app', grants: ['docs'] }),
      ],
    },
    { hasProtectedToken: () => true },
  );
  fireEvent.input(getByLabelText(root, 'Client name'), { target: { value: 'Phone' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Create client' }));
  expect(createProviderCaller).not.toHaveBeenCalled();
  expect(announce).toHaveBeenCalledWith(
    'Save or dismiss the open one-time client token before creating another client.',
  );

  fireEvent.change(getByLabelText(root, 'Client'), { target: { value: 'docs' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Approve access' }));
  expect(updateProviderApprovals).not.toHaveBeenCalled();
  expect(announce).toHaveBeenLastCalledWith(
    'Save or dismiss the open one-time client token before approving provider access.',
  );

  vi.spyOn(window, 'confirm').mockReturnValue(true);
  fireEvent.click(getByRole(root, 'button', { name: /Remove Laptop’s access to OpenAI API/ }));
  expect(updateProviderApprovals).not.toHaveBeenCalled();
  expect(announce).toHaveBeenLastCalledWith(
    'Save or dismiss the open one-time client token before removing access.',
  );
  expect(refresh).not.toHaveBeenCalled();
});

it('writes provider approvals in passkey mode without a second passkey prompt', async () => {
  const { api, root, updateProviderApprovals, updateCaller } = fixture(openaiGroup, {
    auth: 'passkey',
    callers: [caller({ grants: ['openai', 'docs'] })],
  });
  const begin = vi.spyOn(api, 'beginAssertion');
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  fireEvent.click(getByRole(root, 'button', { name: /Remove Laptop’s access to OpenAI API/ }));
  await waitFor(() =>
    expect(updateProviderApprovals).toHaveBeenCalledWith(
      'laptop',
      [{ id: 'openai', approved: false }],
      7,
    ),
  );
  expect(begin).not.toHaveBeenCalled();
  expect(updateCaller).not.toHaveBeenCalled();
});

it('refreshes provider state without replaying a stale approval write', async () => {
  const { root, updateProviderApprovals, refresh, announce } = fixture(openaiGroup, {
    callers: [caller()],
  });
  updateProviderApprovals.mockRejectedValueOnce(
    new ProxyAPIError(409, 'Provider configuration changed; refresh before changing access'),
  );
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  fireEvent.click(getByRole(root, 'button', { name: /Remove Laptop’s access to OpenAI API/ }));
  await waitFor(() => expect(refresh).toHaveBeenCalledWith('route:openai'));
  expect(updateProviderApprovals).toHaveBeenCalledOnce();
  expect(announce).toHaveBeenCalledWith(
    'Provider configuration changed; refresh before changing access',
  );
});
