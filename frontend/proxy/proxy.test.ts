import { withSingletonProviders } from './test-fixtures';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  fireEvent,
  getByLabelText,
  queryByLabelText,
  getByRole,
  queryByRole,
  waitFor,
} from '@testing-library/preact';
import {
  ProxyAPIError,
  ProxyClient,
  type ProxyAdminRoute,
  type ProxyState,
} from '../src/api/proxy-client';
import { mountProxy } from './app';
import { callerCurl } from './security';
import { localPath, outcomeLabel } from './requests';

const route: ProxyAdminRoute = {
  serves: [],
  profile: '',
  provider: 'Example',
  name: 'Example',
  local_prefix: '/example/',
  upstream: 'https://example.com/api/',
  enabled: true,
  allow_insecure_http: false,
  caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
  set_headers: {},
};
function state(overrides: Partial<ProxyState> = {}): ProxyState {
  return {
    defaults: null,
    providers: [],
    auth: 'token',
    mode: 'ui',
    revision: 7,
    routes: [],
    secrets: [],
    callers: [],
    admin_keys: [],
    csrf: 'csrf-token',
    ...overrides,
  };
}
// Route-editor tests opt into the preserved legacy link; Home has its own suite.
beforeEach(() => window.history.replaceState(null, '', '/?tab=routes'));
const disposals: (() => void)[] = [];
function setup(snapshot = state()) {
  const api = new ProxyClient(vi.fn());
  vi.spyOn(api, 'authStatus').mockResolvedValue({
    auth: 'token',
    setup_required: false,
    bootstrap_available: false,
    recovery_available: false,
    bootstrap_verified: false,
    recovery_verified: false,
    csrf: 'preauth',
    origin: 'https://proxy.example',
  });
  vi.spyOn(api, 'state').mockResolvedValue(withSingletonProviders(snapshot));
  vi.spyOn(api, 'timeline').mockResolvedValue({
    timezone: 'UTC',
    range: '30d',
    start: '2026-10-18T00:00:00Z',
    end: '2026-11-17T00:00:00Z',
    currencies: [],
    buckets: [],
  });
  const root = document.createElement('div');
  document.body.append(root);
  const dispose = mountProxy(root, api);
  disposals.push(dispose);
  return { api, root, dispose };
}
async function openKeyDraft(root: HTMLElement) {
  await click(root, 'Add provider');
  await click(root, 'Custom endpoint');
  fireEvent.input(getByLabelText(root, 'Name'), { target: { value: 'openai' } });
  fireEvent.input(getByLabelText(root, 'Upstream base URL'), {
    target: { value: 'https://api.openai.com/v1' },
  });
  await click(root, 'Add substitution');
  fireEvent.input(getByLabelText(root, 'Header name'), { target: { value: 'Authorization' } });
}
async function click(root: HTMLElement, name: string) {
  fireEvent.click(await waitFor(() => getByRole(root, 'button', { name })));
}
function openDetails(root: HTMLElement, title: string) {
  const details = [...root.querySelectorAll('details')].find(
    (node) => node.querySelector('summary')?.textContent === title,
  );
  if (!details) throw new Error(`Missing disclosure: ${title}`);
  details.open = true;
  return details;
}
async function openManage(root: HTMLElement, routeName: string) {
  const line = await waitFor(() => {
    const found = root.querySelector<HTMLElement>(`[data-focus="route:${routeName}"]`);
    if (!found) throw new Error(`Missing route line: ${routeName}`);
    return found;
  });
  const details = line
    .closest('article.route-card')
    ?.querySelector<HTMLDetailsElement>('details.provider-manage');
  if (!details) throw new Error(`Missing provider Manage: ${routeName}`);
  details.open = true;
  return details;
}
afterEach(() => {
  disposals.splice(0).forEach((dispose) => dispose());
  document.body.replaceChildren();
  vi.restoreAllMocks();
  window.history.replaceState(null, '', '/');
});

describe('proxy navigation', () => {
  it('lands on Home by default and preserves the explicit Providers bookmark', async () => {
    window.history.replaceState(null, '', '/_proxy/');
    const { root, api } = setup();
    await waitFor(() =>
      expect(getByRole(root, 'button', { name: 'Home' })).toHaveAttribute('aria-current', 'page'),
    );
    expect(queryByRole(root, 'button', { name: 'Routes' })).toBeNull();
    await waitFor(() => expect(api.timeline).toHaveBeenCalledOnce());
    await click(root, 'Refresh state');
    await waitFor(() => expect(api.timeline).toHaveBeenCalledTimes(2));
    await click(root, 'Providers');
    expect(window.location.search).toBe('?tab=routes');
    await click(root, 'Home');
    expect(window.location.search).toBe('');
  });

  function activeTab(root: HTMLElement, name: string) {
    expect(getByRole(root, 'button', { name })).toHaveAttribute('aria-current', 'page');
  }
  it('keeps Requests selected across refresh and remount without changing the mount path', async () => {
    window.history.replaceState(null, '', '/_proxy/?other=value#anchor');
    const { root, api, dispose } = setup();
    const requests = vi.spyOn(api, 'requests').mockResolvedValue({ requests: [], next: 0 });
    await click(root, 'Requests');
    expect(window.location.pathname).toBe('/_proxy/');
    expect(window.location.search).toBe('?other=value&tab=requests');
    expect(window.location.hash).toBe('#anchor');
    expect(queryByRole(root, 'button', { name: 'Refresh state' })).toBeNull();
    await waitFor(() => expect(getByRole(root, 'button', { name: 'Refresh' })).toBeEnabled());
    await click(root, 'Refresh');
    await waitFor(() => expect(requests).toHaveBeenCalledTimes(2));
    expect(api.state).toHaveBeenCalledOnce();
    activeTab(root, 'Requests');
    const historyState = window.history.state;
    dispose();
    disposals.push(mountProxy(root, api));
    await waitFor(() => activeTab(root, 'Requests'));
    expect(window.history.state).toEqual(historyState);
    window.history.back();
    await waitFor(() => activeTab(root, 'Home'));
    window.history.forward();
    await waitFor(() => activeTab(root, 'Requests'));
  });
  it('preserves Debug bookmarks and clears capture previews when navigating or signing out', async () => {
    window.history.replaceState(null, '', '/_proxy/?tab=debug');
    const { root, api, dispose } = setup();
    vi.spyOn(api, 'debug').mockResolvedValue({ enabled: false, directory: '/captures' });
    vi.spyOn(api, 'debugCaptures').mockResolvedValue({ captures: [] });
    vi.spyOn(api, 'logout').mockResolvedValue();
    await waitFor(() => activeTab(root, 'Debug'));
    await waitFor(() => expect(api.debugCaptures).toHaveBeenCalledOnce());
    await click(root, 'Providers');
    window.history.back();
    await waitFor(() => activeTab(root, 'Debug'));
    await waitFor(() => expect(api.debugCaptures).toHaveBeenCalledTimes(2));
    const preview = root.querySelector('pre')!;
    preview.textContent = 'private capture';
    await click(root, 'Sign out');
    await waitFor(() => getByRole(root, 'heading', { name: 'Sign in' }));
    expect(preview.textContent).toBe('');
    expect(root).not.toHaveTextContent('private capture');
    dispose();
    disposals.push(mountProxy(root, api));
    await waitFor(() => activeTab(root, 'Debug'));
    expect(window.location.search).toBe('?tab=debug');
  });
  it('returns expired debug sessions to sign-in without rendering capture payloads', async () => {
    window.history.replaceState(null, '', '/_proxy/?tab=debug');
    const { root, api } = setup();
    vi.spyOn(api, 'debug').mockRejectedValue(new ProxyAPIError(401, 'Please sign in.'));
    const list = vi.spyOn(api, 'debugCaptures');
    await waitFor(() => getByRole(root, 'heading', { name: 'Sign in' }));
    expect(root.querySelector('pre')).toBeNull();
    expect(list).not.toHaveBeenCalled();
  });
  it('uses Back and Forward without adding duplicate entries for the selected tab', async () => {
    const { root } = setup();
    await click(root, 'Secrets');
    await click(root, 'Clients');
    const length = window.history.length;
    await click(root, 'Clients');
    expect(window.history.length).toBe(length);
    window.history.back();
    await waitFor(() => activeTab(root, 'Secrets'));
    window.history.back();
    await waitFor(() => activeTab(root, 'Providers'));
    window.history.forward();
    await waitFor(() => activeTab(root, 'Secrets'));
  });
  it('protects a one-time client token during Back navigation without adding history entries', async () => {
    const { root, api } = setup(state({ routes: [route] }));
    vi.spyOn(api, 'createProviderCaller').mockResolvedValue({
      token: 'private-once',
      caller: {
        id: 'new',
        name: 'New',
        created: 1,
        revoked: false,
        grants: ['Example'],
        created_by: '',
        external_id: '',
      },
    });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    await click(root, 'Clients');
    fireEvent.input(getByLabelText(root, 'Client name'), { target: { value: 'New' } });
    fireEvent.click(getByLabelText(root, 'Example'));
    await click(root, 'Create client');
    await waitFor(() => expect(getByLabelText(root, 'Client token')).toBeTruthy());
    const length = window.history.length;
    window.history.back();
    await waitFor(() => expect(confirm).toHaveBeenCalledOnce());
    await waitFor(() => expect(window.location.search).toBe('?tab=clients'));
    expect(getByLabelText(root, 'Client token')).toBeTruthy();
    expect(window.history.length).toBe(length);
    confirm.mockReturnValue(true);
    window.history.back();
    await waitFor(() => activeTab(root, 'Providers'));
    expect(queryByRole(root, 'textbox', { name: 'Client token' })).toBeNull();
    window.history.forward();
    await waitFor(() => activeTab(root, 'Clients'));
  });
  it.each(['clients', 'callers'])('opens Clients from the %s tab link', async (tab) => {
    window.history.replaceState(null, '', `/_proxy/?tab=${tab}`);
    const { root } = setup();
    await waitFor(() => activeTab(root, 'Clients'));
    expect(getByRole(root, 'heading', { name: 'Clients', level: 2 })).toBeVisible();
    expect(getByLabelText(root, 'Client name')).toBeVisible();
    expect(window.location.search).toBe('?tab=clients');
    expect(queryByRole(root, 'button', { name: 'Callers' })).toBeNull();
  });
  it('ignores retired pairing links and keeps Clients limited to scoped tokens', async () => {
    window.history.replaceState(null, '', '/_proxy/?tab=clients&connect=retired&code=old');
    const { root } = setup(state({ routes: [route], accounts: [] }));
    await waitFor(() => activeTab(root, 'Clients'));
    expect(getByLabelText(root, 'Client name')).toBeVisible();
    expect(getByLabelText(root, 'Example')).not.toBeChecked();
    expect(queryByRole(root, 'button', { name: 'Connect Claude' })).toBeNull();
    expect(queryByRole(root, 'button', { name: 'Sign in with Claude' })).toBeNull();
    expect(root.querySelector('input[type=password]')).toBeNull();
    await click(root, 'Providers');
    // No page-level account list; accounts live on the routes that use them.
    await waitFor(() => expect(root.querySelector('.route-card')).not.toBeNull());
    expect(root.querySelector('details.route-management')).toBeNull();
    expect(root.textContent).not.toContain('Other provider accounts');
  });
  it.each(['unknown', 'passkeys'])('falls back safely from unavailable tab %s', async (tab) => {
    window.history.replaceState(null, '', `/_proxy/?tab=${tab}`);
    const { root } = setup();
    await waitFor(() => activeTab(root, 'Home'));
    expect(window.location.search).toBe('');
  });
  it('preserves the requested tab through sign in', async () => {
    window.history.replaceState(null, '', '/_proxy/?tab=secrets');
    const { root, api } = setup();
    vi.spyOn(api, 'state').mockRejectedValueOnce(new ProxyAPIError(401, 'Sign in.'));
    vi.spyOn(api, 'login').mockResolvedValue();
    const token = await waitFor(() => getByLabelText(root, /Administrator token/));
    fireEvent.input(token, { target: { value: 'admin' } });
    await click(root, 'Sign in');
    await waitFor(() => activeTab(root, 'Secrets'));
  });
});

describe('proxy transport', () => {
  it('uses same-origin session credentials, revision and state CSRF without credential URLs', async () => {
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify(state())))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    const api = new ProxyClient(fetcher);
    await api.state();
    await api.config(state({ revision: 7 }), [route]);
    expect(fetcher.mock.calls[1]).toEqual([
      '/_proxy/api/config',
      expect.objectContaining({
        method: 'PUT',
        credentials: 'same-origin',
        cache: 'no-store',
        redirect: 'error',
        headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-token' }),
        body: JSON.stringify({
          revision: 7,
          routes: [{ ...route, serves: undefined }],
          providers: {},
          defaults: null,
        }),
      }),
    ]);
  });
  it('round-trips stored providers and defaults without sending display-only route fields', async () => {
    const fetcher = vi
      .fn<typeof fetch>()
      .mockImplementation(async () => new Response('{"revision":8}'));
    const api = new ProxyClient(fetcher);
    const exa = { ...route, name: 'exa', provider: 'exa', profile: 'exa', serves: ['search: exa'] };
    const snapshot = state({
      routes: [exa],
      defaults: { search: 'exa' },
      providers: [
        { id: 'exa', label: 'Exa', family: 'search', routes: ['exa'], status: 'green', reason: '' },
      ],
    });
    await api.config(snapshot, [{ ...exa, enabled: false }]);
    const config = JSON.parse(String(fetcher.mock.calls[0][1]?.body));
    expect(config).toEqual({
      revision: 7,
      providers: { exa: { label: 'Exa' } },
      defaults: { search: 'exa' },
      routes: [{ ...exa, serves: undefined, enabled: false }],
    });
    await api.deleteProvider(snapshot.revision, 'exa');
    expect(fetcher.mock.calls[1]).toEqual([
      '/_proxy/api/providers/exa',
      expect.objectContaining({
        method: 'DELETE',
        body: JSON.stringify({ revision: 7 }),
      }),
    ]);
  });
  it('sends login/logout and consumes login csrf', async () => {
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response('{"csrf":"session"}'))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    const api = new ProxyClient(fetcher);
    await api.login('admin');
    await api.logout();
    expect(fetcher.mock.calls[0][0]).toBe('/_proxy/api/login');
    expect(fetcher.mock.calls[0][1]?.body).toBe('{"token":"admin"}');
    expect(fetcher.mock.calls[1][1]?.headers).toMatchObject({ 'X-CSRF-Token': 'session' });
  });
  it('encodes stable cursors and filters and keeps export authenticated', async () => {
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValue(new Response('{"requests":[],"next":0}'));
    const api = new ProxyClient(fetcher);
    await api.requests({ before: '42', path: '/a?x=1&y=2', caller: 'a b' });
    const path = String(fetcher.mock.calls[0][0]);
    expect(new URL(path, 'https://local').searchParams.get('path')).toBe('/a?x=1&y=2');
    expect(new URL(path, 'https://local').searchParams.get('before')).toBe('42');
  });
  it('does not expose response bodies on failed secret writes', async () => {
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValue(new Response('leaked-secret', { status: 409 }));
    const api = new ProxyClient(fetcher);
    await expect(api.secret('key', 'credential')).rejects.toThrow('Conflict');
    await expect(api.deleteSecret('key')).rejects.not.toThrow('leaked-secret');
  });
});

const presets: NonNullable<ProxyState['presets']> = [
  {
    family: 'inference',
    id: 'openai',
    label: 'OpenAI API',
    upstream: 'https://api.openai.com/v1',
    auth: { header: 'Authorization', prefix: 'Bearer ' },
  },
  {
    family: 'inference',
    id: 'anthropic',
    label: 'Anthropic API',
    upstream: 'https://api.anthropic.com',
    auth: { header: 'x-api-key', prefix: '' },
  },
  {
    family: 'inference',
    id: 'ollama',
    label: 'Ollama',
    upstream: 'http://127.0.0.1:11434',
    auth: { header: '', prefix: '' },
  },
  {
    family: 'inference',
    id: 'vllm',
    label: 'vLLM',
    upstream: '',
    auth: { header: 'Authorization', prefix: 'Bearer ' },
  },
];

describe('provider presets', () => {
  it.each(['openai', 'anthropic'])(
    'creates editable %s presets atomically with an existing key',
    async (id) => {
      const preset = presets.find((p) => p.id === id)!;
      const { root, api } = setup(
        state({ presets, secrets: [{ name: 'provider-key', routes: null, source: 'stored' }] }),
      );
      const save = vi.spyOn(api, 'createProvider').mockResolvedValue({ revision: 8 });
      await click(root, 'Add provider');
      await click(root, preset.label.replace(/ API$/, ''));
      expect(getByLabelText(root, 'Provider name')).toHaveValue(id);
      expect(getByLabelText(root, 'Upstream base URL')).toHaveValue(preset.upstream);
      fireEvent.change(getByLabelText(root, 'Credential source'), {
        target: { value: 'existing' },
      });
      fireEvent.change(getByLabelText(root, 'Secret name'), { target: { value: 'provider-key' } });
      fireEvent.input(getByLabelText(root, 'Provider name'), { target: { value: `${id}-mine` } });
      await click(root, 'Create provider');
      await waitFor(() =>
        expect(save).toHaveBeenCalledWith(7, {
          name: `${id}-mine`,
          label: preset.label,
          preset: id,
          secret: 'provider-key',
        }),
      );
    },
  );
  it('lets you cancel an unsaved draft and choose a different preset', async () => {
    const { root, api } = setup(state({ presets }));
    const save = vi.spyOn(api, 'config');
    await click(root, 'Add provider');
    await click(root, 'Custom endpoint');
    fireEvent.input(getByLabelText(root, 'Name'), { target: { value: 'my-route' } });
    await click(root, 'Cancel');
    await click(root, 'Add provider');
    await click(root, 'Anthropic');
    expect(getByLabelText(root, 'Provider name')).toHaveValue('anthropic');
    expect(save).not.toHaveBeenCalled();
  });
  it('does not offer presets when editing an existing route', async () => {
    const { root } = setup(state({ presets, routes: [route] }));
    await openManage(root, 'Example');
    await click(root, 'Edit Example');
    expect(root.querySelector('.route-form label')?.textContent).toBe('Name');
    expect(root.querySelector('.route-form')?.textContent).not.toContain(
      'Editable API-key defaults',
    );
  });
  it('requires an upstream for vLLM and offers keyless setup', async () => {
    const { root } = setup(state({ presets }));
    await click(root, 'Add provider');
    await click(root, 'vLLM');
    expect(getByLabelText(root, 'Upstream base URL')).toBeRequired();
    expect(getByLabelText(root, 'Credential source')).toHaveValue('');
  });
});

describe('proxy admin DOM', () => {
  it('onboards an empty config and saves a route at its loaded revision', async () => {
    const { root, api } = setup();
    const save = vi.spyOn(api, 'config').mockResolvedValue();
    await waitFor(() => expect(root.textContent).toContain('Add your first provider'));
    await click(root, 'Add provider');
    await click(root, 'Custom endpoint');
    fireEvent.input(getByLabelText(root, 'Name'), { target: { value: 'Example' } });
    fireEvent.input(getByLabelText(root, 'Upstream base URL', { exact: false }), {
      target: { value: 'https://example.com/api/' },
    });
    await click(root, 'Save route');
    await waitFor(() =>
      expect(save).toHaveBeenCalledWith(
        expect.objectContaining({ revision: 7 }),
        [expect.objectContaining({ name: 'Example', upstream: 'https://example.com/api/' })],
        expect.any(Object),
        undefined,
      ),
    );
  });
  it('file mode exposes reload/export, not route or secret writes', async () => {
    const { root, api } = setup(
      state({
        mode: 'file',
        routes: [route],
        secrets: [{ name: 'key', routes: null, source: 'file' }],
      }),
    );
    const reload = vi.spyOn(api, 'reload').mockResolvedValue();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    await click(root, 'Reload file');
    await waitFor(() => expect(reload).toHaveBeenCalledOnce());
    expect(queryByRole(root, 'button', { name: 'Add provider' })).toBeNull();
    await click(root, 'Admin');
    expect(queryByRole(root, 'button', { name: 'Download routes.yaml' })).not.toBeNull();
    expect(queryByRole(root, 'button', { name: 'Replace routes from YAML' })).toBeNull();
    expect(root.textContent).toContain('Edit that file and use Reload file');
    await click(root, 'Secrets');
    expect(queryByRole(root, 'button', { name: 'Replace key' })).toBeNull();
    expect(root.textContent).toContain('file · not used by any route');
  });
  it('returns to sign-in on 401 without persisting the administrator token', async () => {
    const { root, api } = setup();
    await waitFor(() => expect(root.textContent).toContain('Add your first provider'));
    vi.mocked(api.state).mockRejectedValueOnce(new ProxyAPIError(401, 'Sign in'));
    await click(root, 'Refresh state');
    await waitFor(() => expect(getByLabelText(root, /Administrator token/)).toBeTruthy());
    vi.spyOn(api, 'login').mockResolvedValue();
    fireEvent.input(getByLabelText(root, /Administrator token/), {
      target: { value: 'admin-secret' },
    });
    await click(root, 'Sign in');
    await waitFor(() => expect(api.login).toHaveBeenCalledWith('admin-secret'));
    expect(root.textContent).not.toContain('admin-secret');
  });
  it('uses explicit provider approval and displays a token only until dismissed', async () => {
    const { root, api } = setup(state({ routes: [route] }));
    const create = vi.spyOn(api, 'createProviderCaller').mockResolvedValue({
      token: 'once-only',
      caller: {
        id: 'c',
        name: 'Client',
        created: '',
        revoked: false,
        grants: [route.name],
        created_by: '',
        external_id: '',
      },
    });
    await click(root, 'Clients');
    expect((getByLabelText(root, 'Example') as HTMLInputElement).checked).toBe(false);
    fireEvent.input(getByLabelText(root, 'Client name'), { target: { value: 'Client' } });
    fireEvent.click(getByLabelText(root, 'Example'));
    await click(root, 'Create client');
    await waitFor(() => expect(create).toHaveBeenCalledWith('Client', ['Example'], 7));
    await waitFor(() =>
      expect((getByLabelText(root, 'Client token') as HTMLTextAreaElement).value).toBe('once-only'),
    );
    expect(root.textContent).toContain('curl -H');
    await click(root, 'I saved it — dismiss');
    expect(root.textContent).not.toContain('once-only');
  });
  it('renders untrusted provider labels as text without markup', async () => {
    const { root } = setup(
      state({
        routes: [route],
        providers: [
          {
            id: route.provider,
            label: '<img src=x onerror=alert(1)>',
            family: 'inference',
            status: 'green',
            reason: '',
            routes: [route.name],
          },
        ],
      }),
    );
    await waitFor(() => expect(root.textContent).toContain('<img src=x'));
    expect(root.querySelector('img')).toBeNull();
  });
  it('applies request filters from page one and keeps metadata details available', async () => {
    const { root, api } = setup();
    const requests = vi.spyOn(api, 'requests').mockResolvedValue({
      requests: [
        {
          id: 'req1',
          seq: 42,
          started: 1700000000000,
          ended: 1700000000010,
          caller_id: 'c',
          caller_name: 'Client',
          route: 'Example',
          revision: 7,
          method: 'GET',
          path: '/data',
          peer: '127.0.0.1',
          status: 200,
          outcome: 'ok',
          sent: 3,
          received: 4,
          header_ms: 1,
          first_byte_ms: 2,
          total_ms: 10,
          upstream_id: 'up1',
        },
      ],
      next: 42,
      total: 51,
    });
    await click(root, 'Requests');
    await click(root, 'GET /data');
    expect(root.textContent).toContain('Upstream request ID');
    await click(getByRole(root, 'navigation', { name: 'Request pages' }), 'Next');
    await waitFor(() =>
      expect(requests).toHaveBeenLastCalledWith(expect.objectContaining({ page: '2' })),
    );
    openDetails(root, 'Filters');
    fireEvent.input(getByLabelText(root, 'Path'), { target: { value: '/other' } });
    await click(root, 'Apply filters');
    await waitFor(() =>
      expect(requests).toHaveBeenLastCalledWith({ path: '/other', page: '1', page_size: '25' }),
    );
  });
  it('shell-quotes curl examples and uses only the client token', () => {
    expect(
      callerCurl({ ...route, caller_auth: { header: 'X-Auth', prefix: "a'b " } }, 'https://local'),
    ).toBe("curl -H 'X-Auth: a'\\''b '\"$TOKEN\" -- 'https://local/example/'");
  });
});

describe('proxy configuration and credential operations', () => {
  it.each(['pay_as_you_go', 'plan', 'local', 'unspecified'] as const)(
    'edits billing to %s without dropping hidden route security policy',
    async (billing) => {
      const original: ProxyAdminRoute = {
        ...route,
        account: 'trusted',
        websocket: true,
        allow: ['POST /responses'],
        redirects: 'refuse',
        usage: 'auto',
        billing: 'plan',
      };
      const { root, api } = setup(state({ routes: [original] }));
      const config = vi.spyOn(api, 'config').mockResolvedValue();
      const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
      await openManage(root, 'Example');
      await click(root, 'Edit Example');
      fireEvent.change(getByLabelText(root, 'Billing'), {
        target: { value: billing },
      });
      await click(root, 'Save route');
      await waitFor(() =>
        expect(config).toHaveBeenCalledWith(expect.objectContaining({ revision: 7 }), [
          expect.objectContaining({ ...original, billing }),
        ]),
      );
      expect(confirm).not.toHaveBeenCalled();
    },
  );
  it('defaults new routes to unspecified billing, not an assumed API price', async () => {
    const { root, api } = setup();
    const config = vi.spyOn(api, 'config').mockResolvedValue();
    await click(root, 'Add provider');
    await click(root, 'Custom endpoint');
    expect((getByLabelText(root, 'Billing') as HTMLSelectElement).value).toBe('unspecified');
    fireEvent.input(getByLabelText(root, 'Name'), { target: { value: 'new-api' } });
    fireEvent.input(getByLabelText(root, 'Upstream base URL'), {
      target: { value: 'https://example.com/' },
    });
    await click(root, 'Save route');
    await waitFor(() =>
      expect(config).toHaveBeenCalledWith(
        expect.objectContaining({ revision: 7 }),
        [expect.objectContaining({ billing: 'unspecified' })],
        expect.any(Object),
        undefined,
      ),
    );
  });

  it('handles null empty-install collections from Go', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(
        JSON.stringify({
          ...state(),
          routes: null,
          secrets: null,
          callers: null,
          admin_keys: null,
        }),
      ),
    );
    expect(await new ProxyClient(fetcher).state()).toMatchObject({
      routes: [],
      secrets: [],
      callers: [],
      admin_keys: [],
    });
  });
  it('disables and deletes routes using the loaded revision', async () => {
    const { root, api } = setup(state({ routes: [route] }));
    const config = vi.spyOn(api, 'config').mockResolvedValue();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    await openManage(root, 'Example');
    await click(root, 'Disable Example');
    await waitFor(() =>
      expect(config).toHaveBeenCalledWith(expect.objectContaining({ revision: 7 }), [
        { ...route, enabled: false },
      ]),
    );
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    await openManage(root, 'Example');
    await click(root, 'Edit Example');
    await click(root, 'Delete route Example');
    await waitFor(() =>
      expect(config).toHaveBeenLastCalledWith(expect.objectContaining({ revision: 7 }), []),
    );
  });
  it('supports structured secret headers independent of upstream origin', async () => {
    const { root, api } = setup(
      state({
        routes: [route],
        secrets: [{ name: 'key', routes: null, source: 'stored' }],
      }),
    );
    const config = vi.spyOn(api, 'config').mockResolvedValue();
    await openManage(root, 'Example');
    await click(root, 'Edit Example');
    await click(root, 'Add substitution');
    fireEvent.input(getByLabelText(root, 'Header name'), { target: { value: 'Authorization' } });
    fireEvent.change(getByLabelText(root, 'Source'), { target: { value: 'secret' } });
    fireEvent.change(getByLabelText(root, 'Secret name'), { target: { value: 'key' } });
    fireEvent.input(getByLabelText(root, /Secret prefix/), { target: { value: 'Bearer ' } });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    await click(root, 'Save route');
    await waitFor(() =>
      expect(config).toHaveBeenCalledWith(expect.objectContaining({ revision: 7 }), [
        expect.objectContaining({
          set_headers: { Authorization: { secret: 'key', prefix: 'Bearer ' } },
        }),
      ]),
    );
  });
  it('writes a secret once and disables deletion while referenced', async () => {
    const { root, api } = setup(
      state({
        routes: [{ ...route, set_headers: { Authorization: { secret: 'key' } } }],
        secrets: [{ name: 'key', routes: ['Example'], source: 'stored' }],
      }),
    );
    const secret = vi.spyOn(api, 'secret').mockResolvedValue();
    await click(root, 'Secrets');
    expect(getByRole(root, 'button', { name: 'Delete secret key' })).toBeDisabled();
    await click(root, 'Replace key');
    expect(queryByLabelText(root, /Bound origin|Use route origin/)).toBeNull();
    expect(root).toHaveTextContent('stored · used by Example');
    fireEvent.input(getByLabelText(root, /New secret value/), {
      target: { value: 'opaque-credential-123' },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    await click(root, 'Replace secret');
    await waitFor(() => expect(secret).toHaveBeenCalledWith('key', 'opaque-credential-123', true));
    expect(root.textContent).not.toContain('opaque-credential-123');
  });
  it('keeps configuration transfer on Admin, not Routes, with no nested disclosures', async () => {
    const { root } = setup();
    await click(root, 'Add provider');
    expect(queryByRole(root, 'button', { name: 'Download routes.yaml' })).toBeNull();
    expect(queryByRole(root, 'button', { name: 'Replace routes from YAML' })).toBeNull();
    expect(root.querySelector('input[type=file], textarea')).toBeNull();
    await click(root, 'Admin');
    expect(getByRole(root, 'heading', { name: 'Admin' })).toBeVisible();
    expect(getByRole(root, 'button', { name: 'Download routes.yaml' })).toBeVisible();
    expect(getByRole(root, 'button', { name: 'Replace routes from YAML' })).toBeVisible();
    expect(root.querySelector('main details')).toBeNull();
    const source = getByLabelText(root, 'Import source') as HTMLSelectElement;
    expect([...source.options].map((option) => option.text)).toEqual(['File', 'Paste YAML']);
    expect(getByLabelText(root, 'YAML file')).toBeVisible();
    expect(getByLabelText(root, 'Route configuration YAML')).not.toBeVisible();
    fireEvent.change(source, { target: { value: 'paste' } });
    expect(getByLabelText(root, 'YAML file')).not.toBeVisible();
    expect(getByLabelText(root, 'Route configuration YAML')).toBeVisible();
    fireEvent.change(source, { target: { value: 'file' } });
    expect(getByLabelText(root, 'YAML file')).toBeVisible();
    expect(getByLabelText(root, 'Route configuration YAML')).not.toBeVisible();
  });
  it('imports secret-free YAML with revision and explicit confirmation', async () => {
    const { root, api } = setup();
    const importYAML = vi.spyOn(api, 'importYAML').mockResolvedValue();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    await waitFor(() => expect(root.textContent).toContain('Add your first provider'));
    await click(root, 'Admin');
    fireEvent.change(getByLabelText(root, 'Import source'), { target: { value: 'paste' } });
    fireEvent.input(getByLabelText(root, 'Route configuration YAML'), {
      target: { value: 'routes: []' },
    });
    await click(root, 'Replace routes from YAML');
    await waitFor(() => expect(importYAML).toHaveBeenCalledWith(7, 'routes: []'));
  });
  it('replaces routes from a selected file without opening the paste editor', async () => {
    const { root, api } = setup();
    const importYAML = vi.spyOn(api, 'importYAML').mockResolvedValue();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
    await waitFor(() => expect(root.textContent).toContain('Add your first provider'));
    await click(root, 'Admin');
    expect(root.textContent).toContain('This is not a full backup');
    expect(getByLabelText(root, 'Route configuration YAML')).not.toBeVisible();
    const file = new File(['routes: []'], 'routes.yaml', { type: 'text/yaml' });
    Object.defineProperty(file, 'text', { value: vi.fn().mockResolvedValue('routes: []') });
    fireEvent.change(getByLabelText(root, 'YAML file'), { target: { files: [file] } });
    await click(root, 'Replace routes from YAML');
    await waitFor(() => expect(importYAML).toHaveBeenCalledWith(7, 'routes: []'));
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('lose client grants'));
  });
  it('does not replace routes when confirmation is cancelled', async () => {
    const { root, api } = setup();
    const importYAML = vi.spyOn(api, 'importYAML');
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    await waitFor(() => expect(root.textContent).toContain('Add your first provider'));
    await click(root, 'Admin');
    fireEvent.change(getByLabelText(root, 'Import source'), { target: { value: 'paste' } });
    fireEvent.input(getByLabelText(root, 'Route configuration YAML'), {
      target: { value: 'routes: []' },
    });
    await click(root, 'Replace routes from YAML');
    await waitFor(() => expect(confirm).toHaveBeenCalledOnce());
    expect(importYAML).not.toHaveBeenCalled();
  });
  it.each(['missing', 'oversize', 'unreadable', 'empty'] as const)(
    'rejects a %s configuration file before confirmation or replacement',
    async (kind) => {
      const { root, api } = setup();
      const importYAML = vi.spyOn(api, 'importYAML');
      const confirm = vi.spyOn(window, 'confirm');
      await waitFor(() => expect(root.textContent).toContain('Add your first provider'));
      await click(root, 'Admin');
      const read =
        kind === 'unreadable'
          ? vi.fn().mockRejectedValue(new Error('private file error'))
          : vi.fn().mockResolvedValue(kind === 'empty' ? '  ' : 'routes: []');
      if (kind !== 'missing') {
        const file = new File(['routes: []'], 'routes.yaml');
        Object.defineProperty(file, 'text', { value: read });
        if (kind === 'oversize') Object.defineProperty(file, 'size', { value: (1 << 20) + 1 });
        fireEvent.change(getByLabelText(root, 'YAML file'), { target: { files: [file] } });
      }
      await click(root, 'Replace routes from YAML');
      const message = {
        missing: 'Choose a YAML file',
        oversize: '1 MiB or smaller',
        unreadable: 'Could not read',
        empty: 'configuration is empty',
      }[kind];
      await waitFor(() =>
        expect(root.querySelector('[role=status]')?.textContent).toContain(message),
      );
      expect(confirm).not.toHaveBeenCalled();
      expect(importYAML).not.toHaveBeenCalled();
      if (kind === 'oversize') expect(read).not.toHaveBeenCalled();
      expect(root.textContent).not.toContain('private file error');
    },
  );
  it.each(['paste', 'file'] as const)(
    'bounds decoded %s YAML by UTF-8 bytes before confirmation',
    async (source) => {
      const { root, api } = setup();
      const importYAML = vi.spyOn(api, 'importYAML');
      const confirm = vi.spyOn(window, 'confirm');
      await click(root, 'Admin');
      const contents = 'é'.repeat((1 << 19) + 1);
      if (source === 'paste') {
        fireEvent.change(getByLabelText(root, 'Import source'), { target: { value: 'paste' } });
        fireEvent.input(getByLabelText(root, 'Route configuration YAML'), {
          target: { value: contents },
        });
      } else {
        const file = new File(['small'], 'routes.yaml');
        Object.defineProperty(file, 'text', { value: vi.fn().mockResolvedValue(contents) });
        fireEvent.change(getByLabelText(root, 'YAML file'), { target: { files: [file] } });
      }
      await click(root, 'Replace routes from YAML');
      await waitFor(() =>
        expect(root.querySelector('[role=status]')).toHaveTextContent('1 MiB or smaller'),
      );
      expect(confirm).not.toHaveBeenCalled();
      expect(importYAML).not.toHaveBeenCalled();
    },
  );
  it('revokes a caller only after confirmation', async () => {
    const { root, api } = setup(
      state({
        callers: [
          {
            id: 'c1',
            name: 'Client',
            created: 1700000000000,
            revoked: false,
            grants: [],
            created_by: '',
            external_id: '',
          },
        ],
      }),
    );
    const revoke = vi.spyOn(api, 'revokeCaller').mockResolvedValue();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    await click(root, 'Clients');
    expect(queryByRole(root, 'button', { name: 'Revoke Client' })).toBeNull();
    await click(root, 'Client');
    await click(root, 'Revoke Client');
    expect(revoke).not.toHaveBeenCalled();
    confirm.mockReturnValue(true);
    await click(root, 'Revoke Client');
    await waitFor(() => expect(revoke).toHaveBeenCalledWith('c1'));
  });
});

describe('acceptance refinements', () => {
  it('shows only bounded curated JSON errors for 400/409, never HTML or other statuses', async () => {
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: '<b>Prefixes overlap</b>' }), {
          status: 400,
          headers: { 'Content-Type': 'application/json; charset=utf-8' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: 'x'.repeat(600) }), {
          status: 409,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response('<b>arbitrary-body</b>', {
          status: 400,
          headers: { 'Content-Type': 'text/html' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: 'arbitrary-body' }), {
          status: 500,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response('not JSON', { status: 409, headers: { 'Content-Type': 'application/json' } }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: 'z'.repeat(5000) }), {
          status: 409,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    const api = new ProxyClient(fetcher);
    await expect(api.config(state({ revision: 7 }), [])).rejects.toThrow('<b>Prefixes overlap</b>');
    await expect(api.config(state({ revision: 7 }), [])).rejects.toMatchObject({
      message: 'x'.repeat(512),
    });
    await expect(api.config(state({ revision: 7 }), [])).rejects.toThrow('Invalid input');
    await expect(api.config(state({ revision: 7 }), [])).rejects.toThrow(
      'Proxy request failed (500)',
    );
    await expect(api.config(state({ revision: 7 }), [])).rejects.toThrow('Conflict');
    await expect(api.config(state({ revision: 7 }), [])).rejects.toThrow('Conflict');
  });
  it('sends explicit replacement intent and state revisions for caller grant writes', async () => {
    const fetcher = vi.fn<typeof fetch>().mockImplementation(async () => new Response('{}'));
    const api = new ProxyClient(fetcher);
    await api.secret('key', 'first');
    await api.secret('key', 'second', true);
    await api.createCaller('Client', ['Example'], 7);
    await api.updateCaller('c1', [], 7);
    const bodies = fetcher.mock.calls.map(([, options]) => JSON.parse(String(options?.body)));
    expect(bodies).toEqual([
      { name: 'key', value: 'first' },
      { name: 'key', value: 'second', replace: true },
      { name: 'Client', grants: ['Example'], revision: 7 },
      { id: 'c1', grants: [], revision: 7 },
    ]);
    expect(fetcher.mock.calls[3][1]?.method).toBe('PUT');
  });
  it('guards tabs, refresh, sign-out and browser leave while a one-time token is shown', async () => {
    const { root, api } = setup(state({ routes: [route] }));
    vi.spyOn(api, 'createProviderCaller').mockResolvedValue({
      token: 'private-once',
      caller: {
        id: 'new',
        name: 'New',
        created: 1,
        revoked: false,
        grants: ['Example'],
        created_by: '',
        external_id: '',
      },
    });
    const logout = vi.spyOn(api, 'logout').mockResolvedValue();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const copy = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: copy },
    });
    try {
      await click(root, 'Clients');
      fireEvent.input(getByLabelText(root, 'Client name'), { target: { value: 'New' } });
      fireEvent.click(getByLabelText(root, 'Example'));
      await click(root, 'Create client');
      await waitFor(() =>
        expect((getByLabelText(root, 'Client token') as HTMLTextAreaElement).value).toBe(
          'private-once',
        ),
      );
      expect(root.textContent).not.toContain('private-once');
      expect(root.textContent).toContain('$TOKEN');
      await click(root, 'Copy token');
      await waitFor(() => expect(copy).toHaveBeenCalledWith('private-once'));
      expect(getByRole(root, 'button', { name: 'Copy token' }).textContent).toBe('Copied ✓');
      expect((getByLabelText(root, 'Client token') as HTMLInputElement).type).toBe('text');
      for (const action of ['Providers', 'Refresh state', 'Sign out']) await click(root, action);
      expect(confirm).toHaveBeenCalledTimes(3);
      expect(logout).not.toHaveBeenCalled();
      expect(api.state).toHaveBeenCalledTimes(1);
      expect(getByLabelText(root, 'Client token')).toBeTruthy();
      vi.mocked(api.state).mockRejectedValueOnce(new Error('Temporary read failure'));
      confirm.mockReturnValueOnce(true);
      await click(root, 'Refresh state');
      await waitFor(() => expect(root.textContent).toContain('Temporary read failure'));
      expect(getByLabelText(root, 'Client token')).toBeTruthy();
      const leave = new Event('beforeunload', { cancelable: true });
      window.dispatchEvent(leave);
      expect(leave.defaultPrevented).toBe(true);
      await click(root, 'I saved it — dismiss');
      await waitFor(() => expect(queryByRole(root, 'button', { name: 'Copy token' })).toBeNull());
      const dismissed = new Event('beforeunload', { cancelable: true });
      window.dispatchEvent(dismissed);
      expect(dismissed.defaultPrevented).toBe(false);
    } finally {
      Reflect.deleteProperty(navigator, 'clipboard');
    }
  });
  it('allows confirmed navigation away from a token and clears its stale announcement', async () => {
    const { root, api } = setup(state({ routes: [route] }));
    vi.spyOn(api, 'createProviderCaller').mockResolvedValue({
      token: 'private-once',
      caller: {
        id: 'new',
        name: 'New',
        created: 1,
        revoked: false,
        grants: ['Example'],
        created_by: '',
        external_id: '',
      },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    await click(root, 'Clients');
    fireEvent.input(getByLabelText(root, 'Client name'), { target: { value: 'New' } });
    fireEvent.click(getByLabelText(root, 'Example'));
    await click(root, 'Create client');
    await waitFor(() => expect(root.textContent).toContain('Created client New'));
    await click(root, 'Providers');
    expect(queryByRole(root, 'textbox', { name: 'Client token' })).toBeNull();
    expect(root.querySelector(':scope > [role=status]')?.textContent).toBe('');
    expect(document.activeElement).toBe(root.querySelector('main h2'));
  });
  it('suggests an editable prefix from the name, folds Advanced, and defaults substitutions to secrets', async () => {
    const { root } = setup();
    await click(root, 'Add provider');
    await click(root, 'Custom endpoint');
    const local = getByLabelText(root, 'Local prefix') as HTMLInputElement;
    expect(local.value).toBe('');
    fireEvent.input(getByLabelText(root, 'Name'), { target: { value: 'alpha' } });
    expect(local.value).toBe('/alpha/');
    fireEvent.input(local, { target: { value: '/custom/' } });
    fireEvent.input(getByLabelText(root, 'Name'), { target: { value: 'beta' } });
    expect(local.value).toBe('/custom/');
    expect(
      getByLabelText(root, 'Local client authentication header').closest('details')?.open,
    ).toBe(false);
    expect(queryByRole(root, 'button', { name: 'Download routes.yaml' })).toBeNull();
    await click(root, 'Add substitution');
    expect((getByLabelText(root, 'Source') as HTMLSelectElement).value).toBe('secret');
    fireEvent.input(getByLabelText(root, 'Header name'), { target: { value: 'X-API-Key' } });
    fireEvent.change(getByLabelText(root, 'Source'), { target: { value: 'value' } });
    expect(root.textContent).toContain('This looks like a credential header');
    await click(root, 'Cancel');
    expect(document.activeElement).toBe(getByRole(root, 'button', { name: 'Add provider' }));
  });
  it('prefills a name-only secret dialog', async () => {
    const { root } = setup(state({ routes: [route] }));
    await openManage(root, 'Example');
    await click(root, 'Edit Example');
    await click(root, 'Add substitution');
    await click(root, 'Add API key');
    const dialog = getByRole(root, 'dialog', { name: 'Add API key' });
    expect(getByLabelText(dialog, 'New secret value')).toHaveFocus();
    expect(queryByLabelText(dialog, 'Bound origin')).toBeNull();
  });
  it('stores a key in place, preserves all route fields and saves at the new revision', async () => {
    const { root, api } = setup(state({ presets }));
    const write = vi.spyOn(api, 'secret').mockResolvedValue();
    const save = vi.spyOn(api, 'config').mockResolvedValue();
    await openKeyDraft(root);
    fireEvent.input(getByLabelText(root, 'Name'), { target: { value: 'my-openai' } });
    fireEvent.input(getByLabelText(root, 'Local prefix'), {
      target: { value: '/my-custom-prefix/' },
    });
    const routeForm = root.querySelector('.route-form');
    await click(root, 'Add API key');
    const dialog = getByRole(root, 'dialog', { name: 'Add API key' });
    expect(dialog.closest('.route-form')).toBeNull();
    expect(getByLabelText(dialog, 'Secret name')).toHaveValue('my-openai-key');
    fireEvent.input(getByLabelText(dialog, 'New secret value'), {
      target: { value: 'private-api-key' },
    });
    vi.mocked(api.state).mockResolvedValue(
      state({
        presets,
        revision: 8,
        secrets: [{ name: 'my-openai-key', routes: null, source: 'stored' }],
      }),
    );
    await click(dialog, 'Store secret');
    await waitFor(() => expect(root.querySelector('dialog')).toBeNull());
    expect(write).toHaveBeenCalledWith('my-openai-key', 'private-api-key', false);
    expect(root.querySelector('.route-form')).toBe(routeForm);
    expect(getByRole(root, 'heading', { name: 'Custom endpoint' })).toBeVisible();
    expect(getByLabelText(root, 'Name')).toHaveValue('my-openai');
    expect(getByLabelText(root, 'Local prefix')).toHaveValue('/my-custom-prefix/');
    expect(getByLabelText(root, 'Secret name')).toHaveValue('my-openai-key');
    expect(root.textContent).toContain('Your route is not saved yet');
    expect(save).not.toHaveBeenCalled();
    await click(root, 'Save route');
    await waitFor(() =>
      expect(save).toHaveBeenCalledWith(
        expect.objectContaining({ revision: 8 }),
        [expect.objectContaining({ name: 'my-openai', local_prefix: '/my-custom-prefix/' })],
        expect.any(Object),
        undefined,
      ),
    );
    expect(root.textContent).not.toContain('private-api-key');
  });
  it('updates shared secret state without re-rendering the route', async () => {
    const { root, api } = setup(state({ presets }));
    vi.spyOn(api, 'secret').mockResolvedValue();
    await openKeyDraft(root);
    await click(root, 'Add API key');
    const dialog = getByRole(root, 'dialog', { name: 'Add API key' });
    fireEvent.input(getByLabelText(dialog, 'New secret value'), {
      target: { value: 'private-api-key' },
    });
    vi.mocked(api.state).mockResolvedValue(
      state({
        presets,
        revision: 8,
        secrets: [{ name: 'openai-key', routes: null, source: 'stored' }],
      }),
    );
    await click(dialog, 'Store secret');
    await waitFor(() => expect(root.querySelector('dialog')).toBeNull());
    expect(root.querySelector('header')?.textContent).not.toMatch(/managed|Revision/);
    await click(root, 'Secrets');
    expect(getByRole(root, 'heading', { name: 'openai-key' })).toBeVisible();
  });
  it('cancels the key dialog without losing the route or storing the key', async () => {
    const { root, api } = setup(state({ presets }));
    const write = vi.spyOn(api, 'secret');
    await openKeyDraft(root);
    await click(root, 'Add API key');
    const dialog = getByRole(root, 'dialog', { name: 'Add API key' });
    const key = getByLabelText(dialog, 'New secret value') as HTMLInputElement;
    fireEvent.input(key, { target: { value: 'not-saved' } });
    await click(dialog, 'Cancel');
    expect(root.querySelector('dialog')).toBeNull();
    expect(key.value).toBe('');
    expect(getByLabelText(root, 'Name')).toHaveValue('openai');
    expect(write).not.toHaveBeenCalled();
  });
  it('keeps key-write failures visible inside the dialog and never saves a route', async () => {
    const { root, api } = setup(state({ presets }));
    vi.spyOn(api, 'secret').mockRejectedValue(
      new ProxyAPIError(409, 'Secret name already exists.'),
    );
    const save = vi.spyOn(api, 'config');
    await openKeyDraft(root);
    await click(root, 'Add API key');
    const dialog = getByRole(root, 'dialog', { name: 'Add API key' });
    fireEvent.input(getByLabelText(dialog, 'New secret value'), {
      target: { value: 'private-api-key' },
    });
    await click(dialog, 'Store secret');
    await waitFor(() =>
      expect(dialog.querySelector('[role=alert]')?.textContent).toContain(
        'Secret name already exists',
      ),
    );
    expect(getByLabelText(dialog, 'New secret value')).toHaveValue('');
    expect(save).not.toHaveBeenCalled();
    expect(getByLabelText(root, 'Name')).toHaveValue('openai');
  });
  it('does not rebase an unfinished route over concurrent route edits after key storage', async () => {
    const { root, api } = setup(state({ presets }));
    vi.spyOn(api, 'secret').mockResolvedValue();
    const save = vi
      .spyOn(api, 'config')
      .mockRejectedValue(new ProxyAPIError(409, 'Configuration changed; refresh before saving.'));
    await openKeyDraft(root);
    await click(root, 'Add API key');
    const dialog = getByRole(root, 'dialog', { name: 'Add API key' });
    fireEvent.input(getByLabelText(dialog, 'New secret value'), {
      target: { value: 'private-api-key' },
    });
    vi.mocked(api.state).mockResolvedValue(
      state({
        presets,
        revision: 9,
        routes: [route],
        secrets: [{ name: 'openai-key', routes: null, source: 'stored' }],
      }),
    );
    await click(dialog, 'Store secret');
    await waitFor(() => expect(root.querySelector('dialog')).toBeNull());
    expect(root.textContent).toContain('Routes changed elsewhere');
    await click(root, 'Save route');
    await waitFor(() =>
      expect(save).toHaveBeenCalledWith(
        expect.objectContaining({ revision: 7 }),
        [expect.objectContaining({ name: 'openai' })],
        expect.any(Object),
        undefined,
      ),
    );
    expect(getByLabelText(root, 'Name')).toHaveValue('openai');
  });
  it('returns to sign-in when authentication is lost during the key-state reload', async () => {
    const { root, api } = setup(state({ presets }));
    vi.spyOn(api, 'secret').mockResolvedValue();
    await openKeyDraft(root);
    await click(root, 'Add API key');
    const dialog = getByRole(root, 'dialog', { name: 'Add API key' });
    fireEvent.input(getByLabelText(dialog, 'New secret value'), {
      target: { value: 'private-api-key' },
    });
    vi.mocked(api.state).mockRejectedValue(new ProxyAPIError(401, 'Sign in again.'));
    await click(dialog, 'Store secret');
    await waitFor(() => expect(getByLabelText(root, 'Administrator token')).toBeVisible());
    expect(root.querySelector('dialog')).toBeNull();
    expect(root.textContent).not.toContain('private-api-key');
  });
  it('reports a committed key separately when metadata refresh fails', async () => {
    const { root, api } = setup(state({ presets }));
    vi.spyOn(api, 'secret').mockResolvedValue();
    await openKeyDraft(root);
    await click(root, 'Add API key');
    const dialog = getByRole(root, 'dialog', { name: 'Add API key' });
    fireEvent.input(getByLabelText(dialog, 'New secret value'), {
      target: { value: 'private-api-key' },
    });
    vi.mocked(api.state).mockRejectedValue(new Error('offline'));
    await click(dialog, 'Store secret');
    await waitFor(() => expect(root.querySelector('dialog')).toBeNull());
    expect(root.textContent).toContain('Secret stored; route not saved');
    expect(getByLabelText(root, 'Name')).toHaveValue('openai');
  });
  it('refuses accidental duplicate secret creation before any write', async () => {
    const { root, api } = setup(
      state({ secrets: [{ name: 'key', routes: null, source: 'stored' }] }),
    );
    const write = vi.spyOn(api, 'secret').mockResolvedValue();
    await click(root, 'Secrets');
    await click(root, 'Add secret');
    fireEvent.input(getByLabelText(root, 'Secret name'), { target: { value: 'key' } });
    fireEvent.input(getByLabelText(root, 'New secret value'), { target: { value: 'not-sent' } });
    await click(root, 'Store secret');
    await waitFor(() => expect(root.textContent).toContain('Use its Replace action'));
    expect(write).not.toHaveBeenCalled();
    expect(root.textContent).not.toContain('not-sent');
  });
  it('edits provider approvals at the loaded revision without reissuing a client token', async () => {
    const { root, api } = setup(
      state({
        routes: [route],
        callers: [
          {
            id: 'c1',
            name: 'Workstation',
            created: 1700000000000,
            last_used: 1700000001000,
            revoked: false,
            grants: ['Example'],
            created_by: '',
            external_id: '',
          },
        ],
      }),
    );
    const edit = vi.spyOn(api, 'updateProviderApprovals').mockResolvedValue();
    const create = vi.spyOn(api, 'createProviderCaller');
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    await click(root, 'Clients');
    expect(getByLabelText(root, 'Client name')).not.toBeVisible();
    const toggle = getByRole(root, 'button', { name: 'Workstation' });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    expect(root.textContent).toContain(new Date(1700000001000).toLocaleString());
    expect(root.textContent).not.toContain('1700000000000');
    const editor = root.querySelector<HTMLFormElement>('.client-detail form')!;
    fireEvent.click(getByLabelText(editor, 'Example'));
    await click(root, 'Save approvals');
    expect(edit).not.toHaveBeenCalled();
    confirm.mockReturnValue(true);
    await click(root, 'Save approvals');
    await waitFor(() =>
      expect(edit).toHaveBeenCalledWith('c1', [{ id: 'Example', approved: false }], 7),
    );
    expect(create).not.toHaveBeenCalled();
    await waitFor(() =>
      expect(root.textContent).toContain('Saved provider approvals for Workstation'),
    );
    expect(document.activeElement?.textContent).toBe('Workstation');
  });
  it('confirms authority changes, keeps cancellation safe, then focuses the saved route', async () => {
    const { root, api } = setup(state({ routes: [route] }));
    const write = vi.spyOn(api, 'config').mockResolvedValue();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    await openManage(root, 'Example');
    await click(root, 'Edit Example');
    fireEvent.input(getByLabelText(root, 'Upstream base URL'), {
      target: { value: 'https://example.com/wider/' },
    });
    await click(root, 'Save route');
    expect(write).not.toHaveBeenCalled();
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('grants will be dropped'));
    confirm.mockReturnValue(true);
    await click(root, 'Save route');
    await waitFor(() => expect(write).toHaveBeenCalledOnce());
    await waitFor(() => expect(root.textContent).toContain('Saved route Example'));
    expect(document.activeElement).toBe(root.querySelector('[data-focus="route:Example"]'));
    expect(document.activeElement?.tagName).toBe('LI');
    await click(root, 'Secrets');
    expect(root.textContent).not.toContain('Saved route Example');
  });
  it('displays full local paths, friendly outcomes, units and inline selected-row details', async () => {
    const { root, api } = setup();
    const base = {
      id: 'req',
      seq: 2,
      started: 1700000000000,
      ended: 1700000000010,
      caller_id: 'c',
      caller_name: 'Client',
      route: 'alpha',
      revision: 7,
      method: 'POST',
      local_prefix: '/alpha/',
      path: '/anything',
      peer: '127.0.0.1',
      status: 429,
      outcome: 'http_error',
      sent: 3,
      received: 93,
      header_ms: 1,
      first_byte_ms: 2,
      total_ms: 10,
      upstream_id: 'up1',
    };
    vi.spyOn(api, 'requests').mockResolvedValue({
      requests: [base, { ...base, id: 'req2', seq: 1, local_prefix: '/beta/', route: 'beta' }],
      next: 0,
    });
    await click(root, 'Requests');
    await click(root, 'POST /alpha/anything');
    const alpha = getByRole(root, 'button', { name: 'POST /alpha/anything' });
    expect(alpha).toHaveAttribute('aria-expanded', 'true');
    expect(alpha.closest('article')).toHaveClass('selected');
    expect(alpha.closest('article')?.textContent).toContain('93 B');
    expect(alpha.closest('article')?.textContent).toContain('10 ms');
    expect(alpha.closest('article')?.textContent).toContain('Upstream HTTP error');
    expect(
      getByRole(root, 'button', { name: 'POST /beta/anything' })
        .closest('article')
        ?.querySelector('.request-detail'),
    ).toBeNull();
    await click(root, 'POST /beta/anything');
    expect(alpha).toHaveAttribute('aria-expanded', 'false');
    expect(alpha.closest('article')?.querySelector('.request-detail')).toBeNull();
    expect(root.querySelector('.request-detail')?.closest('article')?.textContent).toContain(
      '/beta/anything',
    );
    expect(root.querySelector('details.request-filters')?.hasAttribute('open')).toBe(false);
    expect(outcomeLabel('transport_error')).toContain('result uncertain');
    expect(localPath({ local_prefix: '/alpha/', path: '/anything' })).toBe('/alpha/anything');
    expect(localPath({ path: '/anything' })).toBe('/anything');
  });
});

describe('pending issuance guard', () => {
  it('waits for caller creation before allowing a deliberate token-discard decision', async () => {
    const { root, api } = setup(state({ routes: [route] }));
    let complete!: (value: Awaited<ReturnType<ProxyClient['createProviderCaller']>>) => void;
    vi.spyOn(api, 'createProviderCaller').mockImplementation(
      () =>
        new Promise((resolve) => {
          complete = resolve;
        }),
    );
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
    await click(root, 'Clients');
    fireEvent.input(getByLabelText(root, 'Client name'), { target: { value: 'Pending' } });
    fireEvent.click(getByLabelText(root, 'Example'));
    await click(root, 'Create client');
    await waitFor(() => expect(api.createProviderCaller).toHaveBeenCalledOnce());
    await click(root, 'Providers');
    expect(confirm).not.toHaveBeenCalled();
    expect(root.textContent).toContain('Wait for its one-time token before leaving.');
    complete({
      token: 'pending-token',
      caller: {
        id: 'new',
        name: 'Pending',
        created: 1,
        revoked: false,
        grants: ['Example'],
        created_by: '',
        external_id: '',
      },
    });
    await waitFor(() => expect(getByLabelText(root, 'Client token')).toBeTruthy());
    expect(document.activeElement).toBe(getByLabelText(root, 'Client token'));
    await click(root, 'Providers');
    expect(confirm).toHaveBeenCalledOnce();
    expect(queryByRole(root, 'textbox', { name: 'Client token' })).toBeNull();
  });
});

describe('route pagination', () => {
  const makeRoutes = (count: number) =>
    Array.from({ length: count }, (_, i) => ({
      ...route,
      name: `route-${String(i + 1).padStart(2, '0')}`,
      provider: `route-${String(i + 1).padStart(2, '0')}`,
      local_prefix: `/r${i + 1}/`,
    }));

  it('hides pagination controls when providers fit on one page', async () => {
    const { root } = setup(state({ routes: makeRoutes(10) }));
    await waitFor(() => expect(root.querySelectorAll('.route-card')).toHaveLength(10));
    expect(root.querySelector('.route-pagination')).toBeNull();
  });

  it('paginates providers at 10 per page with Previous, Next, and ellipsis page buttons', async () => {
    const { root } = setup(state({ routes: makeRoutes(85) }));
    await waitFor(() => expect(root.querySelectorAll('.route-card')).toHaveLength(10));
    const nav = getByRole(root, 'navigation', { name: 'Inference provider pagination' });
    expect(nav).not.toBeNull();
    expect(nav.textContent).toContain('Showing 1–10 of 85 providers');
    expect(nav.querySelectorAll('.route-pagination-ellipsis')).toHaveLength(1);
    expect(getByRole(root, 'button', { name: 'Previous' })).toHaveProperty('disabled', true);

    await click(root, 'Next');
    expect(nav.textContent).toContain('Showing 11–20 of 85 providers');
    expect(root.querySelector('.route-card h3')?.textContent).toBe('route-11');

    await click(root, 'Page 9');
    expect(nav.textContent).toContain('Showing 81–85 of 85 providers');
    expect(root.querySelectorAll('.route-card')).toHaveLength(5);
    expect(getByRole(root, 'button', { name: 'Next' })).toHaveProperty('disabled', true);
  });
});

it('keeps reload off Home while security and operator controls remain accessible', async () => {
  window.history.replaceState(null, '', '/_proxy/');
  const { root } = setup(state({ mode: 'file', routes: [route] }));
  await waitFor(() => expect(getByRole(root, 'heading', { name: 'Cost' })).toBeVisible());
  expect(queryByRole(root, 'button', { name: 'Reload file' })).toBeNull();
  expect(getByRole(root, 'button', { name: 'Sign out' })).toBeTruthy();
  expect(getByRole(root, 'button', { name: 'Secrets' })).toBeTruthy();
  await click(root, 'Providers');
  expect(getByRole(root, 'button', { name: 'Reload file' })).toBeTruthy();
});
