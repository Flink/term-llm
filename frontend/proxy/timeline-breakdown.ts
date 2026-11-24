import type { TimelineSeries } from '../src/api/proxy-timeline';
import { el } from './dom';

export interface ShareRow {
  key: string;
  label: string;
  /** Sum of measured buckets; undefined when no bucket was measured. */
  amount?: number;
  unknown: number;
  estimated: boolean;
  series: TimelineSeries[];
}

/** Per-series totals across the range, largest first and unmeasured last. */
export function shareRows(
  seriesList: TimelineSeries[],
  value: (series: TimelineSeries) => number | undefined,
  unknown: (series: TimelineSeries) => number,
  estimated: (series: TimelineSeries) => boolean,
): ShareRow[] {
  const rows = new Map<string, ShareRow>();
  for (const series of seriesList) {
    const row = rows.get(series.key) ?? {
      key: series.key,
      label: series.label,
      unknown: 0,
      estimated: false,
      series: [] as TimelineSeries[],
    };
    const amount = value(series);
    if (amount !== undefined) row.amount = (row.amount ?? 0) + amount;
    row.series.push(series);
    row.unknown += unknown(series);
    row.estimated ||= estimated(series);
    rows.set(series.key, row);
  }
  return [...rows.values()].sort(
    (a, b) => (b.amount ?? -1) - (a.amount ?? -1) || a.label.localeCompare(b.label),
  );
}

interface BreakdownInteraction {
  hint: string;
  action: (row: ShareRow) => { label: string; run: () => void; selected?: boolean };
}

export const breakdownLimit = 8;

let descriptionSequence = 0;

export function breakdownView(
  rows: ShareRow[],
  title: string,
  period: string,
  colour: (key: string) => string,
  format: (row: ShareRow) => string,
  expanded: boolean,
  note?: (row: ShareRow) => string,
  interaction?: BreakdownInteraction,
): HTMLElement {
  const total = rows.reduce((n, row) => n + (row.amount ?? 0), 0);
  const largest = rows[0]?.amount ?? 0;
  const heading = el('h3', el('span', title), el('span', period));
  const hint = interaction?.hint ? el('p', interaction.hint) : undefined;
  if (hint) {
    hint.className = 'timeline-breakdown-hint';
    hint.id = `timeline-breakdown-hint-${++descriptionSequence}`;
  }
  function item(row: ShareRow) {
    const swatch = el('i');
    swatch.className = 'timeline-swatch';
    swatch.style.backgroundColor = colour(row.key);
    const name = el('span', row.label);
    name.className = 'timeline-breakdown-name';
    const share = el(
      'span',
      row.amount !== undefined && total > 0 ? `${Math.round((row.amount / total) * 100)}%` : '',
    );
    share.className = 'timeline-breakdown-share';
    if (share.textContent) share.title = 'Share of measured total';
    const meter = el('span');
    meter.className = 'timeline-breakdown-meter';
    if (row.amount !== undefined && largest > 0) {
      const fill = el('span');
      fill.style.width = `${(row.amount / largest) * 100}%`;
      fill.style.backgroundColor = colour(row.key);
      meter.append(fill);
    }
    meter.setAttribute('aria-hidden', 'true');
    const amount = el('b', format(row));
    const content = [swatch, name, amount, share, meter];
    const detail = note ? el('small', note(row)) : undefined;
    if (detail) {
      detail.className = 'timeline-breakdown-note';
      content.push(detail);
    }
    const li = el('li');
    if (!interaction) {
      li.className = 'timeline-breakdown-row';
      li.append(...content);
      return li;
    }

    const action = interaction.action(row);
    const glyph = action.selected ? el('span', '×') : el('span');
    glyph.className = action.selected ? 'timeline-breakdown-cross' : 'timeline-chevron';
    glyph.setAttribute('aria-hidden', 'true');
    const affordance = el('span', glyph);
    affordance.className = 'timeline-breakdown-affordance';
    const target = el('button', ...content, affordance);
    target.type = 'button';
    target.className = 'timeline-breakdown-row timeline-breakdown-action';
    target.dataset.seriesKey = row.key;
    target.setAttribute('aria-label', action.label);
    if (action.selected !== undefined) target.setAttribute('aria-pressed', String(action.selected));
    // An explicit action label replaces the row contents as the button's name.
    // Keep the metrics available as its accessible description.
    const id = `timeline-breakdown-row-${++descriptionSequence}`;
    amount.id = `${id}-amount`;
    const descriptions = [amount.id];
    if (share.textContent) {
      share.id = `${id}-share`;
      descriptions.push(share.id);
    }
    if (detail?.textContent) {
      detail.id = `${id}-note`;
      descriptions.push(detail.id);
    }
    target.setAttribute('aria-describedby', descriptions.join(' '));
    target.addEventListener('click', action.run);
    li.append(target);
    return li;
  }
  const view = el(
    'aside',
    heading,
    ...(hint ? [hint] : []),
    el('ul', ...rows.slice(0, breakdownLimit).map(item)),
  );
  if (hint) view.setAttribute('aria-describedby', hint.id);
  if (rows.length > breakdownLimit) {
    const more = el('details', el('summary', `${rows.length - breakdownLimit} more`));
    function populate() {
      if (more.open && more.childElementCount === 1) {
        const list = el('ul');
        for (let i = breakdownLimit; i < rows.length; i++) list.append(item(rows[i]!));
        more.append(list);
      }
    }
    more.open = expanded;
    populate();
    more.addEventListener('toggle', populate);
    view.append(more);
  }
  view.className = 'timeline-breakdown';
  view.setAttribute('aria-label', title);
  return view;
}
