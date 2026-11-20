import type { TimelineSeries } from '../src/api/proxy-timeline';
import { button, el } from './dom';

const hoverLimit = 20;

export interface TooltipDetail {
  series: TimelineSeries;
  amount: string;
  notes: string[];
}

export function timelineTooltip(dismiss: () => void) {
  const view = el('div');
  view.className = 'timeline-tooltip';
  view.hidden = true;
  const label = el('strong');
  const close = button('×', dismiss);
  close.className = 'timeline-tooltip-close';
  close.setAttribute('aria-label', 'Dismiss bucket details');
  const heading = el('div', label, close);
  heading.className = 'timeline-tooltip-heading';
  const content = el('div');
  content.role = 'status';
  const count = el('small');
  const totalValue = el('b');
  const totalRow = el('div', el('span', 'Total'), totalValue);
  totalRow.className = 'timeline-tooltip-total';

  function show(
    plot: HTMLElement,
    target: SVGGElement,
    date: string,
    ranked: TimelineSeries[],
    detail: (series: TimelineSeries) => TooltipDetail,
    colour: (key: string) => string,
    total?: string,
  ) {
    label.textContent = date;
    if (view.hidden) view.append(heading, count, content, totalRow);
    count.hidden = ranked.length <= hoverLimit;
    count.textContent = count.hidden ? '' : `Top ${hoverLimit} of ${ranked.length}`;
    totalRow.hidden = total === undefined;
    totalValue.textContent = total ?? '';
    view.classList.toggle('timeline-tooltip-wide', ranked.length > 10 && plot.clientWidth >= 620);
    view.hidden = false;
    function entry(source: TimelineSeries) {
      const { series, amount, notes } = detail(source);
      const item = el('div');
      item.className = 'timeline-tooltip-item';
      const swatch = el('i');
      swatch.className = 'timeline-swatch';
      swatch.style.backgroundColor = colour(series.key);
      const row = el('div', swatch, el('span', series.label), el('b', amount));
      row.className = 'timeline-tooltip-row';
      item.append(row);
      if (notes.length) {
        const note = el('div', notes.join(' · '));
        note.className = 'timeline-tooltip-note';
        item.append(note);
      }
      return item;
    }
    const list = el('div');
    list.className = 'timeline-tooltip-list';
    for (const series of ranked.slice(0, hoverLimit)) list.append(entry(series));
    content.replaceChildren(list);
    if (!ranked.length) list.append(el('span', 'No activity'));
    if (ranked.length > hoverLimit) {
      const remaining = ranked.length - hoverLimit;
      const more = el(
        'details',
        el('summary', `${remaining} more ${remaining === 1 ? 'contributor' : 'contributors'}`),
      );
      more.addEventListener('toggle', () => {
        if (!more.open || more.childElementCount > 1) return;
        const rest = el('div');
        rest.className = 'timeline-tooltip-list';
        for (let i = hoverLimit; i < ranked.length; i++) rest.append(entry(ranked[i]!));
        more.append(rest);
      });
      content.append(more);
    }
    content.scrollTop = 0;
    // Don't cover the initiating bar: the card must not steal its hover or tap.
    const bounds = plot.getBoundingClientRect();
    const slot = target.getBoundingClientRect();
    const width = view.offsetWidth;
    const right = slot.right - bounds.left;
    const left = slot.left - bounds.left - width;
    const beside = right + width <= bounds.width || left >= 0;
    view.classList.toggle('timeline-tooltip-below', !beside);
    view.style.left = `${right + width <= bounds.width ? right : Math.max(0, left)}px`;
  }

  return {
    view,
    show,
    hide() {
      view.hidden = true;
      view.replaceChildren();
    },
  };
}
