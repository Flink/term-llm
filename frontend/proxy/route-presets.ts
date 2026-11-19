import type { ProxyPreset, ProxyRoute, ProxyState } from '../src/api/proxy-client';
import { button, card, el } from './dom';
import { nativeProvider } from './native';
import { routeRules } from './route-rules';

const featured = ['claude-code', 'chatgpt'];

export function presetLabel(preset: ProxyPreset): string {
  return preset.id === 'claude-code' ? 'Claude Code' : preset.label;
}

function sameEndpoint(a: string, b: string): boolean {
  return !!a && !!b && a.replace(/\/$/, '') === b.replace(/\/$/, '');
}

// The catalog supplies presentation, not route authority. Match by saved
// destination and account kind instead of assuming names identify providers.
export function matchRoutePreset(route: ProxyRoute, state: ProxyState): ProxyPreset | undefined {
  const presets = state.presets ?? [];
  const rules = routeRules(route);
  const kind = state.accounts?.find((account) => account.name === route.account)?.kind;
  if (nativeProvider(kind)) return presets.find((preset) => preset.account_kind === kind);
  const matches = presets.filter((preset) => {
    if (kind ? preset.account_kind !== kind : !!preset.account_kind !== !!route.account)
      return false;
    if (!rules.length)
      return !preset.rules?.length && sameEndpoint(route.upstream, preset.upstream);
    return (
      preset.rules?.length === rules.length &&
      rules.every((rule) =>
        preset.rules?.some(
          (candidate) =>
            candidate.path_prefix === rule.path_prefix &&
            sameEndpoint(candidate.upstream, rule.upstream),
        ),
      )
    );
  });
  return matches.find((preset) => preset.id === route.name) ?? matches[0];
}

// Group headings already say "plan" or "API", so the picker drops the suffix.
export function pickerLabel(preset: ProxyPreset): string {
  return presetLabel(preset).replace(/ (plan|API)$/, '');
}

const groups: [NonNullable<ProxyPreset['group']>, string][] = [
  ['plan', 'Subscriptions'],
  ['self_hosted', 'Self-hosted'],
  ['api_key', 'API keys'],
  ['media', 'Speech, images & embeddings'],
];

const hints: Record<string, string> = {
  'claude-code': 'Sign in once, then add devices',
  'agy-bin': 'Sign in once on Linux, then add devices',
  chatgpt: 'Includes dictation, embeddings and live',
  grok: 'Includes Grok CLI',
  gemini: 'Includes Live',
  'opencode-go': 'Chat, Messages and Gemini',
  bedrock: 'AWS account',
  elevenlabs: 'Speech and music',
  flux: 'Images',
  mistral: 'Transcription',
  jina: 'Embeddings',
  voyage: 'Embeddings',
};

function presetHint(preset: ProxyPreset): string {
  if (hints[preset.id]) return hints[preset.id];
  if (preset.group !== 'self_hosted') return '';
  return preset.upstream ? new URL(preset.upstream).host : 'Your server URL';
}

const marks: Record<string, string> = {
  chatgpt: 'GP',
  openai: 'OA',
  openrouter: 'OR',
  gemini: 'GE',
  'openai-compatible': '{}',
  elevenlabs: '11',
};

function initials(label: string): string {
  const words = label.split(/[\s-]+/).filter(Boolean);
  return (words.length > 1 ? words[0][0] + words[1][0] : label.slice(0, 2)).toUpperCase();
}

export function routePresetPicker(
  presets: ProxyPreset[],
  choose: (preset?: ProxyPreset) => void,
  close: () => void,
): HTMLElement {
  // Companions are created with their parent, never chosen on their own.
  const companions = new Set(presets.flatMap((preset) => preset.companions ?? []));
  const offered = presets
    .filter((preset) => !companions.has(preset.id))
    .map((preset, index) => ({ preset, rank: featured.indexOf(preset.id) >>> 0, index }))
    .sort((a, b) => a.rank - b.rank || a.index - b.index)
    .map(({ preset }) => preset);
  const rows: { node: HTMLButtonElement; text: string }[] = [];
  const search = el('input');
  search.type = 'search';
  search.className = 'route-picker-search';
  search.placeholder = 'Search providers';
  search.setAttribute('aria-label', 'Search providers');
  const sections = groups.map(([group, title]) => {
    const choices = offered
      .filter((preset) => (preset.group ?? 'api_key') === group)
      .map((preset) => {
        const label = pickerLabel(preset);
        const mark = el('span', marks[preset.id] ?? initials(label));
        mark.className = 'route-preset-mark';
        mark.setAttribute('aria-hidden', 'true');
        const name = el('span', label);
        name.className = 'route-preset-name';
        const control = button('', () => choose(preset));
        control.className = 'route-preset-choice';
        control.setAttribute('aria-label', label);
        control.append(mark, name);
        const hint = presetHint(preset);
        if (hint) control.append(el('small', hint));
        rows.push({
          node: control,
          text: `${label} ${preset.label} ${preset.id} ${hint}`.toLowerCase(),
        });
        return control;
      });
    if (!choices.length) return undefined;
    const heading = el('h3', title);
    const section = el('div', heading, ...choices);
    section.className = 'route-preset-group';
    return section;
  });
  const [plans, selfHosted, apiKeys, media] = sections;
  const columns = [
    el('div', ...[plans, selfHosted].filter((node) => !!node)),
    el('div', ...[apiKeys, media].filter((node) => !!node)),
  ];
  const grid = el('div', ...columns.filter((column) => column.childElementCount));
  grid.className = 'route-preset-groups';
  const empty = el('p', 'No matching providers. Use a custom endpoint instead.');
  empty.className = 'route-preset-empty';
  empty.hidden = true;
  search.addEventListener('input', () => {
    const query = search.value.trim().toLowerCase();
    for (const row of rows) row.node.hidden = !!query && !row.text.includes(query);
    for (const section of sections)
      if (section) section.hidden = !section.querySelector('.route-preset-choice:not([hidden])');
    empty.hidden = rows.some((row) => !row.node.hidden);
  });
  search.addEventListener('keydown', (event) => {
    if (event.key === 'Enter') {
      event.preventDefault();
      rows.find((row) => !row.node.hidden)?.node.click();
    } else if (event.key === 'Escape') close();
  });
  const custom = button('Custom endpoint', () => choose());
  custom.className = 'route-preset-custom';
  const footer = el('div', el('span', 'Not listed?'), custom);
  footer.className = 'route-picker-footer';
  const picker = card('Add a provider', grid, empty, footer);
  picker.classList.add('route-picker');
  const heading = picker.querySelector('h2')!;
  const header = el('div');
  header.className = 'route-picker-header';
  heading.replaceWith(header);
  header.append(heading, search, button('Cancel', close));
  return picker;
}
