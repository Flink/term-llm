import { afterEach, expect, it, vi } from 'vitest';
import { defaultTimelinePrefs, readTimelinePrefs, writeTimelinePrefs } from './timeline-prefs';

afterEach(() => {
  vi.restoreAllMocks();
  window.history.replaceState(null, '', '/');
  window.localStorage.clear();
});

it('keeps a bookmark self-contained without adding history entries or losing navigation state', () => {
  window.history.replaceState({ proxyNavigationIndex: 4 }, '', '/_proxy/?other=keep#chart');
  const length = window.history.length;
  const prefs = { metric: 'tokens', group: 'client', range: 'today', currency: 'AUD' } as const;
  writeTimelinePrefs(prefs);
  expect(window.history.length).toBe(length);
  expect(window.history.state).toEqual({ proxyNavigationIndex: 4 });
  expect(window.location.hash).toBe('#chart');
  const url = new URL(window.location.href);
  expect(Object.fromEntries(url.searchParams)).toEqual({ ...prefs, other: 'keep' });
  window.localStorage.clear();
  expect(readTimelinePrefs()).toEqual(prefs);
});

it('uses saved filters on bare Home but not for fields omitted by a shared link', () => {
  const prefs = { metric: 'tokens', group: 'client', range: 'today', currency: 'CAD' } as const;
  writeTimelinePrefs(prefs);
  window.history.replaceState(null, '', '/_proxy/');
  expect(readTimelinePrefs()).toEqual(prefs);
  window.history.replaceState(null, '', '/_proxy/?range=7d');
  expect(readTimelinePrefs()).toEqual({ ...defaultTimelinePrefs, range: '7d' });
  writeTimelinePrefs(defaultTimelinePrefs);
  expect(new URL(window.location.href).searchParams.has('currency')).toBe(false);
  window.history.replaceState(null, '', '/_proxy/');
  expect(readTimelinePrefs()).toEqual(defaultTimelinePrefs);
});

it('validates URL filters and still restores from the URL when storage is unavailable', () => {
  vi.spyOn(window, 'localStorage', 'get').mockImplementation(() => {
    throw new Error('disabled');
  });
  window.history.replaceState(null, '', '/_proxy/?group=wrong&range=6m&metric=counts&currency=123');
  expect(readTimelinePrefs()).toEqual(defaultTimelinePrefs);
  window.history.replaceState(null, '', '/_proxy/');
  expect(readTimelinePrefs()).toEqual(defaultTimelinePrefs);
  const prefs = { ...defaultTimelinePrefs, group: 'provider' } as const;
  writeTimelinePrefs(prefs);
  expect(readTimelinePrefs()).toEqual(prefs);
});
