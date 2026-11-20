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

export function allowLabel(entry: string): string {
  const catchAll = /^(\S+) (\/(?:[^*]*\/)?)\*$/.exec(entry);
  if (!catchAll) return entry;
  const [, method, prefix] = catchAll;
  return prefix === '/' ? `${method} any path` : `${method} any path below ${prefix}`;
}
