import type { ProxyPreset, ProxyRoute, ProxyState } from '../src/api/proxy-client';
import { button, card, el } from './dom';
import { providerFamilies } from './provider-groups';

const featured = ['claude-bin', 'chatgpt'];

/** A blank profile has no default endpoint, so the operator must supply the root. */
export function presetUpstreamHint(preset: ProxyPreset): string {
  if (preset.family !== 'classification' || preset.upstream) return '';
  return 'Enter the full Workers AI run root (ending in /ai/run) or AI Gateway Workers AI root (ending in /workers-ai), with the account and gateway IDs filled in. Do not append the model path.';
}

export function matchRoutePreset(route: ProxyRoute, state: ProxyState): ProxyPreset | undefined {
  return state.presets?.find((preset) => preset.id === route.profile);
}

function inferenceGroup(preset: ProxyPreset): string {
  if (preset.billing === 'plan') return 'Subscriptions';
  if (preset.billing === 'local' || ['vllm', 'openai-compatible'].includes(preset.id))
    return 'Self-hosted';
  return 'API keys';
}

const hints: Record<string, string> = {
  'claude-bin': 'Sign in once, then add devices',
  'agy-bin': 'Personal subscriptions only · Sign in once on Linux',
  chatgpt: 'Includes dictation, embeddings and live',
  gemini: 'Includes Live',
  'opencode-go': 'Chat, Messages and Gemini',
  bedrock: 'AWS account',
  elevenlabs: 'Speech and music',
  flux: 'Images',
  mistral: 'Transcription',
  jina: 'Embeddings',
  voyage: 'Embeddings',
  typesafe: 'Jev classification',
  'openai-decisions': 'Separate from OpenAI chat',
  'cloudflare-clef': 'Workers AI or Gateway root',
};

function presetHint(preset: ProxyPreset): string {
  if (hints[preset.id]) return hints[preset.id];
  if (preset.family !== 'inference' || inferenceGroup(preset) !== 'Self-hosted') return '';
  return preset.upstream ? new URL(preset.upstream).host : 'Your server URL';
}

const marks: Record<string, string> = {
  chatgpt: 'GP',
  openai: 'OA',
  openrouter: 'OR',
  gemini: 'GE',
  'openai-compatible': '{}',
  elevenlabs: '11',
  typesafe: 'TS',
  'openai-decisions': 'OD',
  'cloudflare-clef': 'CF',
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
  const subgroups: HTMLElement[] = [];
  function choicesFor(candidates: ProxyPreset[], title?: string) {
    const choices = candidates.map((preset) => {
      const { label } = preset;
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
        text: `${label} ${preset.id} ${hint}`.toLowerCase(),
      });
      return control;
    });
    if (!choices.length) return undefined;
    const section = el('div', ...(title ? [el('h4', title)] : []), ...choices);
    section.className = 'route-preset-group';
    subgroups.push(section);
    return section;
  }
  const sections = providerFamilies.flatMap(([family, title]) => {
    const candidates = offered.filter((preset) => preset.family === family);
    if (!candidates.length) return [];
    const groups =
      family === 'inference'
        ? ['Subscriptions', 'API keys', 'Self-hosted'].map((group) =>
            choicesFor(
              candidates.filter((preset) => inferenceGroup(preset) === group),
              group,
            ),
          )
        : [choicesFor(candidates)];
    const heading = el('h3', title);
    const section = el('section', heading, ...groups.filter((group) => !!group));
    section.className = 'route-preset-family';
    section.setAttribute('aria-label', title);
    return [section];
  });
  const grid = el('div', ...sections);
  grid.className = 'route-preset-groups';
  const empty = el('p', 'No matching providers. Use a custom endpoint instead.');
  empty.className = 'route-preset-empty';
  empty.hidden = true;
  search.addEventListener('input', () => {
    const query = search.value.trim().toLowerCase();
    for (const row of rows) row.node.hidden = !!query && !row.text.includes(query);
    for (const section of [...sections, ...subgroups])
      section.hidden = !section.querySelector('.route-preset-choice:not([hidden])');
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
