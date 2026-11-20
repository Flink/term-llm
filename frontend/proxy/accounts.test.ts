import { afterEach, expect, it, vi } from 'vitest';
import { fireEvent, getByLabelText, getByRole, waitFor } from '@testing-library/preact';
import { ProxyClient, type ProxyAccount, type ProxyState } from '../src/api/proxy-client';
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
  vi.spyOn(api, 'beginAssertion');
  const state = { auth, accounts: [entry], routes: [] } as unknown as ProxyState;
  const deps = { state, api, context: ctx };
  const view = accountControls(deps, entry, ['home', 'home-backend']);
  document.body.append(view);
  return { api, view, announce, deps, ctx };
}
afterEach(() => {
  document.body.replaceChildren();
  vi.restoreAllMocks();
});
it('shows account status and every route sharing it without credential fields or nested disclosures', () => {
  const { view } = setup();
  expect(view.querySelector('details')).toBeNull();
  expect(view.textContent).toContain('home-chat');
  expect(view.textContent).toContain('needs login');
  expect(view.textContent).toContain('Shared by routes home, home-backend.');
  expect(view.querySelector('input')).toBeNull();
});
it('clears credential paste before replacement without a second passkey prompt', async () => {
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
  const save = vi.spyOn(api, 'saveAccount').mockImplementation(async () => {
    expect(paste.value).toBe('');
    return { account: { ...account, state: 'ready' } };
  });
  fireEvent.click(getByRole(view, 'button', { name: 'Replace account credentials' }));
  await waitFor(() =>
    expect(save).toHaveBeenCalledWith({
      name: 'home-chat',
      kind: 'chatgpt',
      replace: true,
      credentials,
    }),
  );
  expect(api.beginAssertion).not.toHaveBeenCalled();
  await waitFor(() => expect(view.textContent).toContain('ready'));
  expect(view.textContent).not.toContain('canary-');
});
it('signs out after confirmation without a second passkey prompt', async () => {
  const { api, view } = setup();
  const signOut = vi.spyOn(api, 'signOutAccount').mockResolvedValue({ account });
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  fireEvent.click(getByRole(view, 'button', { name: 'Sign out account' }));
  await waitFor(() => expect(signOut).toHaveBeenCalledWith('home-chat'));
  expect(api.beginAssertion).not.toHaveBeenCalled();
  expect(window.confirm).toHaveBeenCalledWith(
    expect.stringContaining('Routes home, home-backend use it.'),
  );
});
it('removes only unused accounts, after confirmation', async () => {
  const { api, view, deps, ctx } = setup();
  expect(view.querySelector('button.danger')).toBeNull();
  const unused = accountControls(deps, account);
  document.body.append(unused);
  const remove = vi.spyOn(api, 'removeAccount').mockResolvedValue({ removed: 'home-chat' });
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  fireEvent.click(getByRole(unused, 'button', { name: 'Remove account' }));
  await waitFor(() => expect(remove).toHaveBeenCalledWith('home-chat'));
  expect(api.beginAssertion).not.toHaveBeenCalled();
  expect(deps.state.accounts).toEqual([]);
  expect(unused.isConnected).toBe(false);
  expect(ctx.refresh).toHaveBeenCalled();
});
it('sends the remove flag on account removal', async () => {
  const fetcher = vi
    .fn<typeof fetch>()
    .mockResolvedValue(new Response(JSON.stringify({ removed: 'test' })));
  await new ProxyClient(fetcher).removeAccount('test');
  expect(fetcher.mock.calls[0]?.[1]?.method).toBe('DELETE');
  expect(JSON.parse(String(fetcher.mock.calls[0]?.[1]?.body))).toEqual({
    name: 'test',
    remove: true,
  });
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

it.each(['ready', 'needs_login', 'refreshing'])(
  'excludes Claude accounts from unused account maintenance (%s)',
  (state) => {
    const { deps } = setup('token');
    deps.state.accounts = [
      { ...account, name: 'claude', kind: 'claude-bin', auth_mode: 'shared_vault', state },
    ];
    expect(unusedAccountsView(deps)).toBeUndefined();
  },
);

it('lists only accounts no route references as unused', () => {
  const { deps } = setup('token');
  deps.state.accounts = [account, { ...account, name: 'spare', kind: 'grok' }];
  deps.state.routes = [{ name: 'home', account: 'home-chat' } as ProxyState['routes'][number]];
  const view = unusedAccountsView(deps)!;
  expect(view.querySelector('details')).toBeNull();
  expect(
    [...view.querySelectorAll('.account-controls-status strong')].map((node) => node.textContent),
  ).toEqual(['spare']);
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
