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
  type ProxyRoute,
  type ProxyRouteRule,
  type ProxyState,
} from '../src/api/proxy-client';
import type { UIContext } from './dom';
import { routeAccess } from './route-access';

const route: ProxyRoute = {
  name: 'openai',
  local_prefix: '/openai/',
  upstream: 'https://api.openai.com/v1',
  enabled: true,
  allow_insecure_http: false,
  caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
  set_headers: {},
  billing: 'pay_as_you_go',
};
const chatgptRoute: ProxyRoute = {
  ...route,
  name: 'chatgpt',
  local_prefix: '/chatgpt/',
  upstream: 'https://chatgpt.com/backend-api/codex',
  account: 'subscription',
};
const claudeAccount: ProxyAccount = {
  name: 'claude-slot',
  kind: 'claude-code',
  state: 'native_login',
  generation: 1,
  updated: 1,
};
const claudeRoute: ProxyRoute = {
  ...route,
  name: 'claude',
  account: claudeAccount.name,
  local_prefix: '/claude/',
  upstream: 'https://api.anthropic.com',
};
const rules: ProxyRouteRule[] = [
  {
    path_prefix: '/',
    upstream: 'https://opencode.ai/zen/go/v1',
    caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
    set_headers: {},
  },
  {
    path_prefix: '/messages/',
    upstream: 'https://opencode.ai/zen/go',
    caller_auth: { header: 'x-api-key', prefix: '' },
    set_headers: {},
  },
];
const unifiedRoute: ProxyRoute = {
  ...route,
  name: 'opencode',
  local_prefix: '/opencode/',
  upstream: '',
  caller_auth: { header: '', prefix: '' },
  rules,
};

function caller(overrides: Partial<ProxyCaller> = {}): ProxyCaller {
  return {
    id: 'laptop',
    name: 'Laptop',
    created: 1,
    revoked: false,
    grants: [route.name],
    created_by: '',
    external_id: '',
    ...overrides,
  };
}

function snapshot(overrides: Partial<ProxyState> = {}): ProxyState {
  return {
    auth: 'token',
    mode: 'ui',
    revision: 7,
    routes: [route],
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

function fixture(state = snapshot(), overrides: Partial<UIContext> = {}) {
  const api = new ProxyClient(vi.fn());
  const updateCaller = vi.spyOn(api, 'updateCaller').mockResolvedValue();
  const createCaller = vi.spyOn(api, 'createCaller');
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
    ...overrides,
  };
  const root = routeAccess(state.routes[0], state, api, context);
  document.body.append(root);
  return {
    api,
    root,
    context,
    announce,
    refresh,
    protectToken,
    updateCaller,
    createCaller,
  };
}

function open(root: HTMLElement) {
  fireEvent.click(getByRole(root, 'button', { name: /(Grant|Manage) access for route/ }));
}

it('offers Grant access when no active client holds the route and expands inline setup', () => {
  const { root } = fixture();
  const toggle = getByRole(root, 'button', { name: 'Grant access for route openai' });
  expect(toggle).toHaveAttribute('aria-expanded', 'false');
  expect(toggle).toHaveTextContent('Grant access');
  expect(root).toHaveTextContent('No clients yet.');
  expect(root.querySelector<HTMLElement>('.route-access-panel')!.hidden).toBe(true);

  fireEvent.click(toggle);
  expect(toggle).toHaveAttribute('aria-expanded', 'true');
  expect(root.querySelector<HTMLElement>('.route-access-panel')!.hidden).toBe(false);
  expect(root).toHaveTextContent('None.');
  expect(root).not.toHaveTextContent('Add existing client');
  expect(queryByLabelText(root, 'Client')).toBeNull();
  expect(getByRole(root, 'heading', { name: 'Clients' })).toHaveFocus();
  const name = getByLabelText(root, 'Client name');
  fireEvent.input(name, { target: { value: 'Phone' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Close' }));
  expect(toggle).toHaveFocus();
  expect(root.querySelector<HTMLElement>('.route-access-panel')!.hidden).toBe(true);
});

it('counts only active clients and lists each grant plus its other routes before removal', async () => {
  const active = caller({ id: 'lap', name: 'Laptop', grants: [route.name, 'docs'] });
  const revoked = caller({ id: 'old', name: 'Old laptop', revoked: true });
  const state = snapshot({ callers: [active, revoked] });
  const { root, updateCaller, refresh, announce } = fixture(state);
  expect(getByRole(root, 'button', { name: 'Manage access for route openai' })).toHaveTextContent(
    'Manage access',
  );
  expect(root.querySelector('.route-access-status')).toHaveTextContent('Laptop');
  open(root);

  const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
  const remove = getByRole(root, 'button', { name: /Remove Laptop’s access to openai/ });
  fireEvent.click(remove);
  expect(updateCaller).not.toHaveBeenCalled();
  fireEvent.click(remove);
  await waitFor(() => expect(updateCaller).toHaveBeenCalledWith('lap', ['docs'], 7));
  expect(confirm).toHaveBeenLastCalledWith(expect.stringContaining('stay valid'));
  await waitFor(() => expect(refresh).toHaveBeenCalledWith('route:openai'));
  expect(announce).toHaveBeenLastCalledWith(expect.stringContaining('other grants are unchanged'));
  // The revoked client is neither counted nor offered another grant.
  expect(root).not.toHaveTextContent('Old laptop');
});

it('grants an existing client this route without reissuing its token or dropping other grants', async () => {
  const docs = caller({ id: 'docs', name: 'Docs app', grants: ['docs'] });
  const revoked = caller({ id: 'retired', name: 'Retired', revoked: true, grants: [] });
  const { root, updateCaller, refresh, announce } = fixture(snapshot({ callers: [docs, revoked] }));
  open(root);
  const choice = getByLabelText(root, 'Client') as HTMLSelectElement;
  expect([...choice.options].map((option) => option.textContent)).toEqual([
    'Choose a client',
    'Docs app — docs',
  ]);
  expect(choice.value).toBe('');
  fireEvent.click(getByRole(root, 'button', { name: 'Grant access' }));
  await waitFor(() => expect(announce).toHaveBeenCalledWith('Choose an existing client.'));
  expect(updateCaller).not.toHaveBeenCalled();
  fireEvent.change(choice, { target: { value: 'docs' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Grant access' }));
  await waitFor(() => expect(updateCaller).toHaveBeenCalledWith('docs', ['docs', 'openai'], 7));
  expect(refresh).toHaveBeenCalledWith('route:openai');
  expect(announce).toHaveBeenLastCalledWith(
    expect.stringContaining('Granted Docs app access to openai'),
  );
});

it('offers one click to grant missing companion routes', async () => {
  const laptop = caller({ id: 'lap', name: 'Laptop', grants: ['chatgpt', 'venice'] });
  const state = snapshot({
    routes: [
      chatgptRoute,
      { ...chatgptRoute, name: 'chatgpt-backend', local_prefix: '/chatgpt-backend/' },
    ],
    presets: [
      { id: 'chatgpt', companions: ['chatgpt-backend', 'chatgpt-openai'] },
    ] as ProxyState['presets'],
    callers: [laptop],
  });
  const { root, updateCaller, refresh } = fixture(state);
  open(root);
  // chatgpt-openai does not exist as a route, so it is not offered.
  fireEvent.click(getByRole(root, 'button', { name: 'Grant Laptop access to chatgpt-backend' }));
  await waitFor(() =>
    expect(updateCaller).toHaveBeenCalledWith('lap', ['chatgpt', 'venice', 'chatgpt-backend'], 7),
  );
  expect(refresh).toHaveBeenCalledWith('route:chatgpt');
});

it('creates a route-scoped client, shows the token once, and guards dismissal', async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
  const storageWrite = vi.spyOn(Storage.prototype, 'setItem');
  const issued = caller({ id: 'phone', name: 'Phone', grants: [route.name] });
  const { root, createCaller, refresh, protectToken, announce } = fixture();
  createCaller.mockResolvedValue({ token: 'sk-tlp-once-only', caller: issued });
  // The first refresh is the preserveView reconciliation; it must not replace
  // the one-time token with a re-rendered view.
  refresh.mockResolvedValueOnce(snapshot({ callers: [issued] }));
  open(root);
  fireEvent.input(getByLabelText(root, 'Client name'), { target: { value: 'Phone' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Create client' }));

  const token = await waitFor(() => getByLabelText(root, 'Client token') as HTMLInputElement);
  expect(createCaller).toHaveBeenCalledExactlyOnceWith('Phone', ['openai'], 7);
  expect(token.value).toBe('sk-tlp-once-only');
  expect(token.readOnly).toBe(true);
  expect(token.className).toBe('caller-token');
  expect(token).toHaveFocus();
  expect(protectToken.mock.calls).toEqual([[true, true], [true]]);
  expect(getByRole(root, 'button', { name: 'Manage access for route openai' })).toBeDisabled();
  // The secret is never repeated in guidance text, markup, or browser storage.
  expect(root.textContent).not.toContain('sk-tlp-once-only');
  expect(root.querySelector('pre')).toHaveTextContent('$TOKEN');
  expect(root).toHaveTextContent(`Base URL: ${location.origin}/openai/`);
  expect(root).toHaveTextContent('set TOKEN in your shell to this client token');
  expect(root).toHaveTextContent('Do not set a global HTTP_PROXY or HTTPS_PROXY');
  expect(storageWrite).not.toHaveBeenCalled();
  expect(announce).toHaveBeenLastCalledWith(expect.stringContaining('Copy the token now'));

  fireEvent.click(getByRole(root, 'button', { name: 'Copy token' }));
  await waitFor(() => expect(writeText).toHaveBeenCalledWith('sk-tlp-once-only'));

  // A successful reconciliation must not replace the only copy of the token.
  await waitFor(() =>
    expect(root.querySelector('.route-access-status')).toHaveTextContent('Phone'),
  );
  expect(token.value).toBe('sk-tlp-once-only');

  fireEvent.click(getByRole(root, 'button', { name: 'I saved it — dismiss' }));
  await waitFor(() => expect(refresh).toHaveBeenCalledWith('caller:phone'));
  expect(token.value).toBe('');
  expect(protectToken).toHaveBeenLastCalledWith(false);
  expect(getByRole(root, 'button', { name: 'Manage access for route openai' })).toBeEnabled();
  expect(root).toHaveTextContent('Phone');
});

async function issueRouteToken(selected: ProxyRoute): Promise<HTMLElement> {
  const { root, createCaller } = fixture(snapshot({ routes: [selected] }));
  createCaller.mockResolvedValue({
    token: 'sk-tlp-route-scoped',
    caller: caller({ id: 'phone', name: 'Phone', grants: [selected.name] }),
  });
  open(root);
  fireEvent.input(getByLabelText(root, 'Client name'), { target: { value: 'Phone' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Create client' }));
  await waitFor(() => getByLabelText(root, 'Client token'));
  return root;
}

it.each([
  chatgptRoute,
  { ...route, name: 'venice', local_prefix: '/venice/', upstream: 'https://api.venice.ai/api/v1' },
])('shows safe term-llm YAML alongside curl for canonical $name', async (selected) => {
  const root = await issueRouteToken(selected);
  expect(getByRole(root, 'heading', { name: 'term-llm client configuration' })).toBeVisible();
  expect(root.querySelector('pre code')?.textContent).toBe(
    `proxy:\n  url: ${location.origin}\n  token: file:///run/secrets/proxy-client-token\n  providers: [${selected.name}]`,
  );
  expect(root).toHaveTextContent('Other providers stay direct.');
  expect(root).toHaveTextContent(`Base URL: ${location.origin}${selected.local_prefix}`);
  expect(root.querySelector('pre')).toHaveTextContent('$TOKEN');
  expect(root.textContent).not.toContain('sk-tlp-route-scoped');
  if (selected.name === 'chatgpt') {
    expect(root).toHaveTextContent('grant those routes to the same client too');
    expect(root).toHaveTextContent('chatgpt-backend or chatgpt-openai');
  }
});

it.each([
  { ...chatgptRoute, name: 'family', local_prefix: '/family/' },
  { ...chatgptRoute, local_prefix: '/custom/' },
  { ...chatgptRoute, upstream: 'https://custom.example/codex' },
])(
  'keeps generic guidance for noncanonical $name ($local_prefix → $upstream)',
  async (selected) => {
    const root = await issueRouteToken(selected);
    expect(queryByRole(root, 'heading', { name: 'term-llm client configuration' })).toBeNull();
    expect(root).toHaveTextContent(`Base URL: ${location.origin}${selected.local_prefix}`);
    expect(root.querySelector('pre')).toHaveTextContent('$TOKEN');
  },
);

it('points Claude Code routes at the provider launcher instead of generic connection details', () => {
  const state = snapshot({
    routes: [claudeRoute],
    accounts: [claudeAccount],
    callers: [caller({ id: 'mac', name: 'Mac', grants: ['claude'] })],
  });
  const { root, createCaller } = fixture(state);
  open(root);
  expect(root).toHaveTextContent(
    'Claude Code connects through the device launcher, not a generic API base URL.',
  );
  expect(root).toHaveTextContent('Use Add device in Provider setup');
  expect(queryByLabelText(root, 'Client name')).toBeNull();
  expect(queryByRole(root, 'button', { name: 'Create client' })).toBeNull();
  expect(queryByLabelText(root, 'Client')).toBeNull();
  expect(root.querySelector('pre')).toBeNull();
  expect(root).not.toHaveTextContent(`Base URL: ${location.origin}`);
  // Existing devices stay visible and revocable without touching other grants.
  expect(getByRole(root, 'button', { name: /Remove Mac’s access to claude/ })).toBeVisible();
  expect(createCaller).not.toHaveBeenCalled();
});

it('explains file-managed routes and Claude launchers without offering a generic connection', () => {
  const state = snapshot({ mode: 'file', routes: [claudeRoute], accounts: [claudeAccount] });
  const { root } = fixture(state);
  open(root);
  expect(root).toHaveTextContent('This route and its Claude account are file-managed.');
  expect(root).toHaveTextContent('Grants stay editable');
  expect(root.querySelector('pre')).toBeNull();
});

it('does not point a disabled route at an unavailable device launcher', () => {
  const state = snapshot({
    routes: [{ ...claudeRoute, enabled: false }],
    accounts: [claudeAccount],
  });
  const { root } = fixture(state);
  open(root);
  expect(root).toHaveTextContent('This route is disabled, so its device launcher is unavailable.');
  expect(root).toHaveTextContent('Enable it to issue a one-time launcher command');
  expect(root).not.toHaveTextContent('Use Add device in Provider setup');
});

it('keeps existing grants on a disabled route without promising new requests', () => {
  const state = snapshot({ routes: [{ ...route, enabled: false }], callers: [caller()] });
  const { root } = fixture(state);
  open(root);
  expect(root).toHaveTextContent(
    'This route is disabled. Grants are kept; new requests are blocked until you enable it.',
  );
  expect(root).toHaveTextContent('Laptop');
});

it('shows every subpath header and one shared token for a unified route', async () => {
  const { root, createCaller } = fixture(snapshot({ routes: [unifiedRoute] }));
  createCaller.mockResolvedValue({
    token: 'sk-tlp-unified',
    caller: caller({ id: 'phone', name: 'Phone', grants: ['opencode'] }),
  });
  open(root);
  fireEvent.input(getByLabelText(root, 'Client name'), { target: { value: 'Phone' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Create client' }));
  await waitFor(() => expect(getByLabelText(root, 'Client token')).toBeTruthy());
  expect(root).toHaveTextContent('/opencode/messages/ · client x-api-key');
  expect(root).toHaveTextContent('one client token and one grant cover every subpath');
  expect(root.querySelector('pre')).toHaveTextContent(`${location.origin}/opencode/`);
  expect(root.querySelector('pre')).toHaveTextContent('$TOKEN');
});

it('refuses token issuance, grants, and removals while another token is on screen', async () => {
  const { root, createCaller, updateCaller, refresh, announce } = fixture(
    snapshot({ callers: [caller()] }),
    { hasProtectedToken: () => true },
  );
  open(root);
  fireEvent.input(getByLabelText(root, 'Client name'), { target: { value: 'Phone' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Create client' }));
  expect(createCaller).not.toHaveBeenCalled();
  expect(announce).toHaveBeenCalledWith(
    'Save or dismiss the open one-time client token before creating another client.',
  );

  vi.spyOn(window, 'confirm').mockReturnValue(true);
  fireEvent.click(getByRole(root, 'button', { name: /Remove Laptop’s access to openai/ }));
  expect(updateCaller).not.toHaveBeenCalled();
  expect(announce).toHaveBeenLastCalledWith(
    'Save or dismiss the open one-time client token before removing access.',
  );
  expect(refresh).not.toHaveBeenCalled();
});

it('writes grants in passkey mode without a second passkey prompt', async () => {
  const { api, root, updateCaller } = fixture(
    snapshot({ auth: 'passkey', callers: [caller({ grants: ['openai', 'docs'] })] }),
  );
  const begin = vi.spyOn(api, 'beginAssertion');
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  open(root);
  fireEvent.click(getByRole(root, 'button', { name: /Remove Laptop’s access to openai/ }));
  await waitFor(() => expect(updateCaller).toHaveBeenCalledWith('laptop', ['docs'], 7));
  expect(begin).not.toHaveBeenCalled();
});

it('refreshes route state without replaying a stale grant write', async () => {
  const { root, updateCaller, refresh, announce } = fixture(snapshot({ callers: [caller()] }));
  updateCaller.mockRejectedValueOnce(
    new ProxyAPIError(409, 'Route configuration changed; refresh before granting access'),
  );
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  open(root);
  fireEvent.click(getByRole(root, 'button', { name: /Remove Laptop’s access to openai/ }));
  await waitFor(() => expect(refresh).toHaveBeenCalledWith('route:openai'));
  expect(updateCaller).toHaveBeenCalledOnce();
  expect(announce).toHaveBeenCalledWith(
    'Route configuration changed; refresh before granting access',
  );
});
