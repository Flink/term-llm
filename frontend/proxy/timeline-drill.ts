import type { ChartGroup, TimelineScope } from '../src/api/proxy-timeline';
import type { ShareRow } from './timeline-breakdown';

export type ScopeGroup = Exclude<ChartGroup, 'total'>;
export interface ScopeStep {
  group: ScopeGroup;
  key: string;
  label: string;
}

export const scopeRoots: Record<ScopeGroup, string> = {
  client: 'All clients',
  provider: 'All providers',
  model: 'All models',
};

export function scopeQuery(steps: ScopeStep[]): TimelineScope | undefined {
  if (!steps.length) return undefined;
  return Object.fromEntries(steps.map(({ group, key }) => [group, key]));
}

export function scopeHint(group: ChartGroup, selected: boolean): string {
  if (group === 'total') return 'Select Total to see the model breakdown.';
  if (selected) return `Select the ${group} again to clear its filter.`;
  if (group === 'model') return 'Select a model to show only its usage.';
  return `Select a ${group} to see its models.`;
}

export function scopeAction(group: ChartGroup, selected: boolean, row: ShareRow): string {
  if (group === 'total') return `${row.label}: show models`;
  if (selected) return `${row.label}: clear ${group} filter`;
  if (group === 'model') return `${row.label}: show only this model`;
  return `${row.label}: show models`;
}
