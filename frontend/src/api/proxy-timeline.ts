export type ChartRange = 'today' | '7d' | '30d';
export type ChartGroup = 'total' | 'client' | 'provider' | 'model';

/** Disjoint, normalized components of measured tokens; counted exactly once in `tokens`. */
export interface TimelineTokenParts {
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
}

export interface TimelineSeries {
  key: string;
  label: string;
  costs: { currency: string; micros: number; estimated: boolean; plan_estimate_micros?: number }[];
  tokens?: number;
  /** Absent for legacy totals or unmeasured usage, not equivalent to four zeros. */
  token_parts?: TimelineTokenParts;
  unknown_cost: number;
  unknown_tokens: number;
  plan: number;
}
export interface TimelineBucket {
  start: string;
  end: string;
  label: string;
  series: TimelineSeries[];
}
export interface MoneyTimeline {
  timezone: string;
  range: ChartRange;
  start: string;
  end: string;
  currencies: string[];
  buckets: TimelineBucket[];
  /** Totals for the equal-length window before `start`; absent without covering history. */
  previous?: TimelineSeries;
}
