import type { ChartGroup, ChartRange } from '../src/api/proxy-timeline';

export type Metric = 'cost' | 'tokens';
export interface TimelinePrefs {
  metric: Metric;
  range: ChartRange;
  group: ChartGroup;
  /** Preferred currency; ignored when the data has none of it. */
  currency?: string;
}
export const defaultTimelinePrefs: TimelinePrefs = { metric: 'cost', range: '30d', group: 'model' };
/** Home-only query parameters; other tabs drop them. */
export const timelineQueryKeys = ['metric', 'range', 'group', 'currency'] as const;
const storageKey = 'term-llm-proxy-home-view';
const metrics: Metric[] = ['cost', 'tokens'];
const ranges: ChartRange[] = ['today', '7d', '30d'];
const groups: ChartGroup[] = ['total', 'client', 'provider', 'model'];

function pick<T extends string>(allowed: T[], value: unknown, fallback: T): T {
  return allowed.includes(value as T) ? (value as T) : fallback;
}
function parse(source: Record<string, unknown>): TimelinePrefs {
  const prefs: TimelinePrefs = {
    metric: pick(metrics, source.metric, defaultTimelinePrefs.metric),
    range: pick(ranges, source.range, defaultTimelinePrefs.range),
    group: pick(groups, source.group, defaultTimelinePrefs.group),
  };
  if (typeof source.currency === 'string' && /^[A-Z]{3}$/.test(source.currency))
    prefs.currency = source.currency;
  return prefs;
}
/**
 * The query string wins so reloads and shared links are exact; a bare Home URL
 * falls back to the last view chosen in this browser.
 */
export function readTimelinePrefs(): TimelinePrefs {
  const params = new URL(window.location.href).searchParams;
  if (timelineQueryKeys.some((key) => params.has(key)))
    return parse(Object.fromEntries(params.entries()));
  try {
    const saved: unknown = JSON.parse(window.localStorage.getItem(storageKey) ?? 'null');
    if (saved && typeof saved === 'object') return parse(saved as Record<string, unknown>);
  } catch {
    // A corrupt saved view is just the default view.
  }
  return { ...defaultTimelinePrefs };
}

/** Mirror the view into the current history entry without adding navigation steps. */
export function writeTimelinePrefs(prefs: TimelinePrefs): void {
  const url = new URL(window.location.href);
  for (const key of timelineQueryKeys) {
    const value = prefs[key];
    if (value === undefined) url.searchParams.delete(key);
    else url.searchParams.set(key, value);
  }
  if (url.href !== window.location.href) window.history.replaceState(window.history.state, '', url);
  try {
    window.localStorage.setItem(storageKey, JSON.stringify(prefs));
  } catch {
    // Private modes may refuse storage; the query string still carries the view.
  }
}
