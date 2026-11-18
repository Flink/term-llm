import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { fireEvent, getByLabelText, getByRole, waitFor } from '@testing-library/preact';
import {
  ProxyAPIError,
  ProxyClient,
  type ProxyAccount,
  type ProxyRoute,
  type ProxyState,
} from '../src/api/proxy-client';
import type { PasskeyPlatform } from '../src/hub/platform/passkeys';
import { mountProxy } from './app';
import { providerOnboarding, providerRouteAction, clearClaudeSetup } from './provider-onboarding';
import type { UIContext } from './dom';
import { claudeLauncher, claudeProviderYAML, isClaudeRoute } from './claude';

const account: ProxyAccount = {
  name: 'native-slot',
  kind: 'claude-code',
  state: 'native_login',
  generation: 1,
  updated: 1,
};
const route: ProxyRoute = {
  name: 'my-manual-connection',
  account: account.name,
  local_prefix: '/native/',
  upstream: 'https://api.anthropic.com',
  enabled: true,
  allow_insecure_http: false,
  caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
  set_headers: {},
  billing: 'plan',
};
const other: ProxyRoute = {
  ...route,
  name: 'openai',
  account: undefined,
  local_prefix: '/openai/',
  upstream: 'https://api.openai.com/v1',
  billing: 'pay_as_you_go',
};
const presets = [
  {
    id: 'claude-code',
    label: 'Claude Code plan',
    account_kind: 'claude-code' as const,
    upstream: route.upstream,
    auth: route.caller_auth,
  },
  { id: 'openai', label: 'OpenAI API', upstream: other.upstream, auth: other.caller_auth },
];
function snapshot(overrides: Partial<ProxyState> = {}): ProxyState {
  return {
    auth: 'token',
    mode: 'ui',
    revision: 7,
    routes: [],
    accounts: [],
    callers: [],
    admin_keys: [],
    secrets: [],
    presets,
    csrf: 'csrf',
    ...overrides,
  };
}
const caller = {
  id: 'legacy-client',
  name: 'Legacy Claude client',
  created: 1,
  grants: [route.name],
  revoked: false,
  created_by: '',
  external_id: '',
};
const configured = { accounts: [account], routes: [route], callers: [caller] };
const disposals: (() => void)[] = [];
const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
beforeEach(() => window.history.replaceState(null, '', '/_proxy/?tab=routes'));
afterEach(() => {
  disposals.splice(0).forEach((dispose) => dispose());
  document.body.replaceChildren();
  vi.restoreAllMocks();
  if (clipboardDescriptor) Object.defineProperty(navigator, 'clipboard', clipboardDescriptor);
  else Reflect.deleteProperty(navigator, 'clipboard');
});
function setup(initial = snapshot()) {
  let current = initial;
  let serial = 0;
  const api = new ProxyClient(vi.fn());
  vi.spyOn(api, 'authStatus').mockResolvedValue({ auth: initial.auth, csrf: 'csrf' } as never);
  vi.spyOn(api, 'state').mockImplementation(async () => current);
  vi.spyOn(api, 'beginAssertion').mockResolvedValue({
    publicKey: { challenge: 'fixture' },
  } as never);
  vi.spyOn(api, 'finishPasskey').mockResolvedValue();
  vi.spyOn(api, 'setupClaude').mockImplementation(async () => {
    current = { ...current, ...configured, revision: current.revision + 1 };
    return {
      revision: current.revision,
      url: 'https://proxy.example/native/',
      token: `scoped-canary-${++serial}`,
      caller_id: `caller-${serial}`,
    };
  });
  vi.spyOn(api, 'clearClaude').mockImplementation(async () => {
    current = {
      ...current,
      accounts: (current.accounts ?? []).filter((a) => a.kind !== 'claude-code'),
      routes: current.routes.filter((r) => !isClaudeRoute(r, current)),
      revision: current.revision + 1,
    };
  });
  const platform: PasskeyPlatform = {
    available: () => true,
    get: vi.fn().mockResolvedValue({ id: 'assertion' }),
    create: vi.fn(),
  };
  const root = document.createElement('div');
  document.body.append(root);
  disposals.push(mountProxy(root, api, platform));
  return { api, root, platform };
}
async function click(root: HTMLElement, name: string) {
  fireEvent.click(await waitFor(() => getByRole(root, 'button', { name })));
}
function providerFixture(state = snapshot(), refresh = vi.fn().mockResolvedValue(state)) {
  const api = new ProxyClient(vi.fn());
  const errors: unknown[] = [];
  let protectedToken = false;
  const context: UIContext = {
    refresh,
    announce: vi.fn(),
    navigate: vi.fn(),
    protectToken: (value) => {
      protectedToken = value;
    },
    hasProtectedToken: () => protectedToken,
    async run(action) {
      try {
        await action();
        return true;
      } catch (error) {
        errors.push(error);
        return false;
      }
    },
  };
  const platform: PasskeyPlatform = { available: () => true, get: vi.fn(), create: vi.fn() };
  const connection = {
    revision: 8,
    url: 'https://proxy.example/native/',
    token: 'scoped-canary-1',
    caller_id: 'device',
  };
  vi.spyOn(api, 'setupClaude').mockResolvedValue(connection);
  vi.spyOn(api, 'clearClaude').mockResolvedValue();
  return { api, context, errors, platform, refresh, connection };
}

it('creates a route and launcher, keeps the token out of browser storage, and clears it only after dismissal refresh succeeds', async () => {
  const fixture = providerFixture();
  const { api, context, errors, refresh, platform } = fixture;
  const writeText = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
  const storageWrite = vi.spyOn(Storage.prototype, 'setItem');
  const view = providerOnboarding(presets[0], snapshot(), api, context, platform, vi.fn())!;
  document.body.append(view);
  await click(view, 'Create route & get launcher');
  const command = await waitFor(
    () => getByLabelText(view, 'Launcher command') as HTMLTextAreaElement,
  );
  expect(command.value).toContain('scoped-canary-1');
  expect(command).toHaveFocus();
  expect(api.setupClaude).toHaveBeenCalledExactlyOnceWith(7);
  expect(context.hasProtectedToken!()).toBe(true);
  expect(storageWrite).not.toHaveBeenCalled();
  await click(view, 'Copy launcher command');
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(command.value));
  refresh.mockRejectedValueOnce(new ProxyAPIError(503, 'State unavailable.'));
  await click(view, 'I saved it — dismiss');
  await waitFor(() => expect(errors).toHaveLength(1));
  expect(context.hasProtectedToken!()).toBe(true);
  expect(command.value).toContain('scoped-canary-1');
  await click(view, 'I saved it — dismiss');
  await waitFor(() => expect(command.value).toBe(''));
  expect(context.hasProtectedToken!()).toBe(false);
});

it('adds a device to the selected enabled route without picking a different route or issuing another visible token', async () => {
  const state = snapshot(configured);
  const { api, context, platform } = providerFixture(state);
  const view = providerRouteAction(route, state, api, context, platform)! as HTMLDetailsElement;
  document.body.append(view);
  expect(view.querySelector('summary')).toHaveTextContent('Add device');
  view.open = true;
  context.protectToken(true);
  await click(view, 'Create device launcher');
  expect(api.setupClaude).not.toHaveBeenCalled();
  context.protectToken(false);
  await click(view, 'Create device launcher');
  await waitFor(() => expect(api.setupClaude).toHaveBeenCalledExactlyOnceWith(7, route.name));
  expect(
    providerRouteAction({ ...route, enabled: false }, state, api, context, platform),
  ).toBeUndefined();
  expect(
    providerRouteAction(route, { ...state, mode: 'file' }, api, context, platform),
  ).toBeUndefined();
});

it('keeps clearing all Claude setup separate and requires confirmation', async () => {
  const state = snapshot(configured);
  const { api, context, platform, refresh } = providerFixture(state);
  const view = clearClaudeSetup(state, api, context, platform);
  document.body.append(view);
  const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
  fireEvent.click(view);
  expect(api.clearClaude).not.toHaveBeenCalled();
  fireEvent.click(view);
  await waitFor(() => expect(refresh).toHaveBeenCalledOnce());
  expect(confirm).toHaveBeenCalledWith(
    expect.stringContaining('Other providers and your normal Claude setup are unchanged'),
  );
  expect(api.clearClaude).toHaveBeenCalledExactlyOnceWith(7);
});

it('uses the revisioned endpoint with CSRF, never URL tokens or account/pairing APIs', async () => {
  const connection = {
    revision: 8,
    url: 'https://proxy.example/native/',
    token: 'scoped-canary',
    caller_id: 'caller',
  };
  const fetcher = vi
    .fn<typeof fetch>()
    .mockResolvedValueOnce(new Response(JSON.stringify({ csrf: 'csrf' })))
    .mockResolvedValueOnce(new Response(JSON.stringify(connection)))
    .mockResolvedValueOnce(new Response(null, { status: 204 }));
  const api = new ProxyClient(fetcher);
  await api.login('admin');
  expect(await api.setupClaude(7)).toEqual(connection);
  await api.clearClaude(8);
  expect(fetcher.mock.calls.slice(1)).toEqual([
    [
      '/_proxy/api/claude/setup',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ revision: 7 }),
        headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf' }),
        credentials: 'same-origin',
        cache: 'no-store',
      }),
    ],
    [
      '/_proxy/api/claude/setup',
      expect.objectContaining({
        method: 'DELETE',
        body: JSON.stringify({ revision: 8 }),
        headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf' }),
      }),
    ],
  ]);
});

it('safely quotes native command arguments and literal YAML, without API-key examples', () => {
  expect(claudeLauncher("https://proxy.test/o'connor/", "scope'$x;echo unsafe")).toBe(
    "term-llm proxy run claude --url 'https://proxy.test/o'\\''connor/' --token 'scope'\\''$x;echo unsafe'",
  );
  expect(claudeProviderYAML("https://proxy.test/o'connor/", "scope'token")).toBe(
    "providers:\n  claude-bin:\n    proxy:\n      url: 'https://proxy.test/o''connor/'\n      token: 'scope''token'\n",
  );
  expect(isClaudeRoute({ ...other, name: 'claude' }, snapshot(configured))).toBe(false);
});

it('uses native Claude examples and unchanged curl examples for mixed generic Clients grants', async () => {
  const { root, api } = setup(snapshot({ ...configured, routes: [route, other] }));
  vi.spyOn(api, 'createCaller').mockResolvedValue({
    token: 'generic-scoped-canary',
    caller: {
      id: 'mixed',
      name: 'mixed',
      created: 1,
      grants: [route.name, other.name],
      revoked: false,
      created_by: '',
      external_id: '',
    },
  });
  await click(root, 'Clients');
  await click(root, 'New client');
  fireEvent.input(getByLabelText(root, 'Client name'), { target: { value: 'mixed' } });
  fireEvent.click(getByLabelText(root, route.name));
  fireEvent.click(getByLabelText(root, other.name));
  await click(root, 'Create client');
  await waitFor(() => expect(getByLabelText(root, 'Launcher command')).toBeVisible());
  expect((getByLabelText(root, 'Launcher command') as HTMLTextAreaElement).value).toContain(
    "--token 'generic-scoped-canary'",
  );
  expect([...root.querySelectorAll('pre')].map((pre) => pre.textContent)).toEqual([
    expect.stringContaining('/openai/'),
  ]);
  expect(root.querySelector('pre')).toHaveTextContent('curl -H');
  expect(root.textContent).toContain(
    'For curl examples, set TOKEN in your shell to this client token, not the administrator token.',
  );
});
