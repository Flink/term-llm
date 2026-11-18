import {
  fireEvent,
  getByLabelText,
  getByRole,
  queryByLabelText,
  waitFor,
} from '@testing-library/preact';
import { afterEach, expect, it, vi } from 'vitest';
import { ProxyClient, type ProxyAdminKey, type ProxyState } from '../src/api/proxy-client';
import type { SerializedPublicKeyCredential } from '../src/hub/domain/types';
import { mountProxy } from './app';
import { dateTime } from './dom';

const key: ProxyAdminKey = {
  id: 'ak_ci',
  name: 'Build automation',
  created: 1700000000000,
  last_used: 0,
};
const token = 'sk-tlpadm-synthetic-show-once';
const credential = { id: 'test-credential' } as SerializedPublicKeyCredential;
const assertion = { publicKey: { challenge: 'assertion' } };
const disposals: (() => void)[] = [];

afterEach(() => {
  disposals.splice(0).forEach((dispose) => dispose());
  document.body.replaceChildren();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.history.replaceState(null, '', '/');
});

async function setup(overrides: Partial<ProxyState> = {}) {
  window.history.replaceState(null, '', '/_proxy/?tab=admin');
  const snapshot: ProxyState = {
    auth: 'passkey',
    mode: 'ui',
    revision: 23,
    csrf: 'state-csrf',
    routes: [],
    secrets: [],
    callers: [],
    admin_keys: [],
    ...overrides,
  };
  const issue = vi.fn(async () => Response.json({ key, token }, { status: 201 }));
  const readState = vi.fn(async () => Response.json(snapshot));
  const fetcher = vi.fn<typeof fetch>(async (url, init) => {
    if (url === '/_proxy/api/auth/status')
      return Response.json({ auth: snapshot.auth, csrf: 'auth-csrf' });
    if (url === '/_proxy/api/state') return readState();
    if (url === '/_proxy/api/auth/reauth/begin') return Response.json(assertion);
    if (url === '/_proxy/api/auth/reauth/finish') return Response.json({ csrf: 'fresh-csrf' });
    if (url === '/_proxy/api/admin-keys' && init?.method === 'POST') {
      const result = await issue();
      if (result.ok) snapshot.admin_keys = [key];
      return result;
    }
    if (url === '/_proxy/api/admin-keys' && init?.method === 'DELETE') {
      snapshot.admin_keys = [];
      return Response.json({
        ok: true,
        revoked_clients: JSON.parse(String(init.body)).revoke_clients ? 2 : 0,
      });
    }
    throw new Error(`Unexpected request: ${url}`);
  });
  const get = vi.fn().mockResolvedValue(credential);
  const root = document.createElement('div');
  document.body.append(root);
  disposals.push(
    mountProxy(root, new ProxyClient(fetcher), { available: () => true, get, create: vi.fn() }),
  );
  await waitFor(() => expect(getByRole(root, 'heading', { name: 'API keys' })).toBeVisible());
  const click = (name: string) => fireEvent.click(getByRole(root, 'button', { name }));
  const create = () => {
    fireEvent.input(getByLabelText(root, 'API key name'), { target: { value: `  ${key.name}  ` } });
    click('Create API key');
  };
  const requests = () =>
    fetcher.mock.calls.slice(2).map(([path]) => String(path).replace('/_proxy/api/', ''));
  const mutation = () => fetcher.mock.calls.find(([url]) => url === '/_proxy/api/admin-keys')?.[1];
  return { root, issue, readState, get, create, click, requests, mutation };
}

const browserLeave = () => {
  const event = new Event('beforeunload', { cancelable: true });
  window.dispatchEvent(event);
  return event.defaultPrevented;
};

it('reauthenticates before issuing, protects the pending and shown token, then clears it on dismissal', async () => {
  const { root, issue, get, create, click, requests, mutation } = await setup();
  let finishAssertion!: (value: SerializedPublicKeyCredential) => void;
  get.mockReturnValueOnce(
    new Promise((resolve) => {
      finishAssertion = resolve;
    }),
  );
  let deliverToken!: (response: Response) => void;
  issue.mockReturnValueOnce(
    new Promise((resolve) => {
      deliverToken = resolve;
    }),
  );
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal('navigator', Object.assign(Object.create(navigator), { clipboard: { writeText } }));

  create();
  await waitFor(() => expect(get).toHaveBeenCalledWith(assertion));
  expect(requests()).toEqual(['auth/reauth/begin']);
  expect(issue).not.toHaveBeenCalled();
  finishAssertion(credential);
  await waitFor(() => expect(issue).toHaveBeenCalledOnce());
  expect(requests()).toEqual(['auth/reauth/begin', 'auth/reauth/finish', 'admin-keys']);
  expect(mutation()).toMatchObject({
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'X-CSRF-Token': 'fresh-csrf', 'Content-Type': 'application/json' },
    body: JSON.stringify({ name: key.name }),
  });
  expect(browserLeave()).toBe(true);
  click('Routes');
  expect(confirm).not.toHaveBeenCalled();
  expect(window.location.search).toBe('?tab=admin');
  expect(queryByLabelText(root, 'API key token')).toBeNull();

  deliverToken(Response.json({ key, token }, { status: 201 }));
  const shown = await waitFor(() => getByLabelText<HTMLInputElement>(root, 'API key token'));
  expect(shown).toHaveValue(token);
  expect(shown).toHaveFocus();
  expect(browserLeave()).toBe(true);
  const commands = [...root.querySelectorAll('pre')].map((pre) => pre.textContent);
  expect(commands).toHaveLength(2);
  expect(commands[0]).toContain(`${location.origin}/_proxy/manage/v1/routes`);
  expect(commands[1]).toContain(`${location.origin}/_proxy/manage/v1/clients`);
  expect(commands[1]).toContain('"grants":["example-route"],"revision":23');
  for (const command of commands) {
    expect(command).toContain('Authorization: Bearer $TERM_LLM_PROXY_ADMIN_KEY');
    expect(command).not.toContain(token);
  }
  click('Routes');
  expect(confirm).toHaveBeenCalledOnce();
  expect(shown).toBeVisible();
  click('Copy token');
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(token));
  click('I saved it — dismiss');
  expect(shown).toHaveValue('');
  await waitFor(() =>
    expect(getByRole(root, 'button', { name: `Revoke API key ${key.name}` })).toBeVisible(),
  );
  expect(queryByLabelText(root, 'API key token')).toBeNull();
  expect(browserLeave()).toBe(false);
  expect(requests()).toEqual([
    'auth/reauth/begin',
    'auth/reauth/finish',
    'admin-keys',
    'state',
    'state',
  ]);
});

it('does not let a refresh started before issuance drop the pending token view', async () => {
  const { root, issue, readState, create, click } = await setup({ auth: 'token' });
  let deliverState!: (response: Response) => void;
  readState.mockReturnValueOnce(
    new Promise((resolve) => {
      deliverState = resolve;
    }),
  );
  let deliverToken!: (response: Response) => void;
  issue.mockReturnValueOnce(
    new Promise((resolve) => {
      deliverToken = resolve;
    }),
  );
  click('Refresh state');
  create();
  await waitFor(() => expect(issue).toHaveBeenCalledOnce());
  deliverState(Response.json({ auth: 'token', mode: 'ui', revision: 23, csrf: 'state-csrf' }));
  await waitFor(() =>
    expect(getByRole(root, 'button', { name: 'Refresh state' })).not.toBeDisabled(),
  );
  expect(browserLeave()).toBe(true);
  deliverToken(Response.json({ key, token }, { status: 201 }));
  await waitFor(() => expect(getByLabelText(root, 'API key token')).toHaveValue(token));
});

it.each([false, true])(
  'confirms and reauthenticates before revoking (cascade %s)',
  async (cascade) => {
    const { root, get, click, requests, mutation } = await setup({ admin_keys: [key] });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    expect(root).toHaveTextContent(`Created ${dateTime(key.created)} · Last used Never`);
    const checkbox = getByLabelText(root, 'Also revoke clients created by this key');
    expect(checkbox).not.toBeChecked();
    if (cascade) fireEvent.click(checkbox);
    click(`Revoke API key ${key.name}`);
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining(`Revoke API key ${key.name}?`));
    expect(get).not.toHaveBeenCalled();
    expect(requests()).toEqual([]);

    confirm.mockReturnValue(true);
    click(`Revoke API key ${key.name}`);
    await waitFor(() =>
      expect(root).toHaveTextContent(
        `Revoked API key ${key.name}. ${cascade ? 2 : 0} clients revoked.`,
      ),
    );
    expect(get).toHaveBeenCalledOnce();
    expect(requests()).toEqual(['auth/reauth/begin', 'auth/reauth/finish', 'admin-keys', 'state']);
    expect(mutation()).toMatchObject({
      method: 'DELETE',
      headers: { 'X-CSRF-Token': 'fresh-csrf', 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: key.id, revoke_clients: cascade }),
    });
    expect(root).toHaveTextContent('No API keys yet.');
  },
);

it.each(['create', 'revoke'])(
  'does not %s a key after cancelled reauthentication',
  async (action) => {
    const { root, get, create, click, requests } = await setup({ admin_keys: [key] });
    get.mockRejectedValueOnce(new DOMException('synthetic browser error', 'NotAllowedError'));
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    if (action === 'create') create();
    else click(`Revoke API key ${key.name}`);
    await waitFor(() =>
      expect(getByRole(root, 'status')).toHaveTextContent(
        'The passkey prompt was cancelled or timed out.',
      ),
    );
    expect(requests()).toEqual(['auth/reauth/begin']);
    expect(browserLeave()).toBe(false);
    expect(queryByLabelText(root, 'API key token')).toBeNull();
  },
);

it('uses only the session in token mode and clears pending protection when issuance fails', async () => {
  const { root, issue, get, create, requests, mutation } = await setup({
    auth: 'token',
    mode: 'file',
  });
  expect(root).toHaveTextContent('routes added later');
  expect(root).toHaveTextContent('only clients created through the API');
  expect(root).toHaveTextContent('only over loopback or HTTPS');
  issue.mockResolvedValueOnce(Response.json({ error: 'API key limit reached.' }, { status: 400 }));
  create();
  await waitFor(() =>
    expect(getByRole(root, 'status')).toHaveTextContent('API key limit reached.'),
  );
  expect(get).not.toHaveBeenCalled();
  expect(requests()).toEqual(['admin-keys']);
  expect(mutation()).toMatchObject({ headers: { 'X-CSRF-Token': 'state-csrf' } });
  expect(browserLeave()).toBe(false);
  expect(queryByLabelText(root, 'API key token')).toBeNull();
});
