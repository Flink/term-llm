import { getByRole } from '@testing-library/preact';
import { describe, expect, it } from 'vitest';
import type { TimelineTokenParts } from '../src/api/proxy-timeline';
import { aggregateTokenMix, tokenMixText, tokenMixView } from './timeline-tokens';

const parts = { input: 50, output: 25, cache_read: 20, cache_write: 5 };
const zero = { input: 0, output: 0, cache_read: 0, cache_write: 0 };
const measured = { tokens: 100, token_parts: parts, unknown_tokens: 0 };

function definitions(view: HTMLElement): (string | null)[] {
  return [...view.querySelectorAll('dd')].map((item) => item.textContent);
}

describe('timeline token mix', () => {
  it('sums disjoint components across rows without adding the existing total again', () => {
    const mix = aggregateTokenMix([
      measured,
      { token_parts: { ...parts, input: 0 }, unknown_tokens: 0 },
    ]);
    expect(mix.measuredTokens).toBe(150);
    expect(mix.parts).toEqual({ input: 50, output: 50, cache_read: 40, cache_write: 10 });
    expect(tokenMixText(mix)).toBe('Input: 50 · Output: 50 · Cache read: 40 · Cache write: 10');
  });

  it('names the four values and draws proportional segments using the same order', () => {
    const mix = aggregateTokenMix([measured]);
    const container = document.createElement('div');
    // Keep the fixture isolated while giving aria-labelledby an ID lookup root.
    document.createDocumentFragment().append(container);
    container.append(tokenMixView(mix));
    const view = getByRole(container, 'region', { name: 'Token mix' });
    expect([...view.querySelectorAll('dt')].map((item) => item.textContent)).toEqual([
      'Input',
      'Output',
      'Cache read',
      'Cache write',
    ]);
    expect(definitions(view)).toEqual(['50', '25', '20', '5']);
    const strip = view.querySelector('.timeline-token-strip');
    expect(strip).toHaveAttribute('aria-hidden', 'true');
    expect(
      [...view.querySelectorAll<HTMLElement>('.timeline-token-strip > span')].map((segment) => [
        segment.dataset.part,
        segment.style.width,
      ]),
    ).toEqual([
      ['input', '50%'],
      ['output', '25%'],
      ['cache_read', '20%'],
      ['cache_write', '5%'],
    ]);
  });

  it.each([
    ['no activity', []],
    ['legacy total', [{ tokens: 100, unknown_tokens: 0 }]],
    ['legacy zero total', [{ tokens: 0, unknown_tokens: 0 }]],
    ['unmeasured activity', [{ unknown_tokens: 3 }]],
  ] as const)('leaves the four values unknown for %s', (_name, rows) => {
    const mix = aggregateTokenMix(rows);
    expect(mix.parts).toBeUndefined();
    expect(mix.measuredTokens).toBeUndefined();
    expect(tokenMixText(mix)).toBe('Token breakdown unavailable');
    const view = tokenMixView(mix);
    expect(view.querySelector('.timeline-token-strip')).toBeNull();
    expect(definitions(view)).toEqual(Array(4).fill('Not measured'));
  });

  it('preserves explicitly reported zeros without drawing a misleading strip', () => {
    const mix = aggregateTokenMix([{ tokens: 0, token_parts: zero, unknown_tokens: 0 }]);
    expect(mix.measuredTokens).toBe(0);
    expect(tokenMixText(mix)).toBe('Input: 0 · Output: 0 · Cache read: 0 · Cache write: 0');
    const view = tokenMixView(mix);
    expect(view.querySelector('.timeline-token-strip')).toBeNull();
    expect(definitions(view)).toEqual(['0', '0', '0', '0']);
  });

  it('labels measured counts as lower bounds when legacy and unknown rows coexist', () => {
    const mix = aggregateTokenMix([
      { ...measured, unknown_tokens: 1 },
      { tokens: 1000, unknown_tokens: 0 },
      { unknown_tokens: 2 },
    ]);
    expect(mix.parts).toEqual(parts);
    expect(mix.measuredTokens).toBe(100);
    expect(mix.unknownRequests).toBe(3);
    expect(tokenMixText(mix)).toBe(
      'Input: ≥ 50 · Output: ≥ 25 · Cache read: ≥ 20 · Cache write: ≥ 5 · Partial token breakdown',
    );
    const view = tokenMixView(mix);
    expect(view).toHaveTextContent('Measured breakdown only');
    expect(view).toHaveTextContent('Some rows lack a usable breakdown');
    expect(view).toHaveTextContent('3 requests have incomplete token data');
    expect(view.querySelector<HTMLElement>('.timeline-token-strip > span')?.style.width).toBe(
      '50%',
    );
  });

  it.each([
    { ...measured, token_parts: { input: 50 } as TimelineTokenParts },
    { ...measured, token_parts: { ...parts, input: -1 } },
    { ...measured, token_parts: { ...parts, input: 0.5 } },
    { ...measured, token_parts: { ...parts, input: Number.MAX_SAFE_INTEGER + 1 } },
    { token_parts: { ...parts, input: Number.MAX_SAFE_INTEGER }, unknown_tokens: 0 },
    { ...measured, tokens: 101 },
  ])('does not use malformed or inconsistent breakdowns: %j', (row) => {
    const mix = aggregateTokenMix([row, measured]);
    expect(mix.parts).toEqual(parts);
    expect(mix.measuredTokens).toBe(100);
    expect(mix.missingBreakdown).toBe(true);
  });

  it('accepts the safe integer boundary but never renders a rounded aggregate', () => {
    const max = Number.MAX_SAFE_INTEGER;
    const maxRow = { tokens: max, token_parts: { ...zero, input: max }, unknown_tokens: 0 };
    expect(aggregateTokenMix([maxRow]).measuredTokens).toBe(max);
    for (const rows of [
      [maxRow, measured],
      [measured, maxRow, measured],
    ]) {
      const mix = aggregateTokenMix(rows);
      expect(mix.parts).toBeUndefined();
      expect(mix.measuredTokens).toBeUndefined();
      expect(tokenMixText(mix)).toContain('count too large');
      expect(definitions(tokenMixView(mix))).toEqual(Array(4).fill('Not measured'));
    }
  });

  it('retains safe token parts when the incomplete request count itself is unsafe', () => {
    const mix = aggregateTokenMix([
      { ...measured, unknown_tokens: Number.MAX_SAFE_INTEGER },
      { unknown_tokens: 1 },
      measured,
    ]);
    expect(mix.unknownRequests).toBeUndefined();
    expect(mix.measuredTokens).toBe(200);
    expect(tokenMixText(mix)).toContain('Input: ≥ 100');
    expect(tokenMixView(mix)).toHaveTextContent('Incomplete request count unavailable');
  });
});
