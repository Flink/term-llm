import type { ProxyPreset, ProxyRoute, ProxyState } from '../src/api/proxy-client';
import { check, sameOrigin } from './dom';

function slotTaken(name: string, routes: ProxyRoute[]): boolean {
  const prefix = `/${name}/`;
  return routes.some(
    (r) =>
      r.name === name || r.local_prefix.startsWith(prefix) || prefix.startsWith(r.local_prefix),
  );
}

// Clients bind ancillary features (dictation, embeddings, live, CLI) by fixed
// route names, so a companion is only offered while its name is free.
export function availableCompanions(preset: ProxyPreset | undefined, state: ProxyState) {
  return (preset?.companions ?? [])
    .map((id) => state.presets?.find((candidate) => candidate.id === id))
    .filter((companion): companion is ProxyPreset => !!companion)
    .filter((companion) => !slotTaken(companion.id, state.routes));
}

// Each companion reuses the parent's account, or its key when the companion
// talks to the same origin. Anything that cannot be carried over is skipped.
export function companionRoutes(
  main: ProxyRoute,
  preset: ProxyPreset,
  companions: ProxyPreset[],
): ProxyRoute[] {
  if (main.rules?.length || !preset.upstream || !main.upstream) return [];
  if (!sameOrigin(preset.upstream, main.upstream)) return [];
  const header = preset.auth.header.toLowerCase();
  const secret = Object.entries(main.set_headers).find(
    ([key, rule]) => key.toLowerCase() === header && rule.secret,
  )?.[1].secret;
  const out: ProxyRoute[] = [];
  for (const companion of companions) {
    if (slotTaken(companion.id, [main, ...out])) continue;
    const viaAccount = !!companion.account_kind;
    if (viaAccount ? companion.account_kind !== preset.account_kind || !main.account : !secret)
      continue;
    if (!viaAccount && !sameOrigin(companion.upstream, main.upstream)) continue;
    out.push({
      name: companion.id,
      local_prefix: `/${companion.id}/`,
      upstream: companion.upstream,
      enabled: true,
      allow_insecure_http: false,
      caller_auth: companion.auth,
      set_headers: viaAccount
        ? {}
        : { [companion.auth.header]: { secret, prefix: companion.auth.prefix } },
      billing: companion.billing,
      usage: companion.usage,
      websocket: companion.websocket || undefined,
      ...(viaAccount ? { account: main.account } : {}),
    });
  }
  return out;
}

export function companionToggle(companions: ProxyPreset[]) {
  const names = companions.map((companion) => companion.id).join(' and ');
  return check(`Also create ${names} for the related client features`, true);
}
