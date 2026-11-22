import type { ProxyUsageWindow } from '../src/api/proxy-client';

export const usageMinute = 60_000;
const weekMinutes = 7 * 24 * 60;

export function usageTimestamp(value?: string): number | undefined {
  const time = value ? Date.parse(value) : NaN;
  // Go's zero time is also used to represent an absent reset.
  return Number.isFinite(time) && time > Date.parse('0001-01-01T00:00:00Z') ? time : undefined;
}

export function usagePercent(window: ProxyUsageWindow): number | undefined {
  if (window.percent_unknown || window.disabled || !Number.isFinite(window.used_percent)) return;
  return Math.min(100, Math.max(0, window.used_percent));
}

export function formatUsageDuration(ms: number): string {
  const minutes = Math.round(ms / usageMinute);
  if (minutes < 1) return '<1m';
  if (minutes >= 1440) {
    const hours = Math.round(ms / (60 * usageMinute));
    const days = Math.floor(hours / 24);
    return `${days}d${hours % 24 ? ` ${hours % 24}h` : ''}`;
  }
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return [hours && `${hours}h`, rest && `${rest}m`].filter(Boolean).join(' ');
}

export function usageWindowLabel(window: ProxyUsageWindow): string {
  const label = window.label?.trim().replace(/ window$/i, '');
  if (label) {
    return label.replace(
      /^(\d+) (week|day|hour|minute)s?$/,
      (_, count: string, unit: string) => `${count} ${unit}${count === '1' ? '' : 's'}`,
    );
  }
  const minutes = window.duration_minutes;
  if (!(minutes > 0)) return 'Window';
  for (const [size, unit] of [
    [weekMinutes, 'week'],
    [1440, 'day'],
    [60, 'hour'],
    [1, 'minute'],
  ] as const) {
    if (minutes % size === 0) {
      const count = minutes / size;
      return `${count} ${unit}${count === 1 ? '' : 's'}`;
    }
  }
  return 'Window';
}

interface WindowTiming {
  resetAt?: number;
  expired?: boolean;
  pacePercent?: number;
  forecast?: { kind: 'left' | 'early' | 'quiet'; text: string };
}

export function usageWindowTiming(
  window: ProxyUsageWindow,
  snapshotAt: number | undefined,
  now: number,
  stale = false,
): WindowTiming {
  const resetAt = usageTimestamp(window.resets_at);
  if (resetAt !== undefined && resetAt <= now) return { resetAt, expired: true };
  const timing: WindowTiming = { resetAt };
  const used = usagePercent(window);
  if (!stale && !window.disabled && (used === 100 || window.limit_reached)) {
    timing.forecast = { kind: 'early', text: 'Limit reached' };
  }
  const duration = window.duration_minutes * usageMinute;
  if (
    resetAt === undefined ||
    snapshotAt === undefined ||
    used === undefined ||
    !Number.isFinite(duration) ||
    duration <= 0
  )
    return timing;
  const remaining = resetAt - snapshotAt;
  if (remaining <= 0 || remaining > duration) return timing;
  const elapsed = duration - remaining;
  const fraction = elapsed / duration;
  timing.pacePercent = fraction * 100;
  // Mirror the CLI's providerUsageProjection eligibility. Short windows still
  // get a pace marker, but not a forecast. Compare usage to its own snapshot.
  if (timing.forecast || stale || window.duration_minutes < weekMinutes || used < 2) return timing;
  if (elapsed < Math.max(15 * usageMinute, duration * 0.15)) {
    timing.forecast = { kind: 'quiet', text: 'Too early' };
    return timing;
  }
  const projected = used / fraction;
  timing.forecast =
    projected > 100
      ? {
          kind: 'early',
          text: `Runs out ${formatUsageDuration(duration * (1 - 100 / projected))} early`,
        }
      : { kind: 'left', text: `${Math.round(100 - projected)}% left` };
  return timing;
}
