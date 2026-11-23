import { fireEvent, getByRole, queryByRole } from '@testing-library/preact';
import { afterEach, expect, it, vi } from 'vitest';
import { toastRegion } from './toast';

afterEach(() => vi.useRealTimers());

it('shows only the latest notification and gives it a full four seconds', () => {
  vi.useFakeTimers();
  const { element, toast } = toastRegion();
  toast('Copied', 'success');
  expect(getByRole(element, 'status')).toHaveTextContent('Copied');

  vi.advanceTimersByTime(3000);
  toast('Clipboard unavailable', 'error');
  expect(queryByRole(element, 'status')).toBeNull();
  vi.advanceTimersByTime(1000);
  expect(getByRole(element, 'alert')).toHaveTextContent('Clipboard unavailable');
  vi.advanceTimersByTime(3000);
  expect(element).toBeEmptyDOMElement();
});

it('can be dismissed before the timeout', () => {
  vi.useFakeTimers();
  const { element, toast } = toastRegion();
  toast('Copied', 'success');
  fireEvent.click(getByRole(element, 'button', { name: 'Dismiss notification' }));
  expect(element).toBeEmptyDOMElement();
  expect(vi.getTimerCount()).toBe(0);
});
