import {
  fireEvent,
  getByLabelText,
  getByRole,
  queryByLabelText,
  waitFor,
} from '@testing-library/preact';
import { expect, it, vi } from 'vitest';
import { ProxyClient, type ProxyAdminRoute, type ProxyState } from '../src/api/proxy-client';
import { clientDefaults } from './provider-defaults';
import type { UIContext } from './dom';

function route(provider: string, name: string, serves: string[]): ProxyAdminRoute {
  return {
    name,
    provider,
    profile: name,
    serves,
    enabled: true,
    local_prefix: `/${name}/`,
    upstream: 'https://example.test',
    allow_insecure_http: false,
    caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
    set_headers: {},
  };
}

const state: ProxyState = {
  revision: 12,
  mode: 'ui',
  auth: 'token',
  csrf: 'csrf',
  callers: [],
  admin_keys: [],
  secrets: [],
  providers: [],
  defaults: { search: 'exa' },
  routes: [
    route('exa', 'exa', ['search: exa']),
    route('exa', 'exa-mcp', ['search: exa_mcp', 'fetch: exa_mcp']),
    route('jina', 'jina-reader', ['fetch: jina']),
    route('openai-copy', 'openai-copy', ['providers.openai-copy.base_url']),
    route('openai', 'openai', ['image: openai', 'embed: openai']),
    route('typesafe', 'typesafe', ['classify: typesafe']),
  ],
};

const context: UIContext = {
  run: async (action) => {
    await action();
    return true;
  },
  refresh: vi.fn(),
  announce: vi.fn(),
  navigate: vi.fn(),
  protectToken: vi.fn(),
};

it('replaces the defaults map using route names, including cross-family fetch', async () => {
  const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response('{"revision":13}'));
  const view = clientDefaults(state, new ProxyClient(fetcher), context);
  expect(view.querySelectorAll('select')).toHaveLength(7);
  const search = getByLabelText(view, 'Search') as HTMLSelectElement;
  const fetchSelect = getByLabelText(view, 'Fetch') as HTMLSelectElement;
  const classifySelect = getByLabelText(view, 'Classification') as HTMLSelectElement;
  expect(search).toHaveValue('exa');
  expect(search.options[2].text).toBe('exa · exa_mcp');
  expect(fetchSelect.options[2].text).toBe('jina · jina');
  expect(classifySelect.options[1].text).toBe('typesafe · typesafe');
  expect(getByLabelText(view, 'Image').querySelector('option[value="openai-copy"]')).toBeNull();
  expect(queryByLabelText(view, 'Embed')).toBeNull();
  fireEvent.change(search, { target: { value: '' } });
  fireEvent.change(fetchSelect, { target: { value: 'jina-reader' } });
  fireEvent.change(classifySelect, { target: { value: 'typesafe' } });
  fireEvent.submit(getByRole(view, 'button', { name: 'Save defaults' }).closest('form')!);
  await waitFor(() =>
    expect(fetcher).toHaveBeenCalledWith(
      '/_proxy/api/defaults',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({
          revision: 12,
          defaults: { fetch: 'jina-reader', classify: 'typesafe' },
        }),
      }),
    ),
  );
});

it('shows file-managed selections without a save action', () => {
  const view = clientDefaults({ ...state, mode: 'file' }, new ProxyClient(vi.fn()), context);
  expect(getByLabelText(view, 'Search')).toBeDisabled();
  expect(getByLabelText(view, 'Search')).toHaveValue('exa');
  expect(view.querySelector('button')).toBeNull();
});
