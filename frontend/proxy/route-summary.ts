import type { ProxyBilling, ProxyRoute, ProxyState } from '../src/api/proxy-client';
import { el } from './dom';
import { allowLabel, authLabel, joinLocalPrefix, routeRules } from './route-rules';

const billingLabels: Record<ProxyBilling, string> = {
  pay_as_you_go: 'Pay as you go',
  plan: 'Subscription',
  local: 'Local',
  unspecified: 'Not specified',
};

function displayEndpoint(value: string): string {
  try {
    const url = new URL(value);
    if (url.protocol === 'https:' || url.protocol === 'http:')
      return `${url.origin}${url.pathname === '/' ? '' : url.pathname}`;
    if (url.protocol === 'srv:' && !url.username && !url.password)
      return `srv://${url.host}${url.pathname}`;
  } catch {
    // Never echo a malformed endpoint, which could contain credentials.
  }
  return '';
}

function fact(term: string, ...details: (Node | string)[]): HTMLElement[] {
  return [el('dt', term), el('dd', ...details)];
}

function note(text: string): HTMLElement {
  const node = el('small', text);
  node.className = 'route-fact-note';
  return node;
}

/**
 * Plain-language facts for one route, in request order: what clients call,
 * where the proxy forwards it, and what the proxy adds upstream. Client and
 * upstream credentials share header names, so each says which side it is.
 */
export function routeSummary(route: ProxyRoute & { serves?: string[] }, state: ProxyState) {
  const rules = routeRules(route);
  const destinations = rules.length ? rules : [route];
  const keyNames = new Map<string, Set<string>>();
  const literalHeaders = new Set<string>();
  for (const { set_headers: headers } of destinations) {
    for (const [header, config] of Object.entries(headers)) {
      if (config.secret !== undefined) {
        const names = keyNames.get(config.secret) ?? new Set<string>();
        names.add(config.prefix?.trim() ? `${header}: ${config.prefix.trim()}` : header);
        keyNames.set(config.secret, names);
      } else if (config.value !== undefined) literalHeaders.add(header);
    }
  }
  const facts = el('dl');
  facts.className = 'route-facts';

  if (rules.length) {
    const list = el(
      'ul',
      ...rules.map((rule) =>
        el(
          'li',
          el('code', joinLocalPrefix(route.local_prefix, rule.path_prefix)),
          ` → ${displayEndpoint(rule.upstream) || 'Invalid upstream'}`,
          note(`client token in ${authLabel(rule.caller_auth)}`),
          ...allowDetails(rule.allow),
        ),
      ),
    );
    list.className = 'route-fact-rules';
    facts.append(
      ...fact(
        'Subpaths',
        `${rules.length} subpath ${rules.length === 1 ? 'rule' : 'rules'}; one client token covers all of them.`,
        list,
      ),
    );
  } else {
    facts.append(
      ...fact(
        'Clients call',
        el('code', route.local_prefix),
        note(`with their client token in ${authLabel(route.caller_auth)}`),
      ),
      ...fact('Forwards to', el('code', displayEndpoint(route.upstream) || 'Invalid upstream')),
    );
  }

  const upstream: (Node | string)[] = [];
  for (const [name, headers] of keyNames) {
    const missing = !name || !state.secrets.some((secret) => secret.name === name);
    const key = el('span', name ? `API key ${name}` : 'API key (not set)');
    if (missing) {
      key.className = 'route-fact-missing';
      if (name) key.append(' (missing)');
    }
    upstream.push(
      el('div', key, note(`added as ${[...headers].join(', ')}${missing ? ' once set' : ''}`)),
    );
  }
  for (const header of literalHeaders)
    upstream.push(
      el('div', `Public value in ${header}`, note('Not a secret; stored in the route')),
    );
  if (route.account) upstream.push(el('div', `Account ${route.account}`));
  if (!upstream.length) upstream.push(el('span', 'None'));
  facts.append(...fact('Upstream auth', ...upstream));
  facts.append(...fact('Billing', billingLabels[route.billing || 'unspecified']));
  if (!rules.length && route.allow?.length)
    facts.append(...fact('Allowed requests', ...allowDetails(route.allow)));
  if (route.serves?.length) {
    facts.append(...fact('term-llm uses it for', route.serves.join(' · ')));
  }
  const advanced = el('div', facts);
  advanced.className = 'route-row-settings';
  return { advanced };
}

function allowDetails(entries?: string[]): HTMLElement[] {
  if (!entries?.length) return [];
  const list = el('ul', ...entries.map((entry) => el('li', allowLabel(entry))));
  list.className = 'route-fact-allow';
  return [list];
}
