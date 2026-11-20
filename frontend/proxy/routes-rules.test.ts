import { withSingletonProviders } from './test-fixtures';
import { afterEach, expect, it, vi } from 'vitest';
import {
  fireEvent,
  getByLabelText,
  getByRole,
  queryByLabelText,
  waitFor,
  within,
} from '@testing-library/preact';
import {
  ProxyClient,
  type ProxyPreset,
  type ProxyAdminRoute,
  type ProxyRouteRule,
  type ProxyState,
} from '../src/api/proxy-client';
import type { UIContext } from './dom';
import { routeAuthority } from './route-rules';
import { routesView } from './routes';

const opencodeKey = {
  name: 'opencode-go-key',
  routes: null,
  source: 'stored',
} as const;
const srvSecret = {
  name: 'srv-key',
  routes: null,
  source: 'stored',
} as const;
const rules: ProxyRouteRule[] = [
  {
    path_prefix: '/',
    upstream: 'https://opencode.ai/zen/go/v1',
    caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
    set_headers: { Authorization: { secret: 'opencode-go-key', prefix: 'Bearer ' } },
  },
  {
    path_prefix: '/messages/',
    upstream: 'https://opencode.ai/zen/go',
    caller_auth: { header: 'x-api-key', prefix: '' },
    set_headers: { 'x-api-key': { secret: 'opencode-go-key', prefix: '' } },
  },
  {
    path_prefix: '/gemini/',
    upstream: 'https://opencode.ai/inference/go/v1',
    caller_auth: { header: 'x-goog-api-key', prefix: '' },
    set_headers: { 'x-goog-api-key': { secret: 'opencode-go-key', prefix: '' } },
  },
];
const preset: ProxyPreset = {
  family: 'inference',
  id: 'opencode-go',
  label: 'OpenCode Go',
  // The backend preset leaves the parent empty: rules own every destination.
  upstream: '',
  auth: { header: '', prefix: '' },
  billing: 'plan',
  usage: 'auto',
  rules: rules.map((rule) => ({
    path_prefix: rule.path_prefix,
    upstream: rule.upstream,
    auth: rule.caller_auth,
  })),
};
const legacyPreset: ProxyPreset = {
  family: 'inference',
  id: 'openai',
  label: 'OpenAI API',
  upstream: 'https://api.openai.com/v1',
  auth: { header: 'Authorization', prefix: 'Bearer ' },
  billing: 'pay_as_you_go',
  usage: 'auto',
};
const unified: ProxyAdminRoute = {
  serves: [],
  profile: 'opencode-go',
  provider: 'opencode-go',
  name: 'opencode-go',
  local_prefix: '/opencode-go/',
  upstream: '',
  enabled: true,
  allow_insecure_http: false,
  caller_auth: { header: '', prefix: '' },
  set_headers: {},
  billing: 'plan',
  usage: 'auto',
  rules,
};
function baseState(overrides: Partial<ProxyState> = {}): ProxyState {
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
    presets: [preset, legacyPreset],
    accounts: [],
    ...overrides,
  };
}
function setup(overrides: Partial<ProxyState> = {}) {
  const api = new ProxyClient(vi.fn());
  const save = vi.spyOn(api, 'config').mockResolvedValue();
  const create = vi.spyOn(api, 'createProvider').mockResolvedValue({ revision: 8 });
  const announce = vi.fn();
  let refreshed: ProxyState | undefined;
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
    refresh: vi.fn(async () => refreshed),
    protectToken: vi.fn(),
    navigate: vi.fn(),
  };
  const state = withSingletonProviders(baseState(overrides));
  const root = routesView(state, api, context);
  document.body.append(root);
  return {
    root,
    save,
    create,
    announce,
    api,
    context,
    state,
    setRefreshed(next: ProxyState) {
      refreshed = next;
    },
  };
}
function choose(root: HTMLElement) {
  fireEvent.click(getByRole(root, 'button', { name: 'Add provider' }));
  fireEvent.click(getByRole(root, 'button', { name: 'OpenCode Go' }));
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
function ruleRows(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>('.subpath-rule')];
}
function openRuleEditors(root: HTMLElement): HTMLDetailsElement {
  const details = root.querySelector<HTMLDetailsElement>('details.route-rule-editors')!;
  details
    .closest<HTMLDetailsElement>('fieldset')
    ?.closest<HTMLDetailsElement>('details')
    ?.setAttribute('open', '');
  details.open = true;
  return details;
}
afterEach(() => {
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

it('creates OpenCode Go through one preset, key and provider request', async () => {
  const { root, create } = setup({ secrets: [opencodeKey] });
  choose(root);
  expect(root).toHaveTextContent('/messages/ → https://opencode.ai/zen/go');
  expect(root.querySelectorAll('input[type=password]')).toHaveLength(1);
  fireEvent.change(getByLabelText(root, 'Credential source'), { target: { value: 'existing' } });
  fireEvent.change(getByLabelText(root, 'Secret name'), { target: { value: opencodeKey.name } });
  fireEvent.click(getByRole(root, 'button', { name: 'Create provider' }));
  await waitFor(() =>
    expect(create).toHaveBeenCalledWith(7, {
      name: 'opencode-go',
      label: 'OpenCode Go',
      preset: 'opencode-go',
      secret: opencodeKey.name,
    }),
  );
});

it('loads and saves a unified route unchanged without asking to drop grants', async () => {
  const { root, save } = setup({ routes: [unified], secrets: [opencodeKey] });
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
  openManage(root, 'opencode-go');
  fireEvent.click(getByRole(root, 'button', { name: 'Edit opencode-go' }));
  expect(ruleRows(root)).toHaveLength(3);
  expect(root.querySelector<HTMLDetailsElement>('details.route-rule-editors')?.open).toBe(false);
  expect(getByLabelText(root, 'Secret name')).toHaveValue('opencode-go-key');
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(save.mock.calls[0][1]).toEqual([unified]);
  expect(confirm).not.toHaveBeenCalled();
});

it('edits subpaths and upstreams through the same agency-bearing rules', async () => {
  const { root, save } = setup({ routes: [unified], secrets: [opencodeKey] });
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
  openManage(root, 'opencode-go');
  fireEvent.click(getByRole(root, 'button', { name: 'Edit opencode-go' }));
  openRuleEditors(root);
  const rows = ruleRows(root);
  fireEvent.input(within(rows[1]).getByLabelText('Subpath'), {
    target: { value: '/anthropic/' },
  });
  fireEvent.input(within(rows[1]).getByLabelText('Upstream base URL'), {
    target: { value: 'https://opencode.ai/anthropic' },
  });
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  // Changing a rule upstream or its subpath mapping is an authority edit.
  expect(confirm).toHaveBeenCalledWith(expect.stringContaining('grants will be dropped'));
  const saved = save.mock.calls[0][1][0];
  expect(saved.rules?.[1]).toEqual({
    path_prefix: '/anthropic/',
    upstream: 'https://opencode.ai/anthropic',
    caller_auth: { header: 'x-api-key', prefix: '' },
    set_headers: { 'x-api-key': { secret: 'opencode-go-key', prefix: '' } },
  });
  expect(saved.upstream).toBe('');
});

it('treats subpath mappings as authority but ignores rule order and the envelope', () => {
  const reordered = [rules[2], rules[0], rules[1]];
  expect(routeAuthority({ ...unified, rules: reordered })).toBe(routeAuthority(unified));
  expect(
    routeAuthority({
      ...unified,
      rules: [{ ...rules[1], path_prefix: '/anthropic/' }, rules[0], rules[2]],
    }),
  ).not.toBe(routeAuthority(unified));
  const singleRule = { ...unified, rules: [rules[0]], set_headers: rules[0].set_headers };
  const legacySameUpstream = {
    ...singleRule,
    rules: undefined,
    upstream: rules[0].upstream,
  };
  expect(routeAuthority(singleRule)).not.toBe(routeAuthority(legacySameUpstream));
});

it('asks before a subpath mapping edit but not for billing-only edits', async () => {
  const { root, save } = setup({ routes: [unified], secrets: [opencodeKey] });
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
  openManage(root, 'opencode-go');
  fireEvent.click(getByRole(root, 'button', { name: 'Edit opencode-go' }));
  fireEvent.change(getByLabelText(root, 'Billing'), { target: { value: 'pay_as_you_go' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(confirm).not.toHaveBeenCalled();
  openRuleEditors(root);
  const rows = ruleRows(root);
  fireEvent.input(within(rows[1]).getByLabelText('Subpath'), {
    target: { value: '/anthropic/' },
  });
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(confirm).toHaveBeenCalledOnce());
  expect(save).toHaveBeenCalledOnce();
});

it('allows the same key across subpaths with different upstream origins', async () => {
  const { root, save } = setup({ routes: [unified], secrets: [opencodeKey] });
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  openManage(root, 'opencode-go');
  fireEvent.click(getByRole(root, 'button', { name: 'Edit opencode-go' }));
  openRuleEditors(root);
  fireEvent.input(within(ruleRows(root)[1]).getByLabelText('Upstream base URL'), {
    target: { value: 'https://other.test/v1' },
  });
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(save.mock.calls[0][1][0].rules?.[1]).toMatchObject({
    upstream: 'https://other.test/v1',
    set_headers: { 'x-api-key': { secret: opencodeKey.name } },
  });
});

it('reuses one Add API key dialog for every saved subpath', async () => {
  const { root, api, save, setRefreshed, state } = setup({
    routes: [unified],
    secrets: [opencodeKey],
  });
  openManage(root, 'opencode-go');
  fireEvent.click(getByRole(root, 'button', { name: 'Edit opencode-go' }));
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  vi.spyOn(api, 'secret').mockResolvedValue();
  setRefreshed({
    ...state,
    revision: 8,
    secrets: [{ name: 'opencode-go-key-2', routes: null, source: 'stored' }],
  });
  fireEvent.click(getByRole(root, 'button', { name: 'Add API key' }));
  const dialog = root.querySelector('dialog')!;
  expect(within(dialog).queryByLabelText('Bound origin')).toBeNull();
  fireEvent.input(within(dialog).getByLabelText('New secret value'), {
    target: { value: 'sk-opencode' },
  });
  fireEvent.click(within(dialog).getByRole('button', { name: 'Store secret' }));
  await waitFor(() =>
    expect(api.secret).toHaveBeenCalledWith('opencode-go-key-2', 'sk-opencode', false),
  );
  await waitFor(() => expect(getByLabelText(root, 'Secret name')).toHaveValue('opencode-go-key-2'));
  expect(root.querySelectorAll('.route-key')).toHaveLength(1);
  expect([...(getByLabelText(root, 'Secret name') as HTMLSelectElement).options]).toHaveLength(2);
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(save).toHaveBeenCalledWith(expect.objectContaining({ revision: 8 }), [
    expect.objectContaining({
      rules: rules.map((rule) =>
        expect.objectContaining({
          set_headers: expect.objectContaining({
            [rule.caller_auth.header]: expect.objectContaining({ secret: 'opencode-go-key-2' }),
          }),
        }),
      ),
    }),
  ]);
});

it('allows a stored key for SRV subpath rules and leaves target validation to the server', async () => {
  const srvRules = rules.map((rule) => ({
    ...rule,
    upstream: 'srv://_api._tcp.example.test/base',
    set_headers: Object.fromEntries(
      Object.entries(rule.set_headers).map(([header, config]) => [
        header,
        { ...config, secret: 'srv-key' },
      ]),
    ),
  }));
  const srvRoute = { ...unified, rules: srvRules };
  const { root, save } = setup({ routes: [srvRoute], secrets: [srvSecret] });
  openManage(root, 'opencode-go');
  fireEvent.click(getByRole(root, 'button', { name: 'Edit opencode-go' }));
  expect(getByLabelText(root, 'Secret name')).toHaveValue('srv-key');
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(save.mock.calls[0][1][0].rules?.every((rule) => rule.upstream.startsWith('srv://'))).toBe(
    true,
  );
  expect(
    save.mock.calls[0][1][0].rules?.every((rule) =>
      Object.values(rule.set_headers).some((header) => header.secret === 'srv-key'),
    ),
  ).toBe(true);
});

it('leaves an existing single-upstream route on the legacy editor and save shape', async () => {
  const legacy: ProxyAdminRoute = {
    serves: [],
    profile: 'opencode-go',
    provider: 'opencode-go',
    name: 'opencode-go',
    local_prefix: '/opencode-go/',
    upstream: 'https://opencode.ai/zen/go/v1',
    enabled: true,
    allow_insecure_http: false,
    caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
    set_headers: { Authorization: { secret: 'opencode-go-key', prefix: 'Bearer ' } },
    billing: 'pay_as_you_go',
  };
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
  const { root, save } = setup({ routes: [legacy], secrets: [opencodeKey] });
  openManage(root, 'opencode-go');
  fireEvent.click(getByRole(root, 'button', { name: 'Edit opencode-go' }));
  expect(ruleRows(root)).toHaveLength(0);
  expect(root.querySelector('.route-key')).toBeNull();
  expect(getByLabelText(root, 'Upstream base URL')).toHaveValue('https://opencode.ai/zen/go/v1');
  expect(getByLabelText(root, 'Header name')).toHaveValue('Authorization');
  expect(root.querySelectorAll('.header-substitution')).toHaveLength(1);
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(save.mock.calls[0][1]).toEqual([legacy]);
  expect(save.mock.calls[0][1][0]).not.toHaveProperty('rules');
  expect(confirm).not.toHaveBeenCalled();
});

it('shows one route card for a unified route with subpaths and one key badge', () => {
  const { root } = setup({ routes: [unified], secrets: [opencodeKey] });
  const cards = root.querySelectorAll('.route-card');
  expect(cards).toHaveLength(1);
  const card = cards[0] as HTMLElement;
  expect(getByRole(card, 'heading', { name: 'opencode-go' })).toBeVisible();
  const line = card.querySelector<HTMLElement>('.provider-route-line')!;
  expect(line).toHaveAttribute('tabindex', '-1');
  expect(line).toHaveAttribute('data-focus', 'route:opencode-go');
  expect(line.querySelector('code')).toHaveTextContent('/opencode-go/');
  const manage = card.querySelectorAll<HTMLDetailsElement>('details.provider-manage');
  expect(manage).toHaveLength(1);
  expect(manage[0].open).toBe(false);
  expect(manage[0].querySelector('summary')).toHaveTextContent('Manage');
  expect(getByRole(card, 'button', { name: 'Edit opencode-go' })).not.toBeVisible();
  openManage(root, 'opencode-go');
  expect(manage[0].open).toBe(true);
  expect(getByRole(card, 'button', { name: 'Edit opencode-go' })).toBeVisible();
  const settings = card.querySelector<HTMLElement>('.route-row-settings')!;
  expect(settings).toBeVisible();
  expect(settings).toHaveTextContent('3 subpath rules');
  expect(card).toHaveTextContent(
    'One key opencode-go-key → Authorization, x-api-key, x-goog-api-key',
  );
  const lines = [...card.querySelectorAll('.route-rule-lines small')].map(
    (line) => line.textContent,
  );
  expect(lines).toEqual([
    '/ → https://opencode.ai/zen/go/v1 · client Authorization: Bearer',
    '/messages/ → https://opencode.ai/zen/go · client x-api-key',
    '/gemini/ → https://opencode.ai/inference/go/v1 · client x-goog-api-key',
  ]);
  expect(queryByLabelText(root, 'Secret name')).toBeNull();
});

it('leaves saved sibling routes independent and never converts them on edit', () => {
  const sibling = (
    name: string,
    upstream: string,
    header: string,
    prefix: string,
  ): ProxyAdminRoute => ({
    ...unified,
    provider: name,
    profile: '',
    serves: [],
    rules: undefined,
    name,
    local_prefix: `/${name}/`,
    upstream,
    billing: 'plan',
    caller_auth: { header, prefix },
    set_headers: { [header]: { secret: 'opencode-go-key', prefix } },
  });
  const siblings = [
    sibling('opencode-go', 'https://opencode.ai/zen/go/v1', 'Authorization', 'Bearer '),
    sibling('opencode-go-messages', 'https://opencode.ai/zen/go', 'x-api-key', ''),
    sibling('opencode-go-gemini', 'https://opencode.ai/inference/go/v1', 'x-goog-api-key', ''),
  ];
  const { root } = setup({ routes: siblings, secrets: [opencodeKey] });
  expect(root.querySelectorAll('.route-card')).toHaveLength(3);
  expect(ruleRows(root)).toHaveLength(0);
  openManage(root, 'opencode-go-messages');
  fireEvent.click(getByRole(root, 'button', { name: 'Edit opencode-go-messages' }));
  expect(ruleRows(root)).toHaveLength(0);
  expect(root.querySelector('.route-key')).toBeNull();
  expect(getByLabelText(root, 'Upstream base URL')).toHaveValue('https://opencode.ai/zen/go');
  expect(getByLabelText(root, 'Local client authentication header')).toHaveValue('x-api-key');
});

it('normalizes null rule header maps from loaded state', async () => {
  const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
    new Response(
      JSON.stringify({
        ...baseState(),
        routes: [
          {
            ...unified,
            rules: rules.map((rule) => ({ ...rule, set_headers: null })),
          },
        ],
      }),
    ),
  );
  const api = new ProxyClient(fetcher);
  const loaded = await api.state();
  expect(loaded.routes[0].rules?.map((rule) => rule.set_headers)).toEqual([{}, {}, {}]);
});

it('renders catch-alls as prose and sends raw catch-all syntax from the editor', async () => {
  const saved = {
    ...unified,
    rules: rules.map((rule, index) =>
      index === 0 ? { ...rule, allow: ['GET /v1/*', 'HEAD /*', 'POST /models'] } : rule,
    ),
  };
  const { root, save } = setup({ routes: [saved], secrets: [opencodeKey] });
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
  openManage(root, saved.name);
  expect(root.querySelector('.route-row-settings')).toHaveTextContent('GET any path below /v1/');
  expect(root.querySelector('.route-row-settings')).toHaveTextContent('HEAD any path');
  expect(root.querySelector('.route-row-settings')).toHaveTextContent('POST /models');
  fireEvent.click(getByRole(root, 'button', { name: `Edit ${saved.name}` }));
  openRuleEditors(root);
  const allow = within(ruleRows(root)[0]).getByLabelText('Allowed requests');
  expect(allow).toHaveValue('GET /v1/*\nHEAD /*\nPOST /models');
  fireEvent.input(allow, { target: { value: 'GET /v2/*' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(confirm).toHaveBeenCalledOnce();
  expect(save.mock.calls[0][1][0].rules?.[0].allow).toEqual(['GET /v2/*']);
});
