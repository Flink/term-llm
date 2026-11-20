import type { ProxyState } from '../src/api/proxy-client';

export const slugPattern = '[A-Za-z0-9][A-Za-z0-9_\\-]{0,63}';

export function requireProviderName(name: string, state: ProxyState): void {
  if (!new RegExp(`^${slugPattern}$`).test(name))
    throw new Error(
      'Use 1–64 letters, numbers, hyphens or underscores, starting with a letter or number.',
    );
  if (state.providers.some((provider) => provider.id === name))
    throw new Error(`Provider ${name} already exists.`);
  if (state.routes.some((route) => route.name === name))
    throw new Error(`Provider name ${name} is already used by a route.`);
}
