import type { ProxyPreset, ProxyState } from '../src/api/proxy-client';
import type { ProviderGroup } from './provider-groups';

// Restore missing preset companions only. An arbitrary unprofiled route in an
// inference provider would advertise a second inference endpoint for its ID.
export function missingCompanions(group: ProviderGroup, state: ProxyState): ProxyPreset[] {
  const presets = state.presets ?? [];
  const owned = new Set(group.routes.map((route) => route.profile));
  const primary = presets.find((preset) => owned.has(preset.id) && preset.companions?.length);
  if (!primary) return [];
  return (primary.companions ?? []).flatMap((id) => {
    if (owned.has(id) || (id === 'grok-bin' && group.id !== primary.id)) return [];
    const companion = presets.find((preset) => preset.id === id);
    return companion ? [companion] : [];
  });
}

export function companionRouteName(
  group: ProviderGroup,
  companion: ProxyPreset,
  state: ProxyState,
): string {
  const parent = state.presets?.find((preset) => preset.companions?.includes(companion.id));
  return parent && group.id !== parent.id
    ? `${group.id}-${companion.id.replace(`${parent.id}-`, '')}`.slice(0, 64)
    : companion.id;
}
