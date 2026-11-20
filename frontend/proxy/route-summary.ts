import type { ProxyBilling, ProxyRoute, ProxyState } from '../src/api/proxy-client';
import { badge, el } from './dom';
import { allowLabel, authLabel, routeRules } from './route-rules';

const billingLabels: Record<ProxyBilling, string> = {
  pay_as_you_go: 'Pay as you go',
  plan: 'Subscription',
  local: 'Local',
  unspecified: 'Billing not specified',
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

export function routeSummary(route: ProxyRoute, state: ProxyState) {
  const rules = routeRules(route);
  const destinations = rules.length ? rules : [route];
  const keyNames = new Map<string, Set<string>>();
  const literalHeaders = new Set<string>();
  for (const { set_headers: headers } of destinations) {
    for (const [header, config] of Object.entries(headers)) {
      if (config.secret !== undefined) {
        const names = keyNames.get(config.secret) ?? new Set<string>();
        names.add(header);
        keyNames.set(config.secret, names);
      } else if (config.value !== undefined) literalHeaders.add(header);
    }
  }
  const settings = el('div', badge(billingLabels[route.billing || 'unspecified']));
  settings.className = 'route-badges';
  for (const [name, headers] of keyNames) {
    settings.append(
      badge(
        `${headers.size > 1 ? 'One key' : 'Key'} ${name ? `${name}${state.secrets.some((secret) => secret.name === name) ? '' : ' (missing)'}` : '(not set)'} → ${[...headers].join(', ')}`,
        'credential',
      ),
    );
  }
  for (const header of literalHeaders)
    settings.append(badge(`Public literal → ${header}`, 'credential'));
  const lines = el('div');
  lines.className = 'route-rule-lines';
  if (rules.length) {
    for (const rule of rules)
      lines.append(
        el(
          'div',
          el(
            'small',
            `${rule.path_prefix} → ${displayEndpoint(rule.upstream) || 'Invalid upstream'} · client ${authLabel(rule.caller_auth)}`,
          ),
          ...allowDetails(rule.allow),
        ),
      );
  } else {
    lines.append(
      el(
        'div',
        el('small', 'Upstream: '),
        el('code', displayEndpoint(route.upstream) || 'Invalid upstream'),
      ),
      el('div', el('small', `Client authentication: ${authLabel(route.caller_auth)}`)),
    );
  }
  if (!rules.length) lines.append(...allowDetails(route.allow));
  if (route.account) lines.append(el('div', el('small', `Provider account: ${route.account}`)));
  const advanced = el(
    'div',
    ...(rules.length
      ? [el('p', `${rules.length} subpath ${rules.length === 1 ? 'rule' : 'rules'}`)]
      : []),
    lines,
    settings,
  );
  advanced.className = 'route-row-settings';
  return { advanced };
}

function allowDetails(entries?: string[]): HTMLElement[] {
  if (!entries?.length) return [];
  return [el('ul', ...entries.map((entry) => el('li', allowLabel(entry))))];
}
