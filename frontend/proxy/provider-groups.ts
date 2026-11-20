import type {
  ProxyAdminRoute,
  ProxyCaller,
  ProxyProvider,
  ProxyProviderFamily,
  ProxyState,
} from '../src/api/proxy-client';
import { el } from './dom';

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

export function approvalSummary(group: ProviderGroup, state: ProxyState): HTMLElement {
  const approved = state.callers.filter((caller) => providerApproval(group, caller) === 'approved');
  const partial = state.callers.filter((caller) => providerApproval(group, caller) === 'partial');
  const names = approved
    .slice(0, 3)
    .map((caller) => caller.name)
    .join(', ');
  const more = approved.length > 3 ? ` +${approved.length - 3} more` : '';
  const summary = el('p', approved.length ? `Approved: ${names}${more}` : 'No clients approved');
  if (partial.length)
    summary.append(
      ` · ${partial.length} ${partial.length === 1 ? 'client needs' : 'clients need'} approval`,
    );
  summary.className = 'provider-approval-summary';
  return summary;
}

export function providerRouteLine(route: ProxyAdminRoute): HTMLElement {
  const row = el('li', el('code', route.local_prefix), el('span', route.serves.join(' · ')));
  row.className = 'provider-route-line';
  row.tabIndex = -1;
  row.dataset.focus = `route:${route.name}`;
  return row;
}
