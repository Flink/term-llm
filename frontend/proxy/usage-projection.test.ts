import { describe, expect, it } from 'vitest';
import type { ProxyUsageWindow } from '../src/api/proxy-client';
import { usageMinute, usageWindowLabel, usageWindowTiming } from './usage-projection';

const snapshotAt = Date.parse('2026-11-22T02:31:00Z');
const week = 7 * 1440;
const hour = 60 * usageMinute;

function usage(duration: number, used: number, elapsed: number): ProxyUsageWindow {
  return {
    label: '',
    duration_minutes: duration,
    used_percent: used,
    resets_at: new Date(snapshotAt + (duration - elapsed) * usageMinute).toISOString(),
  };
}

describe('native usage pace and forecast', () => {
  it('compares the measurement against its snapshot, not the ticking countdown', () => {
    const codex = usage(week, 71, week - 28 * 60);
    const timing = usageWindowTiming(codex, snapshotAt, snapshotAt);
    expect(timing.pacePercent).toBeCloseTo(83.33);
    expect(timing.forecast?.text).toBe('15% left');
    expect(usageWindowTiming(codex, snapshotAt, snapshotAt + hour)).toEqual(timing);
    expect(usageWindowTiming(codex, snapshotAt, snapshotAt + 28 * hour)).toEqual({
      resetAt: snapshotAt + 28 * hour,
      expired: true,
    });
  });

  it('calculates a projected overrun, but does not predict another runout at 100% used', () => {
    const weekly = usage(week, 75, week * 0.6);
    expect(usageWindowTiming(weekly, snapshotAt, snapshotAt).forecast).toEqual({
      kind: 'early',
      text: 'Runs out 1d 10h early',
    });
    expect(
      usageWindowTiming({ ...weekly, used_percent: 100 }, snapshotAt, snapshotAt).forecast?.text,
    ).toBe('Limit reached');
  });

  it.each([
    ['short window', usage(360, 100, 180)],
    ['just opened', usage(week, 100, 0)],
    ['unknown duration', { ...usage(360, 100, 180), duration_minutes: 0 }],
    ['missing reset', { ...usage(360, 100, 180), resets_at: undefined }],
    ['explicit limit', { ...usage(week, 30, week / 2), limit_reached: true }],
    ['unknown percentage', { ...usage(360, 0, 0), percent_unknown: true, limit_reached: true }],
  ] as const)('reports observed exhaustion independently of projections: %s', (_, window) => {
    expect(usageWindowTiming(window, snapshotAt, snapshotAt).forecast?.text).toBe('Limit reached');
    expect(usageWindowTiming(window, snapshotAt, snapshotAt, true).forecast).toBeUndefined();
    const expired = { ...window, resets_at: new Date(snapshotAt).toISOString() };
    expect(usageWindowTiming(expired, snapshotAt, snapshotAt).forecast).toBeUndefined();
  });

  it('uses CLI forecast thresholds while still showing even pace on short windows', () => {
    const timing = (window: ProxyUsageWindow) => usageWindowTiming(window, snapshotAt, snapshotAt);
    expect(timing(usage(360, 15, 180))).toMatchObject({ pacePercent: 50 });
    expect(timing(usage(360, 15, 180)).forecast).toBeUndefined();
    expect(timing(usage(week, 0, 0)).pacePercent).toBe(0);
    expect(timing(usage(week, 1.99, week / 2)).forecast).toBeUndefined();
    expect(timing(usage(week, 2, week * 0.15 - 1)).forecast?.text).toBe('Too early');
    expect(timing(usage(week, 2, week * 0.15)).forecast?.text).toBe('87% left');
    const stale = usageWindowTiming(usage(week, 20, week / 2), snapshotAt, snapshotAt, true);
    expect(stale.pacePercent).toBe(50);
    expect(stale.forecast).toBeUndefined();
  });

  it('does not infer pace from an unknown or inconsistent duration, an unavailable percentage, or a missing snapshot time', () => {
    const window = usage(week, 30, week / 2);
    for (const override of [
      { duration_minutes: 0 },
      { duration_minutes: 300 },
      { percent_unknown: true },
      { disabled: true },
      { used_percent: NaN },
    ]) {
      expect(usageWindowTiming({ ...window, ...override }, snapshotAt, snapshotAt)).toEqual({
        resetAt: snapshotAt + (week / 2) * usageMinute,
      });
    }
    expect(usageWindowTiming(window, undefined, snapshotAt).pacePercent).toBeUndefined();
    for (const resets_at of [undefined, 'invalid', '0001-01-01T00:00:00Z']) {
      expect(usageWindowTiming({ ...window, resets_at }, snapshotAt, snapshotAt)).toEqual({});
    }
  });

  it('preserves distinct native window labels without fixed hour columns', () => {
    expect(usageWindowLabel(usage(300, 0, 0))).toBe('5 hours');
    expect(usageWindowLabel(usage(360, 0, 0))).toBe('6 hours');
    expect(usageWindowLabel({ ...usage(0, 0, 0), label: 'Monthly window' })).toBe('Monthly');
    expect(usageWindowLabel({ ...usage(week, 0, 0), label: '1 week window' })).toBe('1 week');
  });
});
