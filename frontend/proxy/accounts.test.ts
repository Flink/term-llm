import { afterEach, expect, it, vi } from 'vitest';
import { fireEvent, getByLabelText, getByRole, waitFor } from '@testing-library/preact';
import { ProxyClient, type ProxyAccount, type ProxyState } from '../src/api/proxy-client';
import type { PasskeyPlatform } from '../src/hub/platform/passkeys';
import type { UIContext } from './dom';
import {
  accountControls,
  accountForm,
  accountStateLabel,
  parseAccountCredentials,
  unusedAccountsView,
} from './accounts';

const account: ProxyAccount = {
  name: 'home-chat',
  kind: 'chatgpt',
  state: 'needs_login',
  generation: 2,
  updated: 1,
};
function setup(auth: 'token' | 'passkey' = 'passkey', entry: ProxyAccount = account) {
  const api = new ProxyClient(vi.fn());
  const announce = vi.fn();
  const ctx: UIContext = {
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
    refresh: vi.fn(),
    protectToken: vi.fn(),
    navigate: vi.fn(),
  };
  const platform: PasskeyPlatform = {
    available: () => true,
    create: vi.fn(),
    get: vi.fn().mockResolvedValue({ id: 'assertion' }),
  };
  vi.spyOn(api, 'beginAssertion').mockResolvedValue({
    publicKey: { challenge: 'fixture' },
  } as never);
  vi.spyOn(api, 'finishPasskey').mockResolvedValue();
  const state = { auth, accounts: [entry], routes: [] } as unknown as ProxyState;
  const deps = { state, api, context: ctx, platform };
  const view = accountControls(deps, entry, ['home', 'home-backend']);
  view.open = true;
  document.body.append(view);
  return { api, view, platform, announce, deps, ctx };
}
afterEach(() => {
  document.body.replaceChildren();
  vi.restoreAllMocks();
});
it('shows account status and every route sharing it without credential fields', () => {
  const { view } = setup();
  expect(view.querySelector('summary')).toHaveTextContent('Manage account home-chat');
  expect(view.textContent).toContain('needs login');
  expect(view.textContent).toContain('Shared by routes home, home-backend.');
  expect(view.querySelector('input')).toBeNull();
});
it('clears credential paste before fresh passkey authentication and replacement', async () => {
  const { api, view } = setup();
  fireEvent.click(getByRole(view, 'button', { name: 'Replace credentials' }));
  const paste = getByLabelText(view, 'Paste credential JSON (write-only)') as HTMLInputElement;
  expect(paste.type).toBe('password');
  expect(paste.value).toBe('');
  const credentials = {
    access_token: 'canary-access',
    refresh_token: 'canary-refresh',
    account_id: 'canary-account',
    expires_at: 4102444800,
  };
  paste.value = JSON.stringify(credentials);
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  vi.mocked(api.beginAssertion).mockImplementation(async () => {
    expect(paste.value).toBe('');
    return { publicKey: { challenge: 'fixture' } } as never;
  });
  const save = vi
    .spyOn(api, 'saveAccount')
    .mockResolvedValue({ account: { ...account, state: 'ready' } });
  fireEvent.click(getByRole(view, 'button', { name: 'Replace account credentials' }));
  await waitFor(() =>
    expect(save).toHaveBeenCalledWith({
      name: 'home-chat',
      kind: 'chatgpt',
      replace: true,
      credentials,
    }),
  );
  expect(api.beginAssertion).toHaveBeenCalledWith('reauth');
  expect(api.finishPasskey).toHaveBeenCalledBefore(save);
  await waitFor(() => expect(view.textContent).toContain('ready'));
  expect(view.textContent).not.toContain('canary-');
});
it('does not send credentials when the fresh passkey prompt is cancelled', async () => {
  const { api, view, platform } = setup();
  fireEvent.click(getByRole(view, 'button', { name: 'Replace credentials' }));
  const paste = getByLabelText(view, 'Paste credential JSON (write-only)') as HTMLInputElement;
  paste.value = '{"access_token":"canary"}';
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  vi.mocked(platform.get).mockRejectedValue(new DOMException('canary-secret', 'NotAllowedError'));
  const save = vi.spyOn(api, 'saveAccount');
  fireEvent.click(getByRole(view, 'button', { name: 'Replace account credentials' }));
  await waitFor(() => expect(platform.get).toHaveBeenCalled());
  expect(save).not.toHaveBeenCalled();
  expect(paste.value).toBe('');
});
it('uses fresh authentication for account sign-out too', async () => {
  const { api, view } = setup();
  const signOut = vi.spyOn(api, 'signOutAccount').mockResolvedValue({ account });
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  fireEvent.click(getByRole(view, 'button', { name: 'Sign out account' }));
  await waitFor(() => expect(signOut).toHaveBeenCalledWith('home-chat'));
  expect(api.finishPasskey).toHaveBeenCalledBefore(signOut);
  expect(window.confirm).toHaveBeenCalledWith(
    expect.stringContaining('Routes home, home-backend use it.'),
  );
});
it('uses real account endpoint verbs and existing CSRF transport', async () => {
  const fetcher = vi
    .fn<typeof fetch>()
    .mockResolvedValueOnce(new Response(JSON.stringify({ csrf: 'csrf' })))
    .mockResolvedValueOnce(new Response(JSON.stringify({ accounts: null })))
    .mockImplementation(async () => new Response(JSON.stringify({ account })));
  const api = new ProxyClient(fetcher);
  await api.login('admin');
  expect(await api.accounts()).toEqual([]);
  const payload = {
    name: 'test',
    kind: 'copilot' as const,
    credentials: { github_token: 'canary' },
  };
  await api.saveAccount(payload);
  await api.saveAccount({ ...payload, replace: true });
  await api.signOutAccount('test');
  expect(fetcher.mock.calls.map((call) => call[1]?.method)).toEqual([
    'POST',
    'GET',
    'POST',
    'PUT',
    'DELETE',
  ]);
  expect(fetcher.mock.calls[4]?.[1]?.headers).toEqual(
    expect.objectContaining({ 'X-CSRF-Token': 'csrf' }),
  );
});
it.each([
  '{"issuer":"https://evil.example"}',
  '{"origins":[]}',
  '{"__proto__":{}}',
  '{"access_token":"canary\\nsecret"}',
  '{"expires_at":1763200000000}',
  '[]',
  '{}',
  'canary-invalid-json',
])('rejects unsafe paste without echoing it: %s', (raw) => {
  expect(() => parseAccountCredentials(raw)).toThrow();
  try {
    parseAccountCredentials(raw);
  } catch (error) {
    expect(String(error)).not.toContain('canary');
  }
});

it.each(['ready', 'needs_login', 'native_login'])(
  'excludes Claude accounts from unused account maintenance (%s)',
  (state) => {
    const { deps } = setup('token');
    deps.state.accounts = [{ ...account, name: 'claude', kind: 'claude-code', state }];
    expect(unusedAccountsView(deps)).toBeUndefined();
  },
);

it('lists only accounts no route references as unused', () => {
  const { deps } = setup('token');
  deps.state.accounts = [account, { ...account, name: 'spare', kind: 'grok' }];
  deps.state.routes = [{ name: 'home', account: 'home-chat' } as ProxyState['routes'][number]];
  const view = unusedAccountsView(deps)!;
  expect([...view.querySelectorAll('summary')].map((node) => node.textContent)).toEqual([
    'Manage account spare',
  ]);
});

it('offers only proxy-held account kinds and can fix the kind for a route', () => {
  const { deps } = setup('token');
  const free = accountForm(deps, { stored: vi.fn(), cancel: vi.fn() });
  const kinds = getByLabelText(free, 'Account kind') as HTMLSelectElement;
  expect([...kinds.options].map((option) => option.value)).toEqual([
    'chatgpt',
    'grok',
    'copilot',
    'aws-sigv4',
  ]);
  expect(kinds).not.toBeDisabled();
  const fixed = accountForm(deps, { kind: 'grok', name: 'grok', stored: vi.fn(), cancel: vi.fn() });
  expect(getByLabelText(fixed, 'Account kind')).toHaveValue('grok');
  expect(getByLabelText(fixed, 'Account kind')).toBeDisabled();
  expect(getByLabelText(fixed, 'Account name')).toHaveValue('grok');
});

it.each(['ready', 'needs_login', 'refreshing'])('keeps non-Claude provider status %s', (state) => {
  expect(accountStateLabel({ ...account, state })).toBe(state.replaceAll('_', ' '));
});
