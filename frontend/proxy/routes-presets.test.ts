import { withSingletonProviders } from './test-fixtures';
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
  type ProxyAdminRoute,
  type ProxyState,
} from '../src/api/proxy-client';
import type { UIContext } from './dom';
import { routesView } from './routes';

const presets: ProxyPreset[] = [
  {
    family: 'inference',
    id: 'openai',
    label: 'OpenAI',
    upstream: 'https://api.openai.com/v1',
    auth: { header: 'Authorization', prefix: 'Bearer ' },
    billing: 'pay_as_you_go',
    usage: 'auto',
    websocket: true,
  },
  {
    family: 'inference',
    id: 'chatgpt',
    label: 'ChatGPT',
    upstream: 'https://chatgpt.com/backend-api/codex',
    auth: { header: 'Authorization', prefix: 'Bearer ' },
    billing: 'plan',
    usage: 'auto',
    websocket: true,
    account_kind: 'chatgpt',
  },
  {
    family: 'embeddings',
    id: 'jina',
    label: 'Jina',

    upstream: 'https://api.jina.ai/v1',
    auth: { header: 'Authorization', prefix: 'Bearer ' },
    billing: 'pay_as_you_go',
  },
];
const featuredPresets: ProxyPreset[] = [
  ...presets,
  {
    family: 'inference',
    id: 'claude-bin',
    label: 'Claude Code',
    upstream: 'https://api.anthropic.com',
    auth: { header: 'Authorization', prefix: 'Bearer ' },
    account_kind: 'claude-bin',
    billing: 'plan',
  },
  {
    family: 'inference',
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
  const create = vi.spyOn(api, 'createProvider').mockResolvedValue({ revision: 8 });
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
  const state: ProxyState = {
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
    presets,
    accounts,
    ...overrides,
  };
  const root = routesView(withSingletonProviders(state), api, context);
  document.body.append(root);
  return { root, save, create, announce };
}
function choose(root: HTMLElement, label: string) {
  fireEvent.click(getByRole(root, 'button', { name: 'Add provider' }));
  fireEvent.click(
    getByRole(root.querySelector<HTMLElement>('.route-picker')!, 'button', { name: label }),
  );
}
function chooseAdvancedSetup(root: HTMLElement, label: string) {
  choose(root, label);
  expect(getByRole(root, 'button', { name: `Sign in to ${label}` })).toBeVisible();
  const setup = getByRole(root, 'heading', { name: `Set up ${label}` }).closest('section')!;
  const advanced = setup.querySelector<HTMLDetailsElement>(':scope > details')!;
  expect(advanced.querySelector('summary')).toHaveTextContent('Advanced');
  expect(advanced.open).toBe(false);
  advanced.open = true;
  fireEvent.click(getByRole(advanced, 'button', { name: /use a saved account/i }));
}
function openManage(root: HTMLElement, routeName: string) {
  const line = root.querySelector<HTMLElement>(`[data-focus="route:${routeName}"]`);
  if (!line) throw new Error(`Missing route line: ${routeName}`);
  const details = line
    .closest('article.route-card')
    ?.querySelector<HTMLDetailsElement>('details.provider-manage');
  if (!details) throw new Error(`Missing provider Manage: ${routeName}`);
  details.open = true;
  return details;
}
function settingsFor(root: HTMLElement, routeName: string): HTMLElement {
  const line = root.querySelector<HTMLElement>(`[data-focus="route:${routeName}"]`);
  if (!line) throw new Error(`Missing route line: ${routeName}`);
  const settings = line
    .closest('article.route-card')
    ?.querySelector<HTMLElement>('.route-row-settings');
  if (!settings) throw new Error(`Missing route settings: ${routeName}`);
  return settings;
}
afterEach(() => {
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

it('opens one grouped picker from one Add provider button', () => {
  const { root } = setup({
    presets: [
      ...featuredPresets.map((preset): ProxyPreset => ({
        ...preset,
      })),
      {
        family: 'search',
        id: 'brave',
        label: 'Brave Search',
        upstream: 'https://api.search.brave.com/res/v1',
        auth: { header: 'X-Subscription-Token', prefix: '' },
      },
    ],
  });
  const intro = root.querySelector<HTMLElement>('.providers-intro')!;
  expect(getByRole(intro, 'heading', { name: 'Providers' })).toBeVisible();
  expect(intro.querySelectorAll('button')).toHaveLength(1);
  expect(root.querySelector('.route-picker')).toBeNull();
  fireEvent.click(getByRole(intro, 'button', { name: 'Add provider' }));
  const picker = root.querySelector<HTMLElement>('.route-picker')!;
  expect(getByRole(picker, 'heading', { name: 'Add a provider' })).toHaveFocus();
  expect(picker.querySelector('details')).toBeNull();
  expect([...picker.querySelectorAll('.route-preset-family h3')].map((h) => h.textContent)).toEqual(
    ['Inference', 'Search', 'Embeddings'],
  );
  expect(
    [...picker.querySelectorAll('.route-preset-family[aria-label="Inference"] h4')].map(
      (h) => h.textContent,
    ),
  ).toEqual(['Subscriptions', 'API keys']);
  expect(picker.querySelector('.route-preset-family[aria-label="Search"] h4')).toBeNull();
  expect(getByRole(picker, 'button', { name: 'Custom endpoint' })).toBeVisible();
  fireEvent.click(getByRole(picker, 'button', { name: 'OpenAI' }));
  expect(getByLabelText(root, 'Provider name')).toHaveValue('openai');
  expect(queryByLabelText(root, 'Provider')).toBeNull();
});

it.each([
  { preset: presets[0], label: 'Example API' },
  { preset: featuredPresets.find((preset) => preset.id === 'claude-bin')!, label: 'Example plan' },
])('uses the exact preset label in the picker and setup heading ($label)', ({ preset, label }) => {
  const { root } = setup({ presets: [{ ...preset, label }] });
  choose(root, label);
  expect(getByRole(root, 'heading', { name: `Set up ${label}` })).toBeVisible();
});

it('distinguishes subscription providers from their API key counterparts', () => {
  const claude = featuredPresets.find((preset) => preset.id === 'claude-bin')!;
  const { root } = setup({
    presets: [
      claude,
      {
        ...claude,
        id: 'anthropic',
        label: 'Anthropic',
        billing: 'pay_as_you_go',
        account_kind: undefined,
      },
      planCases[1].preset,
      { ...presets[0], id: 'xai', label: 'xAI', upstream: 'https://api.x.ai/v1' },
    ],
  });
  fireEvent.click(getByRole(root, 'button', { name: 'Add provider' }));
  const picker = root.querySelector<HTMLElement>('.route-picker')!;
  for (const [label, group] of [
    ['Claude Code', 'Subscriptions'],
    ['Grok', 'Subscriptions'],
    ['Anthropic', 'API keys'],
    ['xAI', 'API keys'],
  ]) {
    const choice = getByRole(picker, 'button', { name: label });
    expect(choice.closest('.route-preset-group')?.querySelector('h4')).toHaveTextContent(group);
  }
});

it('filters presets by search, hides companions and chooses the first match on Enter', () => {
  const { root } = setup({
    presets: [
      { ...presets[1], companions: ['chatgpt-backend'] },
      { ...presets[1], id: 'chatgpt-backend', label: 'ChatGPT Usage & Dictation' },
      ...presets.filter((preset) => preset.id !== 'chatgpt'),
    ],
  });
  fireEvent.click(getByRole(root, 'button', { name: 'Add provider' }));
  const picker = root.querySelector<HTMLElement>('.route-picker')!;
  expect(queryByRole(picker, 'button', { name: 'ChatGPT Usage & Dictation' })).toBeNull();
  const search = getByRole(picker, 'searchbox', { name: 'Search providers' });
  fireEvent.input(search, { target: { value: 'embed' } });
  expect(getByRole(picker, 'button', { name: 'Jina' })).toBeVisible();
  expect(picker.querySelector('[aria-label="OpenAI"]')).not.toBeVisible();
  fireEvent.input(search, { target: { value: 'nothing-matches' } });
  expect(picker.querySelector('.route-preset-empty')).toBeVisible();
  fireEvent.input(search, { target: { value: 'openai' } });
  fireEvent.keyDown(search, { key: 'Enter' });
  expect(getByLabelText(root, 'Provider name')).toHaveValue('openai');
});

it('locks the Exa name and submits one key for both route profiles', async () => {
  const exa: ProxyPreset = {
    ...presets[0],
    family: 'search',
    id: 'exa',
    label: 'Exa',
    companions: ['exa-mcp'],
  };
  const { root, create } = setup({
    presets: [exa, { ...exa, id: 'exa-mcp', label: 'Exa MCP', companions: [] }],
  });
  choose(root, 'Exa');
  expect(getByLabelText(root, 'Provider name')).toHaveValue('exa');
  expect(getByLabelText(root, 'Provider name')).toHaveAttribute('readonly');
  expect(root.querySelectorAll('input[type=password]')).toHaveLength(1);
  fireEvent.input(getByLabelText(root, 'API key'), { target: { value: 'synthetic-key' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Create provider' }));
  await waitFor(() =>
    expect(create).toHaveBeenCalledWith(7, {
      name: 'exa',
      label: 'Exa',
      preset: 'exa',
      value: 'synthetic-key',
    }),
  );
  expect(getByLabelText(root, 'API key')).toHaveValue('');
});

it('keeps Custom endpoint explicit and restores focus on cancel', () => {
  const { root } = setup();
  choose(root, 'Custom endpoint');
  const name = getByLabelText(root, 'Name');
  expect(name).toHaveFocus();
  expect(getByLabelText(root, 'Upstream base URL')).toHaveValue('');
  fireEvent.click(getByRole(root, 'button', { name: 'Cancel' }));
  expect(getByRole(root, 'button', { name: 'Add provider' })).toHaveFocus();
});

it.each<ProxyAdminRoute['set_headers']>([
  {},
  { Authorization: { value: 'public' } },
  { Authorization: { secret: 'key' } },
])('keeps server status separate from credential details: %j', (setHeaders) => {
  const { root } = setup({
    routes: [
      {
        serves: [],
        profile: '',
        provider: 'local',
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
  const row = root.querySelector<HTMLElement>('.route-card')!;
  expect(getByRole(row, 'img', { name: 'Status: green' })).toBeVisible();
  // Client access is a grant, not a provider-health warning on the route line.
  expect(root).not.toHaveTextContent('Needs client access');
  const manage = row.querySelector<HTMLDetailsElement>('details.provider-manage')!;
  expect(manage.open).toBe(false);
  openManage(root, 'local');
  expect(manage.open).toBe(true);
  expect(manage.querySelectorAll('details')).toHaveLength(0);
  expect(row.querySelector('.provider-route-header')).toHaveTextContent('/local/');
  expect(getByRole(row, 'heading', { name: 'Client approval' })).toBeVisible();
  expect(row.querySelector('.route-access-empty')).toHaveTextContent('No clients approved yet.');
});

it('does not show credentials, queries, fragments, or malformed upstream text in a row', () => {
  const base: ProxyAdminRoute = {
    serves: [],
    profile: '',
    provider: 'legacy',
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
      {
        ...base,
        provider: 'broken',
        profile: '',
        serves: [],
        name: 'broken',
        local_prefix: '/broken/',
        upstream: 'sensitive-malformed',
      },
    ],
  });
  expect(settingsFor(root, 'legacy')).toHaveTextContent('https://example.com/api');
  expect(settingsFor(root, 'broken')).toHaveTextContent('Invalid upstream');
  expect(root.textContent).not.toContain('sensitive-');
});

it('reports account readiness separately from provider approval', () => {
  const { root } = setup({
    routes: [
      {
        serves: [],
        profile: '',
        provider: 'chat',
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
  expect(getByRole(root, 'img', { name: 'Status: green' })).toBeVisible();
  // The provider card summarizes approval once, not on each route line.
  const summary = root.querySelector<HTMLElement>('.provider-approval-summary')!;
  expect(summary).toHaveTextContent('Approved: Client');
  const line = root.querySelector<HTMLElement>('[data-focus="route:chat"]')!;
  expect(line).not.toHaveTextContent('Client');
  // The account is managed on the route that uses it, not in a page-level list.
  const card = root.querySelector<HTMLElement>('.route-card')!;
  const manage = card.querySelector<HTMLDetailsElement>('details.provider-manage')!;
  expect(manage.open).toBe(false);
  openManage(root, 'chat');
  expect(manage.open).toBe(true);
  expect(manage.querySelectorAll('details')).toHaveLength(0);
  expect(card.querySelector('.route-access-client')).toHaveTextContent('Client');
  expect(card.querySelector('.account-controls-status')).toHaveTextContent('home-chat');
  expect(card.querySelector('.account-controls-status')).toHaveTextContent('ready');
  expect(root.querySelector('.route-management')).toBeNull();
  // home-grok is not used by any route.
  expect(root.querySelector('.unused-accounts .account-controls-status')).toHaveTextContent(
    'home-grok',
  );
});

it.each([undefined, false, true])(
  'uses a single empty-state prompt independent of request history: %s',
  (has_requests) => {
    const { root } = setup({ has_requests });
    expect(getByRole(root, 'heading', { name: 'Add your first provider' })).toBeVisible();
    expect(getByRole(root, 'button', { name: 'Add provider' })).toBeVisible();
    expect(root.querySelector('.onboarding')).toBeNull();
  },
);

it('keeps file-managed route setup read-only', () => {
  const { root } = setup({ mode: 'file' });
  expect(queryByRole(root, 'button', { name: 'Add provider' })).toBeNull();
  expect(root).toHaveTextContent('Routes are file-managed and read-only.');
});

const planCases = [
  { preset: presets[1], label: 'ChatGPT', saved: 'home-chat' },
  {
    preset: {
      ...presets[1],
      id: 'grok',
      label: 'Grok',
      account_kind: 'grok',
      upstream: 'https://cli-chat-proxy.grok.com/v1',
    } satisfies ProxyPreset,
    label: 'Grok',
    saved: 'home-grok',
  },
];

it.each(planCases)(
  'offers sign-in, not a credential form, without a saved $label account',
  ({ preset, label }) => {
    const { root } = setup({
      presets: [preset],
      accounts: accounts.filter((a) => a.kind !== preset.account_kind),
    });
    choose(root, label);
    const panel = getByRole(root, 'heading', { name: `Set up ${label}` }).closest('section')!;
    expect(getByRole(panel, 'button', { name: `Sign in to ${label}` })).toBeVisible();
    if (label === 'Grok') expect(panel).not.toHaveTextContent('Advanced');
    expect(queryByLabelText(panel, 'Trusted account')).toBeNull();
    expect(panel.querySelector('input[type=password]')).toBeNull();
  },
);

it.each(planCases)(
  'links a saved $label account without asking for credentials',
  async ({ preset, label, saved }) => {
    const { root, create } = setup({ presets: [preset] });
    chooseAdvancedSetup(root, label);
    expect(getByRole(root, 'heading', { name: `Set up ${label}` })).toBeVisible();
    expect(getByLabelText(root, 'Label', { exact: true })).toHaveValue(label);
    if (label === 'Grok')
      expect(queryByRole(root, 'button', { name: 'Import account credentials' })).toBeNull();
    expect(queryByRole(root, 'button', { name: 'Connect new account' })).toBeNull();
    expect((getByLabelText(root, 'Trusted account') as HTMLSelectElement).options).toHaveLength(2);
    expect(queryByLabelText(root, 'API key')).toBeNull();
    fireEvent.change(getByLabelText(root, 'Trusted account'), { target: { value: saved } });
    fireEvent.click(getByRole(root, 'button', { name: 'Create provider' }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(7, {
        name: preset.id,
        label: preset.label,
        preset: preset.id,
        routes: [expect.objectContaining({ name: preset.id, profile: preset.id, account: saved })],
      }),
    );
  },
);

it.each(['copilot', 'chatgpt'] as const)(
  'imports the first %s account without losing the provider draft',
  async (kind) => {
    const copilot: ProxyPreset = {
      ...presets[1],
      id: 'copilot',
      label: 'GitHub Copilot',
      account_kind: 'copilot',
      upstream: 'https://api.githubcopilot.com',
    };
    const preset = kind === 'chatgpt' ? presets[1] : copilot;
    const { root, create } = setup({ presets: [preset], accounts: [] });
    if (kind === 'chatgpt') chooseAdvancedSetup(root, 'ChatGPT');
    else choose(root, 'GitHub Copilot');
    fireEvent.input(getByLabelText(root, 'Provider name'), { target: { value: 'draft-name' } });
    const select = getByLabelText(root, 'Trusted account') as HTMLSelectElement;
    expect(select.options).toHaveLength(1);
    HTMLDialogElement.prototype.showModal ??= function (this: HTMLDialogElement) {
      this.open = true;
    };
    HTMLDialogElement.prototype.close ??= function (this: HTMLDialogElement) {
      this.open = false;
      this.dispatchEvent(new Event('close'));
    };
    fireEvent.click(getByRole(root, 'button', { name: 'Import account credentials' }));
    const dialog = root.querySelector<HTMLDialogElement>('dialog')!;
    expect(getByLabelText(dialog, 'Account kind')).toHaveValue(kind);
    expect(getByLabelText(dialog, 'Account kind')).toBeDisabled();
    expect(getByLabelText(dialog, 'Account name')).toHaveValue(kind);
    const stored: ProxyAccount = {
      name: kind,
      kind,
      state: 'ready',
      generation: 1,
      updated: 1,
    };
    const api = ProxyClient.prototype;
    const save = vi.spyOn(api, 'saveAccount').mockResolvedValue({ account: stored });
    fireEvent.input(getByLabelText(dialog, 'Paste credential JSON (write-only)'), {
      target: {
        value: kind === 'copilot' ? '{"github_token":"fixture"}' : '{"access_token":"fixture"}',
      },
    });
    fireEvent.submit(dialog.querySelector('form')!);
    await waitFor(() => expect(save).toHaveBeenCalled());
    await waitFor(() => expect(select).toHaveValue(kind));
    expect(root.querySelector('dialog')).toBeNull();
    fireEvent.click(getByRole(root, 'button', { name: 'Create provider' }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        7,
        expect.objectContaining({
          name: 'draft-name',
          routes: [expect.objectContaining({ profile: preset.id, account: kind })],
        }),
      ),
    );
  },
);

it('infers Grok for a missing account on a legacy grok-bin route without a preset', () => {
  const { root } = setup({
    accounts: [],
    routes: [
      {
        provider: 'legacy',
        name: 'cli',
        profile: 'grok-bin',
        serves: [],
        account: 'missing',
        local_prefix: '/cli/',
        upstream: planCases[1].preset.upstream,
        enabled: true,
        allow_insecure_http: false,
        caller_auth: presets[1].auth,
        set_headers: {},
      },
    ],
  });
  openManage(root, 'cli');
  fireEvent.click(getByRole(root, 'button', { name: 'Connect account missing' }));
  expect(getByLabelText(root, 'Account kind')).toHaveValue('grok');
  expect(getByLabelText(root, 'Account kind')).toBeDisabled();
});

it('preserves an existing route’s account, transport and admission policy when unrelated fields change', async () => {
  const existing: ProxyAdminRoute = {
    serves: [],
    profile: '',
    provider: 'plan',
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
  openManage(root, 'plan');
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

it('saves a transport change on an existing route without a grant-loss prompt', async () => {
  const existing: ProxyAdminRoute = {
    serves: [],
    profile: '',
    provider: 'api',
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
  openManage(root, 'api');
  fireEvent.click(getByRole(root, 'button', { name: 'Edit api' }));
  expect(queryByLabelText(root, 'Trusted account')).toBeNull();
  fireEvent.click(getByLabelText(root, 'Forward WebSocket upgrades'));
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(confirm).not.toHaveBeenCalled();
});

it('keeps billing choices concise and technical guidance and preview in closed Advanced', () => {
  const { root } = setup();
  choose(root, 'Custom endpoint');
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
  expect(advanced.contains(form.querySelector('.preview'))).toBe(true);
  expect(form.querySelector('.route-form-grid small')).toBeNull();
  advanced.open = true;
  expect(getByRole(form, 'heading', { name: 'Preview' })).toBeVisible();
  fireEvent.input(getByLabelText(form, 'Name'), { target: { value: 'my-api' } });
  expect(form.querySelector('.preview')).toHaveTextContent('/my-api/');
});

it.each<ProxyAdminRoute['set_headers']>([
  { 'X-Project': { value: 'project-name' } },
  { 'X-Api-Key': { secret: 'chat-key', prefix: 'Bearer ' } },
])('preserves and edits existing account-backed header substitutions: %j', async (setHeaders) => {
  const existing: ProxyAdminRoute = {
    serves: [],
    profile: '',
    provider: 'chat',
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
    secrets: [{ name: 'chat-key', routes: null, source: 'stored' }],
  });
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
  openManage(root, 'chat');
  fireEvent.click(getByRole(root, 'button', { name: 'Edit chat' }));
  const form = root.querySelector<HTMLFormElement>('.route-form')!;
  expect(form.querySelectorAll('details')).toHaveLength(0);
  const advanced = form.querySelector<HTMLElement>('.route-form-advanced')!;
  expect(advanced.querySelector('.header-substitution')).not.toBeNull();
  expect(form.textContent).not.toContain('remove client grants');
  fireEvent.click(getByRole(form, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(save.mock.calls[0][1]).toEqual([existing]);
  expect(confirm).not.toHaveBeenCalled();

  expect(getByRole(form, 'button', { name: 'Add substitution' })).toBeVisible();
  fireEvent.click(getByRole(form, 'button', { name: 'Remove substitution' }));
  expect(queryByRole(form, 'heading', { name: 'Upstream headers' })).toBeNull();
  expect(queryByRole(form, 'button', { name: 'Add substitution' })).toBeNull();
  fireEvent.click(getByRole(form, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
  // Credential edits keep client grants, so saving needs no confirmation.
  expect(confirm).not.toHaveBeenCalled();
  expect(save.mock.calls[1][1][0].set_headers).toEqual({});
});

it('retains visible credential warnings for literal headers and insecure upstreams', () => {
  const { root } = setup();
  choose(root, 'Custom endpoint');
  fireEvent.click(getByRole(root, 'button', { name: 'Add substitution' }));
  fireEvent.input(getByLabelText(root, 'Header name'), { target: { value: 'Authorization' } });
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

it('keeps pending shared Claude out of the generic credentials form', () => {
  const { root } = setup({
    presets: featuredPresets,
    providers: [
      {
        id: 'claude',
        label: 'Claude Code',
        family: 'inference',
        status: 'red',
        reason: 'Sign-in needed',
        routes: ['claude'],
      },
    ],
    accounts: [
      {
        name: 'claude',
        kind: 'claude-bin',
        generation: 1,
        updated: 1,
        auth_mode: 'shared_vault',
        state: 'needs_login',
      },
    ],
    routes: [
      {
        serves: [],
        profile: '',
        provider: 'claude',
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
  expect(root.querySelector('.provider-status')).toHaveTextContent('Sign-in needed');
  expect(root.querySelector('.provider-status')).not.toHaveTextContent('ready');
  choose(root, 'Claude Code');
  expect(getByRole(root, 'button', { name: 'Create shared route & sign in' })).toBeVisible();
  expect(queryByLabelText(root, 'Trusted account')).toBeNull();
});
