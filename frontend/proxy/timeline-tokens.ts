import type { TimelineSeries, TimelineTokenParts } from '../src/api/proxy-timeline';
import { el } from './dom';

const components = [
  ['input', 'Input'],
  ['output', 'Output'],
  ['cache_read', 'Cache read'],
  ['cache_write', 'Cache write'],
] as const;

export interface TokenMix {
  /** The recorded breakdown only. Absent without usable parts or on aggregate overflow. */
  parts?: TimelineTokenParts;
  /** Sum of `parts`, never includes legacy total-only rows. */
  measuredTokens?: number;
  missingBreakdown: boolean;
  /** Undefined if the request count is invalid or too large to represent exactly. */
  unknownRequests: number | undefined;
  overflow: boolean;
}

function safeCount(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
}

function measuredTotal(parts: TimelineTokenParts): number | undefined {
  let total = 0;
  for (const [key] of components) {
    const amount = parts[key];
    if (!safeCount(amount)) return undefined;
    total += amount;
  }
  return safeCount(total) ? total : undefined;
}

/** Aggregate disjoint series rows, e.g. a bucket, one group, or the selected range. */
export function aggregateTokenMix(
  series: Iterable<Pick<TimelineSeries, 'token_parts' | 'tokens' | 'unknown_tokens'>>,
): TokenMix {
  const mix: TokenMix = { missingBreakdown: false, unknownRequests: 0, overflow: false };
  for (const row of series) {
    const unknown = row.unknown_tokens;
    if (mix.unknownRequests !== undefined) {
      const count = mix.unknownRequests + unknown;
      mix.unknownRequests = safeCount(unknown) && safeCount(count) ? count : undefined;
    }

    const parts = row.token_parts;
    const total = parts && measuredTotal(parts);
    if (!parts || total === undefined || (row.tokens !== undefined && row.tokens !== total)) {
      // Unknown-only rows are already covered by the unknown request count.
      if (parts || row.tokens !== undefined || unknown === 0) mix.missingBreakdown = true;
      continue;
    }
    if (mix.overflow) continue;
    const sum = (mix.measuredTokens ?? 0) + total;
    if (!safeCount(sum)) {
      mix.overflow = true;
      delete mix.parts;
      delete mix.measuredTokens;
      continue;
    }
    const combined = (mix.parts ??= { input: 0, output: 0, cache_read: 0, cache_write: 0 });
    // Nonnegative parts are bounded by the already-checked sum.
    for (const [key] of components) combined[key] += parts[key];
    mix.measuredTokens = sum;
  }
  return mix;
}

function partial(mix: TokenMix): boolean {
  return mix.missingBreakdown || mix.unknownRequests !== 0;
}

function formatPart(mix: TokenMix, key: keyof TimelineTokenParts): string {
  const amount = mix.parts?.[key];
  if (amount === undefined) return 'Not measured';
  return `${partial(mix) ? '≥ ' : ''}${amount.toLocaleString()}`;
}

/** Compact, exact-count note for a breakdown row or tooltip. */
export function tokenMixText(mix: TokenMix): string {
  if (mix.overflow) return 'Token breakdown unavailable (count too large)';
  if (!mix.parts) return 'Token breakdown unavailable';
  const text = components.map(([key, label]) => `${label}: ${formatPart(mix, key)}`).join(' · ');
  return partial(mix) ? `${text} · Partial token breakdown` : text;
}

function coverageNote(mix: TokenMix): string {
  if (mix.overflow) return 'Token breakdown unavailable: the measured count is too large.';
  const notes: string[] = [];
  if (!mix.parts) notes.push('No token breakdown available.');
  else if (partial(mix)) notes.push('Measured breakdown only; counts are lower bounds.');
  if (mix.parts && mix.missingBreakdown) notes.push('Some rows lack a usable breakdown.');
  const count = mix.unknownRequests;
  if (count === undefined) notes.push('Incomplete request count unavailable.');
  else if (count > 0) {
    notes.push(
      `${count.toLocaleString()} ${count === 1 ? 'request has' : 'requests have'} incomplete token data.`,
    );
  }
  return notes.join(' ');
}

function tokenStrip(parts: TimelineTokenParts, total: number): HTMLElement {
  const strip = el('div');
  strip.className = 'timeline-token-strip';
  strip.setAttribute('aria-hidden', 'true');
  for (const [key] of components) {
    if (parts[key] === 0) continue;
    const segment = el('span');
    segment.dataset.part = key;
    segment.style.width = `${(parts[key] / total) * 100}%`;
    strip.append(segment);
  }
  return strip;
}

let mixSequence = 0;

/** Always show the four labelled values; the strip represents measured parts only. */
export function tokenMixView(mix: TokenMix): HTMLElement {
  const title = el('h3', 'Token mix');
  title.id = `timeline-token-mix-${++mixSequence}`;
  const view = el('section', title);
  view.className = 'timeline-token-mix';
  view.setAttribute('aria-labelledby', title.id);
  if (mix.parts && mix.measuredTokens) view.append(tokenStrip(mix.parts, mix.measuredTokens));

  const list = el('dl');
  for (const [key, label] of components) {
    const swatch = el('span');
    swatch.className = 'timeline-token-swatch';
    swatch.setAttribute('aria-hidden', 'true');
    const item = el('div', el('dt', swatch, label), el('dd', formatPart(mix, key)));
    item.dataset.part = key;
    list.append(item);
  }
  view.append(list);
  const note = coverageNote(mix);
  if (note) view.append(el('p', note));
  return view;
}
