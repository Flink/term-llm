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
  type ProxyRoute,
  type ProxyRouteRule,
  type ProxyState,
} from '../src/api/proxy-client';
import type { UIContext } from './dom';
import { routeAuthority } from './route-rules';
import { routesView } from './routes';

const opencodeKey = {
  name: 'opencode-go-key',
  origin: 'https://opencode.ai',
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
  id: 'openai',
  label: 'OpenAI API',
  upstream: 'https://api.openai.com/v1',
  auth: { header: 'Authorization', prefix: 'Bearer ' },
  billing: 'pay_as_you_go',
  usage: 'auto',
};
const unified: ProxyRoute = {
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
  const state = baseState(overrides);
  const root = routesView(state, api, context);
  document.body.append(root);
  return {
    root,
    save,
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
  fireEvent.click(getByRole(root, 'button', { name: 'Add route' }));
  fireEvent.click(getByRole(root, 'button', { name: 'OpenCode Go' }));
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

it('offers one OpenCode Go preset that creates three editable subpath rules with one key', () => {
  const { root } = setup({ secrets: [opencodeKey] });
  choose(root);
  expect(getByLabelText(root, 'Name')).toHaveValue('opencode-go');
  expect(getByLabelText(root, 'Local prefix')).toHaveValue('/opencode-go/');
  expect(getByLabelText(root, 'Billing')).toHaveValue('plan');
  const form = root.querySelector<HTMLFormElement>('.route-form')!;
  const core = form.querySelector<HTMLElement>('.route-form-grid')!;
  // The parent upstream is unused while rules exist, so it is not offered twice.
  expect(within(core).queryByLabelText('Upstream base URL')).toBeNull();
  // Rules own client authentication; the backend rejects a parent caller_auth.
  expect(queryByLabelText(root, 'Local client authentication header')).toBeNull();
  expect(queryByLabelText(root, 'Local client authentication prefix')).toBeNull();
  const rows = ruleRows(root);
  expect(rows).toHaveLength(3);
  const editorDisclosure = form.querySelector<HTMLDetailsElement>('details.route-rule-editors')!;
  expect(editorDisclosure.open).toBe(false);
  const summary = form.querySelector<HTMLElement>('.route-rule-summary')!;
  expect(summary.querySelectorAll('li')).toHaveLength(3);
  expect(summary).toHaveTextContent('/ → https://opencode.ai/zen/go/v1');
  expect(summary).toHaveTextContent('/messages/ → https://opencode.ai/zen/go');
  expect(summary).toHaveTextContent('/gemini/ → https://opencode.ai/inference/go/v1');
  editorDisclosure.open = true;
  expect(within(rows[0]).getByLabelText('Subpath')).toHaveValue('/');
  expect(within(rows[0]).getByLabelText('Upstream base URL')).toHaveValue(
    'https://opencode.ai/zen/go/v1',
  );
  expect(within(rows[1]).getByLabelText('Subpath')).toHaveValue('/messages/');
  expect(within(rows[1]).getByLabelText('Upstream base URL')).toHaveValue(
    'https://opencode.ai/zen/go',
  );
  expect(within(rows[2]).getByLabelText('Subpath')).toHaveValue('/gemini/');
  expect(within(rows[2]).getByLabelText('Upstream base URL')).toHaveValue(
    'https://opencode.ai/inference/go/v1',
  );
  // Protocol header details live in Advanced with the authority warning.
  const advanced = getByLabelText(form, 'Usage measurement').closest('details')!;
  expect(advanced.open).toBe(false);
  expect(advanced).toHaveTextContent('/ · client Authorization: Bearer');
  expect(advanced).toHaveTextContent('/messages/ · client x-api-key');
  expect(advanced).toHaveTextContent('/gemini/ · client x-goog-api-key');
  expect(advanced).toHaveTextContent('no parent client authentication');
  expect(advanced).toHaveTextContent('Changing a subpath mapping');
  // Exactly one key entry and one selector serve all three substitutions.
  expect(form.querySelectorAll('.route-key')).toHaveLength(1);
  expect(form.querySelectorAll('.header-substitution')).toHaveLength(0);
  const key = getByLabelText(form, 'Secret name') as HTMLSelectElement;
  expect(key.tagName).toBe('SELECT');
  expect([...key.options].map((option) => option.value)).toEqual(['', 'opencode-go-key']);
  expect(form).toHaveTextContent('One key and one client grant cover all three protocol paths');
  expect(form).toHaveTextContent('Shared upstream origin: https://opencode.ai');
  expect(getByRole(form, 'button', { name: 'Add API key' })).toBeVisible();
  expect(form.querySelector('.preview')).toHaveTextContent('One client token and one client grant');
});

it('saves unified rules with one shared secret and a single scoped route', async () => {
  const { root, save } = setup({ secrets: [opencodeKey] });
  choose(root);
  const form = root.querySelector<HTMLFormElement>('.route-form')!;
  fireEvent.change(getByLabelText(form, 'Secret name'), {
    target: { value: 'opencode-go-key' },
  });
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  const routes = save.mock.calls[0][1];
  expect(routes).toHaveLength(1);
  expect(routes[0]).toEqual({
    name: 'opencode-go',
    local_prefix: '/opencode-go/',
    // The backend rejects a nonempty parent upstream when rules exist.
    upstream: '',
    enabled: true,
    allow_insecure_http: false,
    caller_auth: { header: '', prefix: '' },
    set_headers: {},
    billing: 'plan',
    usage: 'auto',
    rules: [
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
    ],
  });
  expect(routes[0]).not.toHaveProperty('account');
});

it('never serializes parent destination fields for a rules route from a stale preset', async () => {
  const stalePreset: ProxyPreset = {
    ...preset,
    upstream: 'https://ignored.test/v1',
    auth: { header: 'Authorization', prefix: 'Bearer ' },
    websocket: true,
  };
  const { root, save } = setup({ presets: [stalePreset], secrets: [opencodeKey] });
  choose(root);
  fireEvent.change(getByLabelText(root, 'Secret name'), {
    target: { value: 'opencode-go-key' },
  });
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  const saved = save.mock.calls[0][1][0];
  expect(saved.upstream).toBe('');
  expect(saved.caller_auth).toEqual({ header: '', prefix: '' });
  expect(saved.set_headers).toEqual({});
  expect(saved).not.toHaveProperty('allow');
});

it('loads and saves a unified route unchanged without asking to drop grants', async () => {
  const { root, save } = setup({ routes: [unified], secrets: [opencodeKey] });
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
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

it('keeps one key across all rules and refuses a mixed upstream origin', async () => {
  const { root, save, announce } = setup({ routes: [unified], secrets: [opencodeKey] });
  fireEvent.click(getByRole(root, 'button', { name: 'Edit opencode-go' }));
  openRuleEditors(root);
  const rows = ruleRows(root);
  fireEvent.input(within(rows[1]).getByLabelText('Upstream base URL'), {
    target: { value: 'https://other.test/v1' },
  });
  expect(root.querySelector('.route-key .warning')).toHaveTextContent(
    'must share one upstream origin',
  );
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() =>
    expect(announce).toHaveBeenCalledWith(expect.stringContaining("stored secret's exact origin")),
  );
  expect(save).not.toHaveBeenCalled();
});

it('reuses one Add API key dialog for every rule with the shared origin', async () => {
  const { root, api, save, setRefreshed, state } = setup();
  choose(root);
  vi.spyOn(api, 'secret').mockResolvedValue();
  setRefreshed({
    ...state,
    revision: 8,
    secrets: [{ name: 'opencode-go-key', origin: 'https://opencode.ai', source: 'stored' }],
  });
  fireEvent.click(getByRole(root, 'button', { name: 'Add API key' }));
  const dialog = root.querySelector('dialog')!;
  expect(within(dialog).getByLabelText('Bound origin')).toHaveValue('https://opencode.ai');
  fireEvent.input(within(dialog).getByLabelText('New secret value'), {
    target: { value: 'sk-opencode' },
  });
  fireEvent.click(within(dialog).getByRole('button', { name: 'Store secret' }));
  await waitFor(() =>
    expect(api.secret).toHaveBeenCalledWith(
      'opencode-go-key',
      'https://opencode.ai',
      'sk-opencode',
      false,
    ),
  );
  await waitFor(() => expect(getByLabelText(root, 'Secret name')).toHaveValue('opencode-go-key'));
  expect(root.querySelectorAll('.route-key')).toHaveLength(1);
  expect([...(getByLabelText(root, 'Secret name') as HTMLSelectElement).options]).toHaveLength(2);
  fireEvent.click(getByRole(root, 'button', { name: 'Save route' }));
  await waitFor(() => expect(save).toHaveBeenCalledOnce());
  expect(save).toHaveBeenCalledWith(8, [
    expect.objectContaining({
      rules: rules.map((rule) =>
        expect.objectContaining({
          set_headers: expect.objectContaining({
            [rule.caller_auth.header]: expect.objectContaining({ secret: 'opencode-go-key' }),
          }),
        }),
      ),
    }),
  ]);
});

it('leaves an existing single-upstream route on the legacy editor and save shape', async () => {
  const legacy: ProxyRoute = {
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
  const settings = card.querySelector<HTMLDetailsElement>('.route-row-settings')!;
  expect(settings.open).toBe(false);
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
  const sibling = (name: string, upstream: string, header: string, prefix: string): ProxyRoute => ({
    ...unified,
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
