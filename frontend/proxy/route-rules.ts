import type { ProxyAuth, ProxyRoute, ProxyRouteRule } from '../src/api/proxy-client';

// Helpers for routes that unify several subpath upstreams behind one local
// prefix, one client token and one client grant. A route without rules keeps
// all authority in its own upstream/set_headers fields; nothing here converts it.
export function routeRules(route: ProxyRoute): ProxyRouteRule[] {
  return route.rules ?? [];
}
export function hasRules(route: ProxyRoute): boolean {
  return routeRules(route).length > 0;
}
/** The header that carries this subpath's upstream credential, if any. */
export function ruleCredentialHeader(rule: ProxyRouteRule): string {
  const secret = Object.keys(rule.set_headers).find(
    (header) => rule.set_headers[header]?.secret !== undefined,
  );
  return secret ?? rule.caller_auth.header;
}
/** Whether the rule currently expects a stored secret (not a public literal). */
export function ruleNeedsSecret(rule: ProxyRouteRule): boolean {
  return Object.values(rule.set_headers).some((header) => header.secret !== undefined);
}
export function routeNeedsSecret(route: ProxyRoute): boolean {
  const rules = routeRules(route);
  return rules.length
    ? rules.some(ruleNeedsSecret)
    : Object.values(route.set_headers).some((header) => header.secret !== undefined);
}
export function authLabel(auth: ProxyAuth): string {
  if (!auth.header) return 'No credential header';
  return auth.prefix ? `${auth.header}: ${auth.prefix.trim()}` : auth.header;
}
/** Join a route's local mount and a rule subpath without doubling slashes. */
export function joinLocalPrefix(localPrefix: string, subpath: string): string {
  return `${localPrefix.replace(/\/+$/, '')}/${subpath.replace(/^\/+/, '')}`;
}

function headerAuthority(setHeaders: ProxyRoute['set_headers']): unknown[] {
  return Object.entries(setHeaders)
    .map(([name, rule]) => [
      name.toLowerCase(),
      rule.secret ?? null,
      rule.prefix ?? '',
      rule.value ?? null,
    ])
    .sort((a, b) => String(a[0]).localeCompare(String(b[0])));
}

/**
 * Authority-bearing route settings for grant invalidation. A rules envelope is
 * distinct from a single upstream, subpath mappings count, and rule order does
 * not. Local prefixes, enabled state and client auth formats are excluded.
 */
export function routeAuthority(route: ProxyRoute): string {
  const shared = [
    route.account || '',
    !!route.websocket,
    [...(route.allow ?? [])].sort(),
    route.redirects || 'passthrough',
  ];
  const rules = routeRules(route);
  if (rules.length) {
    const mappings = rules
      .map((rule) => {
        const url = new URL(rule.upstream);
        return [
          rule.path_prefix,
          url.origin,
          url.pathname.replace(/\/$/, ''),
          headerAuthority(rule.set_headers),
          [...(rule.allow ?? [])].sort(),
        ];
      })
      .sort((a, b) => String(a[0]).localeCompare(String(b[0])));
    return JSON.stringify(['rules', mappings, ...shared]);
  }
  const url = new URL(route.upstream);
  return JSON.stringify([
    'single',
    [url.origin, url.pathname.replace(/\/$/, ''), headerAuthority(route.set_headers)],
    ...shared,
  ]);
}

export function allowLabel(entry: string): string {
  const catchAll = /^(\S+) (\/(?:[^*]*\/)?)\*$/.exec(entry);
  if (!catchAll) return entry;
  const [, method, prefix] = catchAll;
  return prefix === '/' ? `${method} any path` : `${method} any path below ${prefix}`;
}
