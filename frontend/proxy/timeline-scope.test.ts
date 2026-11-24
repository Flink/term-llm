import { getByRole, within } from '@testing-library/preact';
import { expect, it, vi } from 'vitest';
import { breakdownView, type ShareRow } from './timeline-breakdown';
import { timelineScopeView } from './timeline-scope';

const rows: ShareRow[] = Array.from({ length: 10 }, (_, i) => ({
  key: `opaque:["${i}"]:a/b`,
  label: `Model ${i}`,
  amount: 100 - i * 10,
  unknown: 0,
  estimated: false,
  series: [],
}));
const colour = () => 'blue';
const format = (row: ShareRow) => `${row.amount} tokens`;
const note = () => 'Input: 60 · Output: 40';

it('keeps the passive breakdown and metrics when no interaction is supplied', () => {
  const view = breakdownView(rows.slice(0, 2), 'By model', '7 days', colour, format, false, note);
  expect(view.querySelector('button')).toBeNull();
  expect(view.querySelector('[data-series-key]')).toBeNull();
  expect(view).toHaveTextContent('Model 0100 tokens53%');
  expect(view).toHaveTextContent(note());
  expect(view.querySelector<HTMLElement>('.timeline-breakdown-meter > span')?.style.width).toBe(
    '100%',
  );
});

it('makes the full row actionable and renders expanded rows synchronously', () => {
  const run = vi.fn();
  const hint = 'Select a model to focus the chart.';
  const view = breakdownView(rows, 'By model', '30 days', colour, format, true, note, {
    hint,
    action: (row) => ({ label: `Focus ${row.label}`, run: () => run(row.key) }),
  });
  // Keep ID-based descriptions resolvable without leaving DOM behind for other tests.
  document.createDocumentFragment().append(view);
  expect(view).toHaveAccessibleDescription(hint);
  expect(view.querySelector('details')?.open).toBe(true);
  const targets = within(view).getAllByRole('button');
  expect(targets.map((target) => target.dataset.seriesKey)).toEqual(rows.map((row) => row.key));
  const target = getByRole(view, 'button', { name: 'Focus Model 9' });
  expect(target).toHaveAttribute('type', 'button');
  expect(target).not.toHaveAttribute('aria-pressed');
  expect(target).toHaveAccessibleDescription(`10 tokens 2% ${note()}`);
  target.querySelector('small')?.click();
  expect(run).toHaveBeenCalledExactlyOnceWith(rows[9]?.key);
});

it('uses a clear glyph and pressed state when a row is selected', () => {
  const run = vi.fn();
  const view = breakdownView(
    rows.slice(0, 2),
    'By model',
    '7 days',
    colour,
    format,
    false,
    undefined,
    {
      hint: 'Select a model to toggle chart focus.',
      action: (row) => ({ label: row.label, run, selected: row === rows[0] }),
    },
  );
  const selected = getByRole(view, 'button', { name: 'Model 0', pressed: true });
  expect(selected).toHaveTextContent('×');
  selected.click();
  expect(run).toHaveBeenCalledTimes(1);
  expect(getByRole(view, 'button', { name: 'Model 1', pressed: false })).not.toHaveTextContent('×');
});

it('exposes ancestor navigation and an independently named clear action', () => {
  const root = vi.fn();
  const parent = vi.fn();
  const clear = vi.fn();
  const view = timelineScopeView(
    [
      { label: 'All clients', run: root },
      { label: 'desktop', run: parent },
    ],
    { label: 'Model <2>', clearLabel: 'Clear model filter', clear },
  );
  const container = document.createElement('div');
  container.append(view);
  const nav = getByRole(container, 'navigation', { name: 'Usage scope' });
  const trail = within(nav).getAllByRole('listitem');
  expect(trail).toHaveLength(3);
  const current = trail[2]?.querySelector('[aria-current="location"]');
  expect(current).toHaveTextContent('Model <2>');
  getByRole(nav, 'button', { name: 'All clients' }).click();
  getByRole(nav, 'button', { name: 'desktop' }).click();
  const clearButton = getByRole(nav, 'button', { name: 'Clear model filter' });
  expect(clearButton).toHaveClass('timeline-scope-clear');
  expect(clearButton).toHaveTextContent('×');
  clearButton.click();
  expect(root).toHaveBeenCalledTimes(1);
  expect(parent).toHaveBeenCalledTimes(1);
  expect(clear).toHaveBeenCalledTimes(1);
});
