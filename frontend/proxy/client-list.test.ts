import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, getByLabelText, getByRole, queryByRole } from '@testing-library/preact';
import {
  ProxyClient,
  type ProxyAdminKey,
  type ProxyCaller,
  type ProxyRoute,
  type ProxyState,
} from '../src/api/proxy-client';
import { duplicateSuffixes, shortDate, sortCallers } from './client-list';
import type { UIContext } from './dom';
import { callersView } from './security';

const route = (name: string): ProxyRoute => ({
  name,
  local_prefix: `/${name}/`,
  upstream: 'https://example.com/',
  enabled: true,
  allow_insecure_http: false,
  caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
  set_headers: {},
});
const caller = (overrides: Partial<ProxyCaller>): ProxyCaller => ({
  id: 'c_one',
  name: 'One',
  created: 1,
  revoked: false,
  grants: [],
  created_by: '',
  external_id: '',
  ...overrides,
});

afterEach(() => {
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

function render(callers: ProxyCaller[], adminKeys: ProxyAdminKey[] = []) {
  const state: ProxyState = {
    auth: 'token',
    mode: 'ui',
    revision: 3,
    routes: [route('venice'), route('chatgpt')],
    secrets: [],
    callers,
    admin_keys: adminKeys,
    csrf: 'csrf',
  };
  const context: UIContext = {
    run: async (action) => {
      await action();
      return true;
    },
    refresh: vi.fn(async () => state),
    announce: vi.fn(),
    navigate: vi.fn(),
    protectToken: vi.fn(),
  };
  const root = callersView(state, new ProxyClient(vi.fn()), context);
  document.body.append(root);
  return root;
}

describe('shortDate', () => {
  const now = new Date(2026, 10, 18, 16, 0);
  it.each([
    [undefined, 'Never'],
    ['nonsense', 'Unknown'],
    [new Date(2026, 10, 18, 9, 5).getTime(), /^Today /],
    [new Date(2026, 10, 17, 23, 59).getTime(), /^Yesterday /],
    [new Date(2026, 10, 2, 12).getTime(), /^Nov 2$/],
    [new Date(2025, 10, 2, 12).getTime(), /2025/],
  ])('formats %s', (value, expected) => {
    expect(shortDate(value, 'Never', now)).toMatch(expected);
  });
});

it('suffixes only duplicate names, lengthening IDs until they differ', () => {
  const suffixes = duplicateSuffixes([
    caller({ id: 'c_abcd1111', name: 'Claude Code' }),
    caller({ id: 'c_abcd2222', name: 'Claude Code' }),
    caller({ id: 'c_ffff0000', name: 'arch' }),
  ]);
  expect(Object.fromEntries(suffixes)).toEqual({ c_abcd1111: 'abcd1', c_abcd2222: 'abcd2' });
});

it('sorts by most recent use, then newest creation', () => {
  const sorted = sortCallers([
    caller({ id: 'old', created: 1 }),
    caller({ id: 'used', created: 1, last_used: 5 }),
    caller({ id: 'new', created: 2 }),
  ]);
  expect(sorted.map((c) => c.id)).toEqual(['used', 'new', 'old']);
});

it('collapses creation behind New client once active clients exist', () => {
  const root = render([caller({ grants: ['venice'] })]);
  const name = getByLabelText(root, 'Client name');
  const toggle = getByRole(root, 'button', { name: 'New client' });
  expect(name).not.toBeVisible();
  fireEvent.click(toggle);
  expect(toggle).toHaveAttribute('aria-expanded', 'true');
  expect(document.activeElement).toBe(name);
  fireEvent.input(name, { target: { value: 'draft' } });
  fireEvent.click(getByRole(root, 'button', { name: 'Cancel' }));
  expect(name).not.toBeVisible();
  expect((name as HTMLInputElement).value).toBe('');
  expect(document.activeElement).toBe(toggle);
});

it('opens creation by default when no active client exists', () => {
  const root = render([caller({ revoked: true })]);
  expect(getByLabelText(root, 'Client name')).toBeVisible();
});

it('labels API-created clients with their key name or revoked status and external ID', () => {
  const root = render(
    [
      caller({ id: 'c_ci', name: 'CI', created_by: 'ak_active', external_id: 'build/runner-1' }),
      caller({ id: 'c_old', name: 'Old runner', created_by: 'ak_deleted', revoked: true }),
      caller({ id: 'c_browser', name: 'Browser' }),
    ],
    [{ id: 'ak_active', name: '<Automation>', created: 1, last_used: 0 }],
  );
  const active = getByRole(root, 'button', { name: 'CI' }).closest('article');
  expect(active).toHaveTextContent('Created by API key ‹<Automation>›');
  expect(active).toHaveTextContent('External ID: build/runner-1');
  expect(active?.querySelector('automation')).toBeNull();
  const revoked = root.querySelector('details.client-revoked')!;
  revoked.setAttribute('open', '');
  expect(
    getByRole(revoked as HTMLElement, 'heading', { name: 'Old runner' }).closest('article'),
  ).toHaveTextContent('Created by a revoked API key');
  const browser = getByRole(root, 'button', { name: 'Browser' }).closest('article');
  expect(browser).not.toHaveTextContent(/Created by|External ID/);
});

it('keeps one row expanded and folds revoked clients away without actions', () => {
  const root = render([
    caller({ id: 'c_aaaa1', name: 'Claude Code', grants: ['venice'] }),
    caller({ id: 'c_bbbb2', name: 'Claude Code', grants: ['chatgpt'] }),
    caller({ id: 'c_old', name: 'term', revoked: true, grants: ['venice'] }),
  ]);
  const first = getByRole(root, 'button', { name: 'Claude Code #aaaa' });
  const second = getByRole(root, 'button', { name: 'Claude Code #bbbb' });
  fireEvent.click(first);
  expect(root.querySelectorAll('.client-detail form')).toHaveLength(1);
  fireEvent.click(second);
  expect(first).toHaveAttribute('aria-expanded', 'false');
  expect(second).toHaveAttribute('aria-expanded', 'true');
  expect(root.querySelectorAll('.client-detail form')).toHaveLength(1);
  expect(getByRole(root, 'button', { name: 'Revoke Claude Code #bbbb' })).toBeVisible();
  const revoked = root.querySelector('details.client-revoked')!;
  expect(revoked.querySelector('summary')).toHaveTextContent('Revoked (1)');
  expect(revoked).toHaveTextContent('term');
  expect(queryByRole(revoked as HTMLElement, 'button')).toBeNull();
});
