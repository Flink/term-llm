import { afterEach, expect, it } from 'vitest';
import type { ProxyAdminRoute, ProxyState } from '../src/api/proxy-client';
import { routeSummary } from './route-summary';

const base: ProxyAdminRoute = {
  serves: ['providers.venice.api_key', 'providers.venice.base_url'],
  profile: 'venice',
  provider: 'venice',
  name: 'venice',
  local_prefix: '/venice/',
  upstream: 'https://api.venice.ai/api/v1',
  enabled: true,
  allow_insecure_http: false,
  caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
  set_headers: { Authorization: { secret: 'venice-key', prefix: 'Bearer ' } },
  billing: 'pay_as_you_go',
};

function state(secrets: string[] = []): ProxyState {
  return {
    defaults: null,
    providers: [],
    auth: 'token',
    mode: 'ui',
    revision: 1,
    routes: [base],
    secrets: secrets.map((name) => ({ name, source: 'stored', routes: null })),
    callers: [],
    admin_keys: [],
    csrf: 'csrf',
  };
}

function facts(route: ProxyAdminRoute, loaded = state()): Record<string, string> {
  const view = routeSummary(route, loaded).advanced;
  document.body.append(view);
  const terms = [...view.querySelectorAll('dt')];
  return Object.fromEntries(
    terms.map((dt) => [dt.textContent, dt.nextElementSibling!.textContent]),
  );
}

afterEach(() => document.body.replaceChildren());

it('describes a route in request order and names which side each credential belongs to', () => {
  expect(facts(base, state(['venice-key']))).toEqual({
    'Clients call': '/venice/with their client token in Authorization: Bearer',
    'Forwards to': 'https://api.venice.ai/api/v1',
    'Upstream auth': 'API key venice-keyadded as Authorization: Bearer',
    Billing: 'Pay as you go',
    'term-llm uses it for': 'providers.venice.api_key · providers.venice.base_url',
  });
});

it('marks a missing key and describes literal headers, accounts and unset billing', () => {
  const view = routeSummary(base, state()).advanced;
  expect(view.querySelector('.route-fact-missing')).toHaveTextContent(
    'API key venice-key (missing)',
  );
  expect(view).toHaveTextContent('added as Authorization: Bearer once set');
  expect(
    facts({
      ...base,
      serves: [],
      billing: undefined,
      account: 'home-chat',
      set_headers: { 'X-Project': { value: 'public' } },
    }),
  ).toMatchObject({
    'Upstream auth': 'Public value in X-ProjectNot a secret; stored in the routeAccount home-chat',
    Billing: 'Not specified',
  });
  expect(facts({ ...base, set_headers: {} })['Upstream auth']).toBe('None');
});

it('never echoes credentials, queries or malformed upstreams', () => {
  const leaky = facts({
    ...base,
    upstream: 'https://user:secret-pass@example.com/api?token=secret-query#frag',
  });
  expect(leaky['Forwards to']).toBe('https://example.com/api');
  expect(facts({ ...base, upstream: 'secret-malformed' })['Forwards to']).toBe('Invalid upstream');
  expect(document.body.textContent).not.toContain('secret-');
});
