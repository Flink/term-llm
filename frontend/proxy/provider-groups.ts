import type {
  ProxyCaller,
  ProxyPreset,
  ProxyProviderClientConfig,
  ProxyRoute,
  ProxyState,
} from '../src/api/proxy-client';
import { el } from './dom';
import { matchRoutePreset, presetLabel } from './route-presets';

export interface ProviderGroup {
  id: string;
  label: string;
  routes: ProxyRoute[];
  config?: ProxyProviderClientConfig;
  config_yaml?: string;
}

export function providerGroups(state: ProxyState): ProviderGroup[] {
  const byName = new Map(state.routes.map((route) => [route.name, route]));
  const groups: ProviderGroup[] = [];
  for (const provider of state.providers ?? []) {
    const routes: ProxyRoute[] = [];
    for (const name of provider.routes) {
      const route = byName.get(name);
      if (route) {
        routes.push(route);
        byName.delete(name);
      }
    }
    if (routes.length) groups.push({ ...provider, routes });
  }
  // A mixed-version response, or a route without a known integration, must
  // still have its own management surface. Never guess shared ownership here.
  for (const route of byName.values()) {
    const preset = matchRoutePreset(route, state);
    const label = preset && preset.id === route.name ? presetLabel(preset) : route.name;
    groups.push({ id: route.name, label, routes: [route] });
  }
  return groups;
}

export function writableProviderID(group: ProviderGroup, state: ProxyState): string {
  // A legacy singleton can share its name with an explicit group.
  return group.id.startsWith('route:') ? freeProviderID(group.routes[0].name, state) : group.id;
}

export function freeProviderID(name: string, state: ProxyState): string {
  const taken = new Set(state.routes.flatMap((route) => [route.name, route.provider]));
  for (let serial = 1; ; serial++) {
    const suffix = serial === 1 ? '' : `-${serial}`;
    const id = `${name.slice(0, 64 - suffix.length)}${suffix}`;
    if (!taken.has(id)) return id;
  }
}

const capabilities: Record<string, string> = {
  chatgpt: 'Chat, models, images & realtime call setup',
  'chatgpt-backend': 'Usage & dictation',
  'chatgpt-openai': 'Embeddings & realtime sideband',
  'gemini-live': 'Realtime audio',
  grok: 'Chat, models & plan usage',
  'grok-bin': 'Grok CLI',
  openai: 'OpenAI API requests',
};

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

export function providerRouteLine(
  route: ProxyRoute,
  preset: ProxyPreset | undefined,
  warning: string | undefined,
  providerLabel: string,
): HTMLElement {
  const label = preset ? (capabilities[preset.id] ?? presetLabel(preset)) : route.name;
  const text = el('span');
  if (label !== providerLabel) text.append(label);
  if (warning) {
    if (text.textContent) text.append(' · ');
    const issue = el('span', warning);
    issue.className = 'provider-route-warning';
    text.append(issue);
  }
  const row = el('li', el('code', route.local_prefix), text);
  row.className = 'provider-route-line';
  row.tabIndex = -1;
  row.dataset.focus = `route:${route.name}`;
  return row;
}
