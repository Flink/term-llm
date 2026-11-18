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
  ProxyClient,
  type ProxyAccount,
  type ProxyPreset,
  type ProxyRoute,
  type ProxyState,
} from '../src/api/proxy-client';
import type { PasskeyPlatform } from '../src/hub/platform/passkeys';
import type { UIContext } from './dom';
import { routesView } from './routes';

const presets: ProxyPreset[] = [
  {
    id: 'openai',
    label: 'OpenAI API',
    upstream: 'https://api.openai.com/v1',
    auth: { header: 'Authorization', prefix: 'Bearer ' },
    billing: 'pay_as_you_go',
    usage: 'auto',
    websocket: true,
  },
  {
    id: 'chatgpt',
    label: 'ChatGPT plan',
    upstream: 'https://chatgpt.com/backend-api/codex',
    auth: { header: 'Authorization', prefix: 'Bearer ' },
    billing: 'plan',
    usage: 'auto',
    websocket: true,
    account_kind: 'chatgpt',
  },
  {
    id: 'jina',
    label: 'Jina embeddings',
    upstream: 'https://api.jina.ai/v1',
    auth: { header: 'Authorization', prefix: 'Bearer ' },
    billing: 'pay_as_you_go',
  },
];
const featuredPresets: ProxyPreset[] = [
  ...presets,
  {
    id: 'claude-code',
    label: 'Claude Code plan',
    upstream: 'https://api.anthropic.com',
    auth: { header: 'Authorization', prefix: 'Bearer ' },
    account_kind: 'claude-code',
  },
  {
    id: 'venice',
    label: 'Venice',
    upstream: 'https://api.venice.ai/api/v1',
    auth: { header: 'Authorization', prefix: 'Bearer ' },
  },
];
const accounts: ProxyAccount[] = [
  { name: 'home-chat', kind: 'chatgpt', state: 'ready', generation: 1, updated: 1 },
  { name: 'home-grok', kind: 'grok', state: 'ready', generation: 1, updated: 1 },
];
function setup(overrides: Partial<ProxyState> = {}) {
  const api = new ProxyClient(vi.fn());
  const save = vi.spyOn(api, 'config').mockResolvedValue();
  const announce = vi.fn();
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
    refresh: vi.fn(),
    protectToken: vi.fn(),
    navigate: vi.fn(),
  };
  const platform: PasskeyPlatform = { available: () => true, create: vi.fn(), get: vi.fn() };
  const state: ProxyState = {
    auth: 'token',
    mode: 'ui',
    revision: 7,
    routes: [],
    secrets: [],
    callers: [],
    admin_keys: [],
    csrf: 'csrf-token',
    presets,
    accounts,
    ...overrides,
  };
  const root = routesView(state, api, context, platform);
  document.body.append(root);
  return { root, save, announce };
}
function choose(root: HTMLElement, label: string) {
  fireEvent.click(getByRole(root, 'button', { name: 'Add route' }));
  fireEvent.click(
    getByRole(root.querySelector<HTMLElement>('.route-picker')!, 'button', { name: label }),
  );
}
function openManualChatGPT(root: HTMLElement) {
  choose(root, 'ChatGPT');
  expect(getByRole(root, 'button', { name: 'Sign in to ChatGPT' })).toBeVisible();
  const setup = getByRole(root, 'heading', { name: 'Set up ChatGPT plan' }).closest('section')!;
  setup.querySelector<HTMLDetailsElement>(':scope > details')!.open = true;
  fireEvent.click(getByRole(setup, 'button', { name: 'Use an existing account manually' }));
}
afterEach(() => {
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

it('opens one grouped picker from one Add route button', () => {
  const { root } = setup({
    presets: featuredPresets.map((preset) => ({
      ...preset,
      group: preset.billing === 'plan' || preset.account_kind ? 'plan' : 'api_key',
    })),
  });
  const toolbar = root.querySelector<HTMLElement>('.route-cmd-bar')!;
  expect(toolbar.querySelectorAll('button')).toHaveLength(1);
  expect(root.querySelector('.route-picker')).toBeNull();
  fireEvent.click(getByRole(toolbar, 'button', { name: 'Add route' }));
  const picker = root.querySelector<HTMLElement>('.route-picker')!;
  expect(getByRole(picker, 'heading', { name: 'Add a route' })).toHaveFocus();
  expect(picker.querySelector('details')).toBeNull();
  const groups = [...picker.querySelectorAll('.route-preset-group')].map((group) => [
    group.querySelector('h3')!.textContent,
    [...group.querySelectorAll('button')].map((node) => node.getAttribute('aria-label')),
  ]);
  // Featured plans lead their group; group headings replace the plan/API suffixes.
  expect(groups).toEqual([
    ['Subscriptions', ['Claude Code', 'ChatGPT']],
    ['API keys', ['OpenAI', 'Jina embeddings', 'Venice']],
  ]);
  expect(getByRole(picker, 'button', { name: 'Custom endpoint' })).toBeVisible();
  fireEvent.click(getByRole(picker, 'button', { name: 'OpenAI' }));
  expect(getByLabelText(root, 'Name')).toHaveValue('openai');
  expect(queryByLabelText(root, 'Provider')).toBeNull();
});

it('filters presets by search, hides companions and chooses the first match on Enter', () => {
  const { root } = setup({
    presets: [
      { ...presets[1], group: 'plan', companions: ['chatgpt-backend'] },
      { ...presets[1], id: 'chatgpt-backend', label: 'ChatGPT plan usage', group: 'plan' },
      ...presets.filter((preset) => preset.id !== 'chatgpt'),
    ],
  });
  fireEvent.click(getByRole(root, 'button', { name: 'Add route' }));
  const picker = root.querySelector<HTMLElement>('.route-picker')!;
  expect(queryByRole(picker, 'button', { name: 'ChatGPT plan usage' })).toBeNull();
  const search = getByRole(picker, 'searchbox', { name: 'Search providers' });
  fireEvent.input(search, { target: { value: 'embed' } });
  expect(getByRole(picker, 'button', { name: 'Jina embeddings' })).toBeVisible();
  expect(picker.querySelector('[aria-label="OpenAI"]')).not.toBeVisible();
  fireEvent.input(search, { target: { value: 'nothing-matches' } });
  expect(picker.querySelector('.route-preset-empty')).toBeVisible();
  fireEvent.input(search, { target: { value: 'openai' } });
  fireEvent.keyDown(search, { key: 'Enter' });
  expect(getByLabelText(root, 'Name')).toHaveValue('openai');
});

it('creates companion routes with the shared key unless declined', async () => {
  const gemini: ProxyPreset = {
    id: 'gemini',
    label: 'Google Gemini',
    upstream: 'https://generativelanguage.googleapis.com/v1beta',
    auth: { header: 'x-goog-api-key', prefix: '' },
    billing: 'pay_as_you_go',
    usage: 'auto',
    group: 'api_key',
    companions: ['gemini-live'],
  };
  const live: ProxyPreset = {
    id: 'gemini-live',
    label: 'Gemini Live',
    upstream: 'https://generativelanguage.googleapis.com',
    auth: { header: 'x-goog-api-key', prefix: '' },
    billing: 'pay_as_you_go',
    websocket: true,
    group: 'api_key',
  };
  const secrets = [
    { name: 'gemini-key', origin: 'https://generativelanguage.googleapis.com', source: 'stored' },
  ] as ProxyState['secrets'];
  for (const keep of [true, false]) {
    const { root, save } = setup({ presets: [gemini, live], secrets });
    choose(root, 'Google Gemini');
    const toggle = getByLabelText(root, 'Also create gemini-live for the related client features');
    expect(toggle).toBeChecked();
    if (!keep) fireEvent.click(toggle);
    fireEvent.change(getByLabelText(root, 'Secret name'), { target: { value: 'gemini-key' } });
    fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
    await waitFor(() => expect(save).toHaveBeenCalled());
    const routes = save.mock.calls[0][1];
    expect(routes.map((route) => route.name)).toEqual(
      keep ? ['gemini', 'gemini-live'] : ['gemini'],
    );
    if (keep)
      expect(routes[1]).toMatchObject({
        local_prefix: '/gemini-live/',
        upstream: live.upstream,
        websocket: true,
        set_headers: { 'x-goog-api-key': { secret: 'gemini-key', prefix: '' } },
      });
    document.body.replaceChildren();
    vi.restoreAllMocks();
  }
});

it('keeps Custom endpoint explicit and restores focus on cancel', () => {
  const { root } = setup();
  choose(root, 'Custom endpoint');
  const name = getByLabelText(root, 'Name');
  expect(name).toHaveFocus();
  expect(getByLabelText(root, 'Upstream base URL')).toHaveValue('');
  fireEvent.click(getByRole(root, 'button', { name: 'Cancel' }));
  expect(getByRole(root, 'button', { name: 'Add route' })).toHaveFocus();
});

it.each<ProxyRoute['set_headers']>([
  {},
  { Authorization: { value: 'public' } },
  { Authorization: { secret: 'key' } },
])('derives route status from provider readiness and client access: %j', (setHeaders) => {
  const { root } = setup({
    routes: [
      {
        name: 'local',
        local_prefix: '/local/',
        upstream: 'https://example.com',
        enabled: true,
        allow_insecure_http: false,
        caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
        set_headers: setHeaders,
      },
    ],
  });
  const row = root.querySelector('.route-card')!;
  const needsKey = Object.values(setHeaders).some((rule) => rule.secret);
  expect(row.querySelector('.route-summary')).toHaveTextContent(
    needsKey ? 'Provider key needed' : 'Needs client access',
  );
  expect(row.querySelector('.route-summary')).not.toHaveTextContent('Enabled');
  expect(getByRole(row as HTMLElement, 'heading', { name: 'Provider setup' })).toBeVisible();
  expect(getByRole(row as HTMLElement, 'heading', { name: 'Client access' })).toBeVisible();
});

it('does not show credentials, queries, fragments, or malformed upstream text in a row', () => {
  const base: ProxyRoute = {
    name: 'legacy',
    local_prefix: '/legacy/',
    enabled: true,
    allow_insecure_http: false,
    caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
    set_headers: {},
    upstream:
      'https://sensitive-user:sensitive-password@example.com/api?token=sensitive-query#sensitive-fragment',
  };
  const { root } = setup({
    routes: [
      base,
      { ...base, name: 'broken', local_prefix: '/broken/', upstream: 'sensitive-malformed' },
    ],
  });
  expect(root.querySelector('[data-focus="route:legacy"] .route-row-settings')).toHaveTextContent(
    'https://example.com/api',
  );
  expect(root.querySelector('[data-focus="route:broken"] .route-row-settings')).toHaveTextContent(
    'Invalid upstream',
  );
  expect(root.textContent).not.toContain('sensitive-');
});

it('reports account readiness separately from client grants', () => {
  const { root } = setup({
    routes: [
      {
        name: 'chat',
        local_prefix: '/chat/',
        upstream: presets[1].upstream,
        enabled: true,
        allow_insecure_http: false,
        caller_auth: presets[1].auth,
        set_headers: {},
        account: 'home-chat',
      },
    ],
    callers: [
      {
        id: 'client',
        name: 'Client',
        created: 1,
        revoked: false,
        grants: ['chat'],
        created_by: '',
        external_id: '',
      },
    ],
  });
  expect(root.querySelector('.route-summary')).toHaveTextContent('Configured');
  expect(root.querySelector('.route-connections')).toHaveTextContent('Provider account connected');
  // The account is managed on the route that uses it, not in a page-level list.
  const card = root.querySelector<HTMLElement>('[data-focus="route:chat"]')!;
  expect(card.querySelector('.account-controls summary')).toHaveTextContent(
    'Manage account home-chat',
  );
  expect(root.querySelector('.route-management')).toBeNull();
  // home-grok is not used by any route.
  expect(root.querySelector('.unused-accounts summary')).toHaveTextContent(
    'Manage account home-grok',
  );
});

it.each([undefined, false, true])(
  'uses a single empty-state prompt independent of request history: %s',
  (has_requests) => {
    const { root } = setup({ has_requests });
    expect(getByRole(root, 'heading', { name: 'Start with one route' })).toBeVisible();
    expect(getByRole(root, 'button', { name: 'Add route' })).toBeVisible();
    expect(root.querySelector('.onboarding')).toBeNull();
  },
);

it('keeps file-managed route setup read-only', () => {
  const { root } = setup({ mode: 'file' });
  expect(queryByRole(root, 'button', { name: 'Add route' })).toBeNull();
  expect(root).toHaveTextContent('Routes are file-managed and read-only.');
});

it('creates an account-kind route from an explicitly chosen same-kind account, with no static secret', async () => {
  const { root, save } = setup();
  openManualChatGPT(root);
  const form = root.querySelector<HTMLFormElement>('.route-form')!;
  expect(form.querySelector('.header-substitution')).toBeNull();
  expect(form.textContent).not.toContain('Upstream headers');
  expect(queryByRole(form, 'button', { name: 'Add substitution', hidden: true })).toBeNull();
  const account = getByLabelText(root, 'Trusted account') as HTMLSelectElement;
  expect([...account.options].map((o) => o.value)).toEqual(['', 'home-chat']);
  expect(account).toHaveValue('');
  expect(account.required).toBe(true);
  expect(getByLabelText(root, 'Billing')).toHaveValue('plan');
  fireEvent.change(account, { target: { value: 'home-chat' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(save.mock.calls[0][1]).toEqual([
    {
      name: 'chatgpt',
      local_prefix: '/chatgpt/',
      upstream: 'https://chatgpt.com/backend-api/codex',
      enabled: true,
      allow_insecure_http: false,
      caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
      set_headers: {},
      billing: 'plan',
      account: 'home-chat',
      websocket: true,
      usage: 'auto',
    },
  ]);
});

it('refuses to save an account-kind route without a valid same-kind account', async () => {
  const { root, save, announce } = setup();
  openManualChatGPT(root);
  const form = root.querySelector<HTMLFormElement>('.route-form')!;
  // fireEvent.submit bypasses native "required" validation, as a scripted submit would.
  fireEvent.submit(form);
  await waitFor(() => expect(announce).toHaveBeenCalledWith(expect.stringContaining('chatgpt')));
  // A forged option of another kind is rejected too.
  const account = getByLabelText(root, 'Trusted account') as HTMLSelectElement;
  const forged = document.createElement('option');
  forged.value = 'home-grok';
  account.append(forged);
  fireEvent.change(account, { target: { value: 'home-grok' } });
  fireEvent.submit(form);
  await waitFor(() => expect(announce).toHaveBeenCalledTimes(2));
  expect(save).not.toHaveBeenCalled();
});

it('connects a missing account inline without losing the route draft', async () => {
  const { root } = setup({ accounts: [accounts[1]] });
  openManualChatGPT(root);
  fireEvent.input(getByLabelText(root, 'Name'), { target: { value: 'draft-name' } });
  const select = getByLabelText(root, 'Trusted account') as HTMLSelectElement;
  expect(select.options).toHaveLength(1);
  expect(root.querySelector('.route-form')?.textContent).toContain(
    'Connect a chatgpt account to continue.',
  );
  HTMLDialogElement.prototype.showModal ??= function (this: HTMLDialogElement) {
    this.open = true;
  };
  HTMLDialogElement.prototype.close ??= function (this: HTMLDialogElement) {
    this.open = false;
    this.dispatchEvent(new Event('close'));
  };
  fireEvent.click(getByRole(root, 'button', { name: 'Connect new account' }));
  const dialog = root.querySelector<HTMLDialogElement>('dialog')!;
  expect(getByLabelText(dialog, 'Account kind')).toHaveValue('chatgpt');
  expect(getByLabelText(dialog, 'Account kind')).toBeDisabled();
  expect(getByLabelText(dialog, 'Account name')).toHaveValue('chatgpt');
  const stored: ProxyAccount = {
    name: 'chatgpt',
    kind: 'chatgpt',
    state: 'ready',
    generation: 1,
    updated: 1,
  };
  const api = ProxyClient.prototype;
  const save = vi.spyOn(api, 'saveAccount').mockResolvedValue({ account: stored });
  fireEvent.input(getByLabelText(dialog, 'Paste credential JSON (write-only)'), {
    target: { value: '{"access_token":"a","refresh_token":"r","expires_at":4102444800}' },
  });
  fireEvent.submit(dialog.querySelector('form')!);
  await waitFor(() => expect(save).toHaveBeenCalled());
  await waitFor(() => expect(select).toHaveValue('chatgpt'));
  expect(root.querySelector('dialog')).toBeNull();
  expect(getByLabelText(root, 'Name')).toHaveValue('draft-name');
});

it('applies profile billing, usage and WebSocket defaults to API-key routes only when present', async () => {
  const { root, save } = setup({
    secrets: [
      { name: 'openai-key', origin: 'https://api.openai.com', source: 'stored' },
      { name: 'jina-key', origin: 'https://api.jina.ai', source: 'stored' },
    ],
  });
  choose(root, 'OpenAI');
  expect(queryByLabelText(root, 'Trusted account')).toBeNull();
  expect(getByLabelText(root, 'Forward WebSocket upgrades')).toBeChecked();
  expect(getByLabelText(root, 'Usage measurement')).toHaveValue('auto');
  fireEvent.change(getByLabelText(root, 'Secret name'), { target: { value: 'openai-key' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(save.mock.calls[0][1][0]).toMatchObject({
    billing: 'pay_as_you_go',
    websocket: true,
    usage: 'auto',
    set_headers: { Authorization: { secret: 'openai-key', prefix: 'Bearer ' } },
  });
  expect(save.mock.calls[0][1][0]).not.toHaveProperty('account');

  fireEvent.click(getByRole(root, 'button', { name: 'Cancel' }));
  choose(root, 'Jina embeddings');
  expect(getByLabelText(root, 'Forward WebSocket upgrades')).not.toBeChecked();
  expect(getByLabelText(root, 'Usage measurement')).toHaveValue('off');
  fireEvent.change(getByLabelText(root, 'Secret name'), { target: { value: 'jina-key' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
  const jina = save.mock.calls[1][1][0];
  expect(jina.websocket).toBeUndefined();
  expect(jina.usage).toBeUndefined();
});

it('preserves an existing route’s account, transport and admission policy when unrelated fields change', async () => {
  const existing: ProxyRoute = {
    name: 'plan',
    local_prefix: '/plan/',
    upstream: 'https://chatgpt.com/backend-api/codex',
    enabled: true,
    allow_insecure_http: false,
    caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
    set_headers: {},
    billing: 'plan',
    account: 'retired',
    websocket: true,
    usage: 'off',
    allow: ['POST /responses'],
    redirects: 'refuse',
  };
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
  // The saved account no longer exists: it must stay selectable and unchanged.
  const { root, save } = setup({ routes: [existing] });
  fireEvent.click(getByRole(root, 'button', { name: 'Edit plan' }));
  expect(getByLabelText(root, 'Trusted account')).toHaveValue('retired');
  expect(root.querySelector('.route-form')?.textContent).not.toContain('Upstream headers');
  expect(getByLabelText(root, 'Forward WebSocket upgrades')).toBeChecked();
  expect(getByLabelText(root, 'Usage measurement')).toHaveValue('off');
  fireEvent.change(getByLabelText(root, 'Billing'), {
    target: { value: 'unspecified' },
  });
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(save.mock.calls[0][1]).toEqual([{ ...existing, billing: 'unspecified' }]);
  expect(confirm).not.toHaveBeenCalled();
});

it('asks before a transport change that drops grants on an existing route', async () => {
  const existing: ProxyRoute = {
    name: 'api',
    local_prefix: '/api/',
    upstream: 'https://api.openai.com/v1',
    enabled: true,
    allow_insecure_http: false,
    caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
    set_headers: {},
  };
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
  const { root, save } = setup({ routes: [existing] });
  fireEvent.click(getByRole(root, 'button', { name: 'Edit api' }));
  expect(queryByLabelText(root, 'Trusted account')).toBeNull();
  fireEvent.click(getByLabelText(root, 'Forward WebSocket upgrades'));
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(confirm).toHaveBeenCalledOnce());
  expect(save).not.toHaveBeenCalled();
});

it('keeps billing choices concise and technical guidance and preview in closed Advanced', () => {
  const note = 'Use this route as the client base URL.';
  const { root } = setup({ presets: [{ ...presets[0], note }] });
  choose(root, 'OpenAI');
  const form = root.querySelector<HTMLFormElement>('.route-form')!;
  const billing = getByLabelText(form, 'Billing') as HTMLSelectElement;
  expect([...billing.options].map((option) => option.text)).toEqual([
    'Not specified',
    'Pay as you go',
    'Subscription',
    'Local',
  ]);
  const advanced = getByLabelText(form, 'Usage measurement').closest('details')!;
  expect(advanced.querySelector('summary')).toHaveTextContent(/^Advanced$/);
  expect(advanced.open).toBe(false);
  expect(advanced.textContent).toContain('Names are permanent');
  expect(advanced.textContent).toContain('does not change provider charges');
  expect(advanced.textContent).toContain(note);
  expect(advanced.contains(form.querySelector('.preview'))).toBe(true);
  expect(form.querySelector('.route-form-grid small')).toBeNull();
  // API-key routes still expose credential setup without opening Advanced.
  expect(getByRole(form, 'button', { name: 'Add API key' }).closest('details')).toBeNull();
  advanced.open = true;
  expect(getByRole(form, 'heading', { name: 'Preview' })).toBeVisible();
  fireEvent.input(getByLabelText(form, 'Name'), { target: { value: 'my-api' } });
  expect(form.querySelector('.preview')).toHaveTextContent('/my-api/');
});

it.each<ProxyRoute['set_headers']>([
  { 'X-Project': { value: 'project-name' } },
  { 'X-Api-Key': { secret: 'chat-key', prefix: 'Bearer ' } },
])('preserves and edits existing account-backed header substitutions: %j', async (setHeaders) => {
  const existing: ProxyRoute = {
    name: 'chat',
    local_prefix: '/chat/',
    upstream: presets[1].upstream,
    caller_auth: presets[1].auth,
    set_headers: setHeaders,
    enabled: true,
    allow_insecure_http: false,
    billing: 'plan',
    account: 'home-chat',
  };
  const { root, save } = setup({
    routes: [existing],
    secrets: [{ name: 'chat-key', origin: 'https://chatgpt.com', source: 'stored' }],
  });
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
  fireEvent.click(getByRole(root, 'button', { name: 'Edit chat' }));
  const form = root.querySelector<HTMLFormElement>('.route-form')!;
  const advanced = form.querySelector('details')!;
  expect(advanced.open).toBe(false);
  expect(advanced.querySelector('.header-substitution')).not.toBeNull();
  expect(form.textContent).toContain('changes can remove client grants');
  fireEvent.click(getByRole(form, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(save.mock.calls[0][1]).toEqual([existing]);
  expect(confirm).not.toHaveBeenCalled();

  advanced.open = true;
  expect(getByRole(form, 'button', { name: 'Add substitution' })).toBeVisible();
  fireEvent.click(getByRole(form, 'button', { name: 'Remove substitution' }));
  expect(queryByRole(form, 'heading', { name: 'Upstream headers' })).toBeNull();
  expect(queryByRole(form, 'button', { name: 'Add substitution' })).toBeNull();
  fireEvent.click(getByRole(form, 'button', { name: 'Save route' }));
  await waitFor(() => expect(confirm).toHaveBeenCalledOnce());
  expect(confirm).toHaveBeenCalledWith(expect.stringContaining('grants will be dropped'));
  expect(save).toHaveBeenCalledOnce();
  confirm.mockReturnValue(true);
  fireEvent.click(getByRole(form, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
  expect(save.mock.calls[1][1][0].set_headers).toEqual({});
});

it('retains visible credential warnings for literal headers and insecure upstreams', () => {
  const { root } = setup();
  choose(root, 'OpenAI');
  const form = root.querySelector<HTMLFormElement>('.route-form')!;
  form.querySelector('details')!.open = true;
  fireEvent.change(getByLabelText(form, 'Source'), { target: { value: 'value' } });
  const literal = getByLabelText(form, 'Literal value');
  expect(literal).toHaveAccessibleDescription(
    'Public in state and YAML. Never enter credentials here.',
  );
  expect(form.querySelector('.warning')).toBeVisible();
  expect(form.querySelector('.warning')).toHaveTextContent(
    'Use a stored secret; literals are public.',
  );
  fireEvent.input(getByLabelText(form, 'Upstream base URL'), {
    target: { value: 'http://example.com' },
  });
  expect(getByLabelText(form, /Allow insecure HTTP upstream/)).toBeVisible();
  expect(form.querySelector('.insecure-option')).toHaveTextContent(
    'credentials may travel unencrypted',
  );
});

it.each([
  { auth_mode: 'token_only' as const, state: 'ready' },
  { auth_mode: 'rotating' as const, state: 'ready' },
  { auth_mode: 'vault' as const, state: 'native_login' },
  { auth_mode: 'vault' as const, state: 'ready' },
])(
  'keeps Claude device sign-in distinct from stored account readiness: $auth_mode/$state',
  (metadata) => {
    const { root } = setup({
      presets: featuredPresets,
      accounts: [{ name: 'claude', kind: 'claude-code', generation: 1, updated: 1, ...metadata }],
      routes: [
        {
          name: 'claude',
          local_prefix: '/claude/',
          upstream: 'https://api.anthropic.com',
          enabled: true,
          allow_insecure_http: false,
          caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
          set_headers: {},
          account: 'claude',
        },
      ],
      callers: [
        {
          id: 'client',
          name: 'Client',
          created: 1,
          revoked: false,
          grants: ['claude'],
          created_by: '',
          external_id: '',
        },
      ],
    });
    expect(root.querySelector('.route-summary')).toHaveTextContent('Native sign-in on each device');
    expect(root.querySelector('.route-summary')).not.toHaveTextContent('ready');
    choose(root, 'Claude Code');
    expect(getByRole(root, 'button', { name: 'Create route & get launcher' })).toBeVisible();
    expect(queryByLabelText(root, 'Trusted account')).toBeNull();
  },
);
