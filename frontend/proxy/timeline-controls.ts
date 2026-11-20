import type { ChartGroup, ChartRange } from '../src/api/proxy-timeline';
import { button, el, select } from './dom';
import type { Metric, TimelinePrefs } from './timeline-prefs';

export const groupingLabels: Record<ChartGroup, string> = {
  total: 'Total',
  client: 'By client',
  provider: 'By provider',
  model: 'By model',
};
export const periodLabels: Record<ChartRange, string> = {
  today: 'Today',
  '7d': '7 days',
  '30d': '30 days',
};

function segmented<T extends string>(
  label: string,
  options: [T, string][],
  choose: (value: T) => void,
) {
  const items = options.map(([value, text]) => [value, button(text, () => choose(value))] as const);
  const view = el('div', ...items.map(([, control]) => control));
  view.className = 'timeline-seg';
  view.role = 'group';
  view.setAttribute('aria-label', label);
  return {
    view,
    update: (selected: T) => {
      for (const [value, control] of items)
        control.setAttribute('aria-pressed', String(value === selected));
    },
    focus: (selected: T) => items.find(([value]) => value === selected)?.[1].focus(),
  };
}

function pick(caption: string, name: string, control: HTMLSelectElement): HTMLLabelElement {
  const text = el('span', caption);
  text.setAttribute('aria-hidden', 'true');
  control.setAttribute('aria-label', name);
  const view = el('label', text, control);
  view.className = 'timeline-pick';
  return view;
}

export function timelineControls(
  change: (prefs: Partial<TimelinePrefs>) => void,
  resetView: () => void,
) {
  const metric = segmented<Metric>(
    'Metric',
    [
      ['cost', 'Cost'],
      ['tokens', 'Tokens'],
    ],
    (metric) => change({ metric }),
  );
  const range = segmented<ChartRange>(
    'Range',
    [
      ['today', 'Today'],
      ['7d', '7d'],
      ['30d', '30d'],
    ],
    (range) => change({ range }),
  );
  const group = select([
    ['total', 'Total'],
    ['client', 'Client'],
    ['provider', 'Provider'],
    ['model', 'Model'],
  ]);
  const currency = select([]);
  const currencyPick = pick('Currency', 'Currency', currency);
  const reset = button('Reset view', resetView);
  reset.className = 'timeline-reset';
  const view = el(
    'div',
    reset,
    metric.view,
    range.view,
    pick('Split by', 'Grouping', group),
    currencyPick,
  );
  view.className = 'timeline-controls';
  group.addEventListener('change', () => change({ group: group.value as ChartGroup }));
  currency.addEventListener('change', () => change({ currency: currency.value }));
  return {
    view,
    update(
      prefs: TimelinePrefs,
      currencies: string[],
      currentCurrency: string,
      isDefault: boolean,
    ) {
      if (isDefault && document.activeElement === reset) range.focus(prefs.range);
      reset.hidden = isDefault;
      metric.update(prefs.metric);
      range.update(prefs.range);
      group.value = prefs.group;
      if (
        currency.options.length !== currencies.length ||
        currencies.some((code, i) => currency.options[i]?.value !== code)
      ) {
        currency.replaceChildren(
          ...currencies.map((code) => {
            const option = el('option', code);
            option.value = code;
            return option;
          }),
        );
      }
      currency.value = currentCurrency;
      currencyPick.hidden = prefs.metric !== 'cost' || currencies.length < 2;
    },
  };
}
