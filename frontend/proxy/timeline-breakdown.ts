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

export function breakdownView(
  rows: ShareRow[],
  title: string,
  period: string,
  colour: (key: string) => string,
  format: (row: ShareRow) => string,
  expanded: boolean,
  note?: (row: ShareRow) => string,
): HTMLElement {
  const total = rows.reduce((n, row) => n + (row.amount ?? 0), 0);
  const largest = rows[0]?.amount ?? 0;
  const heading = el('h3', el('span', title), el('span', period));
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
    const li = el('li', swatch, name, el('b', format(row)), share, meter);
    if (note) {
      const detail = el('small', note(row));
      detail.className = 'timeline-breakdown-note';
      li.append(detail);
    }
    return li;
  }
  const limit = 8;
  const view = el('aside', heading, el('ul', ...rows.slice(0, limit).map(item)));
  if (rows.length > limit) {
    const more = el('details', el('summary', `${rows.length - limit} more`));
    function populate() {
      if (more.open && more.childElementCount === 1) {
        const list = el('ul');
        for (let i = limit; i < rows.length; i++) list.append(item(rows[i]!));
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
