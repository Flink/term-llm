import type { ProxyBilling, ProxyPreset, ProxyRoute, ProxyState } from '../src/api/proxy-client';
import { badge, disclosure, el, sameOrigin } from './dom';
import { presetLabel } from './route-presets';
import { authLabel, routeRules, routeUpstreams } from './route-rules';

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
  } catch {
    // Never echo a malformed endpoint, which could contain credentials.
  }
  return '';
}

export function routeSummary(route: ProxyRoute, state: ProxyState, preset?: ProxyPreset) {
  const rules = routeRules(route);
  const account = state.accounts?.find((item) => item.name === route.account);
  const providerName = preset
    ? presetLabel(preset)
    : account?.kind === 'claude-code'
      ? 'Claude Code'
      : account?.kind === 'chatgpt'
        ? 'ChatGPT plan'
        : 'Custom endpoint';
  const clients = state.callers.filter(
    (caller) => !caller.revoked && caller.grants.includes(route.name),
  ).length;
  const destinations = rules.length ? rules : [route];
  const keyNames = new Map<string, Set<string>>();
  const literalHeaders = new Set<string>();
  let missingKey = false;
  for (const { upstream, set_headers: headers } of destinations) {
    for (const [header, config] of Object.entries(headers)) {
      if (config.secret !== undefined) {
        const names = keyNames.get(config.secret) ?? new Set<string>();
        names.add(header);
        keyNames.set(config.secret, names);
        missingKey ||= !state.secrets.some(
          (secret) => secret.name === config.secret && sameOrigin(secret.origin, upstream),
        );
      } else if (config.value !== undefined) literalHeaders.add(header);
    }
  }
  const nativeLogin = account?.kind === 'claude-code';
  const providerIssue =
    !account && route.account
      ? 'Provider account unavailable'
      : account && !nativeLogin && account.state !== 'ready'
        ? account.state === 'refreshing'
          ? 'Provider sign-in refreshing'
          : 'Provider sign-in needed'
        : account?.auth_mode === 'token_only' &&
            account.expires_at &&
            account.expires_at <= Date.now() / 1000
          ? 'Provider sign-in needed'
          : missingKey
            ? 'Provider key needed'
            : undefined;
  const status = !route.enabled
    ? badge('Disabled', 'muted')
    : providerIssue
      ? badge(providerIssue, 'caution')
      : !clients
        ? badge('Needs client access', 'caution')
        : nativeLogin
          ? badge('Native sign-in on each device')
          : badge('Configured');
  const title = el('h2', route.name);
  title.tabIndex = -1;
  const meta = el('div', badge(providerName), status);
  meta.className = 'route-badges';
  if (routeUpstreams(route).some((upstream) => upstream.toLowerCase().startsWith('http:')))
    meta.append(badge('HTTP · unencrypted', 'caution'));
  const summary = el('div', title, el('code', route.local_prefix), meta);
  summary.className = 'route-summary';
  const provider = el(
    'p',
    providerIssue
      ? providerIssue + '.'
      : nativeLogin
        ? 'Sign in inside Claude Code after running the device launcher.'
        : account
          ? 'Provider account connected.'
          : keyNames.size
            ? 'Stored provider key configured.'
            : 'No stored provider credentials needed.',
  );
  const settings = el('div', badge(billingLabels[route.billing || 'unspecified']));
  settings.className = 'route-badges';
  for (const [name, headers] of keyNames) {
    settings.append(
      badge(
        `${headers.size > 1 ? 'One key' : 'Key'} ${name || '(not set)'} → ${[...headers].join(', ')}`,
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
  if (route.account) lines.append(el('div', el('small', `Provider account: ${route.account}`)));
  const advanced = disclosure(
    'Advanced',
    ...(rules.length
      ? [el('p', `${rules.length} subpath ${rules.length === 1 ? 'rule' : 'rules'}`)]
      : []),
    lines,
    settings,
  );
  advanced.className = 'route-row-settings';
  return { summary, provider, advanced };
}
