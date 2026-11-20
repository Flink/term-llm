import type { ProxyState } from '../src/api/proxy-client';

// Most UI fixtures need only singleton providers. Fixtures for grouping or
// status supply the server's provider entries explicitly.
export function withSingletonProviders(state: ProxyState): ProxyState {
  if (state.providers.length || !state.routes.length) return state;
  return {
    ...state,
    providers: [...new Set(state.routes.map((route) => route.provider))].map((id) => ({
      id,
      label: id,
      family: 'inference',
      status: 'green',
      reason: '',
      routes: state.routes.filter((route) => route.provider === id).map((route) => route.name),
    })),
  };
}
