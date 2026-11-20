import type { MoneyTimeline, TimelineSeries } from '../src/api/proxy-timeline';

export function svg<K extends keyof SVGElementTagNameMap>(
  tag: K,
  attrs: Record<string, string | number>,
  text = '',
): SVGElementTagNameMap[K] {
  const node = document.createElementNS('http://www.w3.org/2000/svg', tag);
  for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, String(value));
  node.textContent = text;
  return node;
}

/** Round the axis up to 4 even steps of 1, 2, 2.5 or 5 × 10ⁿ, never below `unit`. */
export function niceScale(peak: number, unit: number): { max: number; step: number } {
  const raw = Math.max(peak, unit) / 4;
  const magnitude = 10 ** Math.floor(Math.log10(raw));
  let step = [1, 2, 2.5, 5, 10].map((m) => m * magnitude).find((s) => s >= raw) ?? raw;
  step = Math.max(unit, Math.ceil(step / unit) * unit);
  return { max: step * 4, step };
}

export interface ChartSpec {
  data: MoneyTimeline;
  width: number;
  /** Stacking order, largest first so it sits on the baseline. */
  ordered: (series: TimelineSeries[]) => TimelineSeries[];
  value: (series: TimelineSeries) => number | undefined;
  colour: (key: string) => string;
  tick: (value: number, step: number) => string;
  ariaLabel: string;
  bucketLabel: (start: string) => string;
  axisLabel: (start: string, last: boolean) => string;
  unit: number;
  uid: string;
}
export function drawChart(spec: ChartSpec): SVGSVGElement {
  const { data, width } = spec;
  const narrow = width < 600;
  const height = narrow ? 260 : 340;
  const left = narrow ? 52 : 64;
  const right = 8;
  const top = 12;
  const bottom = height - 30;
  const chart = svg('svg', { viewBox: `0 0 ${width} ${height}`, role: 'group' });
  chart.setAttribute('aria-label', spec.ariaLabel);
  const defs = svg('defs', {});
  chart.append(defs);
  const sums = data.buckets.map((bucket) =>
    bucket.series.reduce((n, series) => n + (spec.value(series) ?? 0), 0),
  );
  const { max, step } = niceScale(Math.max(...sums), spec.unit);
  const y = (v: number) => bottom - ((bottom - top) * v) / max;
  for (let tick = 0; tick <= 4; tick++) {
    const at = y(step * tick);
    chart.append(
      svg('line', {
        x1: left,
        x2: width - right,
        y1: at,
        y2: at,
        class: tick ? 'timeline-grid' : 'timeline-baseline',
      }),
      svg(
        'text',
        { x: left - 10, y: at + 4, 'text-anchor': 'end', class: 'timeline-axis' },
        spec.tick(step * tick, step),
      ),
    );
  }
  const count = data.buckets.length;
  const slot = (width - left - right) / Math.max(1, count);
  const barWidth = Math.max(2, Math.min(slot * 0.64, 42));
  const radius = Math.min(4, barWidth / 3);
  const every = Math.max(1, Math.ceil(count / (narrow ? 3 : 6)));
  data.buckets.forEach((bucket, index) => {
    const x = left + slot * index;
    const bar = svg('g', {
      role: 'button',
      tabindex: 0,
      'data-bucket': index,
      'aria-label': spec.bucketLabel(bucket.start),
    });
    // Keep time slots tappable even when they have no measurable value.
    bar.append(
      svg('rect', { x, y: top, width: slot, height: bottom - top, class: 'timeline-hit' }),
    );
    const segments = spec.ordered(bucket.series).flatMap((series) => {
      const amount = spec.value(series);
      return amount !== undefined && amount > 0 ? [{ series, amount }] : [];
    });
    if (segments.length) {
      const clip = `${spec.uid}-${index}`;
      const total = segments.reduce((n, s) => n + s.amount, 0);
      const barX = x + (slot - barWidth) / 2;
      const clipPath = svg('clipPath', { id: clip });
      clipPath.append(
        svg('rect', {
          x: barX,
          y: y(total),
          width: barWidth,
          height: bottom - y(total) + radius,
          rx: radius,
        }),
      );
      defs.append(clipPath);
      const stack = svg('g', { class: 'timeline-stack', 'clip-path': `url(#${clip})` });
      let base = bottom;
      for (const { series, amount } of segments) {
        const h = (bottom - top) * (amount / max);
        base -= h;
        const gap = segments.length > 1 && h > 3 ? 1 : 0;
        stack.append(
          svg('rect', {
            x: barX,
            y: base,
            width: barWidth,
            height: h - gap,
            fill: spec.colour(series.key),
          }),
        );
      }
      bar.append(stack);
    }
    chart.append(bar);
    const last = index === count - 1;
    if (last || (index % every === 0 && count - 1 - index >= every / 2))
      chart.append(
        svg(
          'text',
          { x: x + slot / 2, y: height - 8, 'text-anchor': 'middle', class: 'timeline-axis' },
          spec.axisLabel(bucket.start, last),
        ),
      );
  });
  return chart;
}
