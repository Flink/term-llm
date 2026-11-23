import { fireEvent } from '@testing-library/preact';
import { afterEach, expect, it, vi } from 'vitest';
import { clipboardButton } from './clipboard';

afterEach(() => {
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});

it.each(['resolves', 'rejects'] as const)(
  'does not notify or select a fallback when clipboard %s after the panel closes',
  async (outcome) => {
    let finish!: () => void;
    const result = new Promise<void>((resolve, reject) => {
      finish = outcome === 'resolves' ? resolve : () => reject(new Error('Permission denied'));
    });
    const writeText = vi.fn(() => result);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const toast = vi.fn();
    const fallback = vi.fn();
    const copy = clipboardButton(
      'Copy',
      { toast },
      {
        read: () => 'sample',
        copied: 'Copied.',
        unavailable: 'Copy manually.',
        fallback,
      },
    );
    document.body.append(copy);
    fireEvent.click(copy);
    expect(writeText).toHaveBeenCalledWith('sample');
    copy.remove();
    finish();
    await result.catch(() => {});
    expect(toast).not.toHaveBeenCalled();
    expect(fallback).not.toHaveBeenCalled();
  },
);
