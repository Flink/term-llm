import type {
  ProxyAdminRoute,
  ProxyCaller,
  ProxyProvider,
  ProxyProviderFamily,
  ProxyState,
} from '../src/api/proxy-client';

export const providerFamilies: [ProxyProviderFamily, string][] = [
  ['inference', 'Inference'],
  ['classification', 'Classification'],
  ['search', 'Search'],
  ['media', 'Media'],
  ['embeddings', 'Embeddings'],
];

export interface ProviderGroup extends Omit<ProxyProvider, 'routes'> {
  routes: ProxyAdminRoute[];
}

export function providerGroups(state: ProxyState): ProviderGroup[] {
  const routes = new Map(state.routes.map((route) => [route.name, route]));
  return state.providers.map((provider) => ({
    ...provider,
    routes: provider.routes.flatMap((name) => {
      const route = routes.get(name);
      return route ? [route] : [];
    }),
  }));
}

export function providerApproval(
  group: ProviderGroup,
  caller: ProxyCaller,
): 'none' | 'partial' | 'approved' {
  if (caller.revoked) return 'none';
  const count = group.routes.filter((route) => caller.grants.includes(route.name)).length;
  return !count ? 'none' : count === group.routes.length ? 'approved' : 'partial';
}
