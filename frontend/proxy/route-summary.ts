import type { ProxyBilling, ProxyRoute, ProxyState } from '../src/api/proxy-client';
import { badge, el, sameOrigin } from './dom';
import { isSharedNativeAccount, legacyNativeNotice, nativeProvider } from './native';
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

export function routeSummary(route: ProxyRoute, state: ProxyState) {
  const rules = routeRules(route);
  const account = state.accounts?.find((item) => item.name === route.account);
  const native = nativeProvider(account?.kind);
  const shared = isSharedNativeAccount(account);
  const legacy = native && !shared;
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
  const signInLabel = shared && native ? `Shared ${native.shortName}` : 'Provider';
  const providerIssue =
    !account && route.account
      ? 'Provider account unavailable'
      : legacy
        ? 'Shared subscription migration needed'
        : account && account.state !== 'ready'
          ? `${signInLabel} sign-in ${account.state === 'refreshing' ? 'refreshing' : 'needed'}`
          : account?.auth_mode === 'token_only' &&
              account.expires_at &&
              account.expires_at <= Date.now() / 1000
            ? 'Provider sign-in needed'
            : missingKey
              ? 'Provider key needed'
              : undefined;
  const status =
    providerIssue ??
    (shared ? `${signInLabel} subscription ready` : account ? 'Connected' : 'Configured');
  const warnings = [
    !route.enabled ? 'Disabled' : '',
    routeUpstreams(route).some((upstream) => upstream.toLowerCase().startsWith('http:'))
      ? 'HTTP · unencrypted'
      : '',
  ].filter(Boolean);
  const provider = el(
    'p',
    legacy
      ? legacyNativeNotice
      : providerIssue
        ? providerIssue + '.'
        : shared
          ? 'The proxy is signed in. Workers use their own authorized launcher without signing in.'
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
  const advanced = el(
    'div',
    ...(rules.length
      ? [el('p', `${rules.length} subpath ${rules.length === 1 ? 'rule' : 'rules'}`)]
      : []),
    lines,
    settings,
  );
  advanced.className = 'route-row-settings';
  return { status, providerIssue, warnings, provider, advanced };
}
