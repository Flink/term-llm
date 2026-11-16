import { describe, expect, it } from 'vitest';
import { el, input, select } from './dom';

describe('proxy form autofill protection', () => {
  it.each([
    ['text input', () => input()],
    ['URL input', () => input('', 'url')],
    ['secret input', () => input('', 'password')],
    ['textarea', () => el('textarea')],
    ['select', () => select([['secret', 'Stored secret']])],
  ] as const)('opts %s out of browser and password-manager autofill', (_name, create) => {
    const control = create();
    expect(control).toHaveAttribute('autocomplete', 'off');
    expect(control).toHaveAttribute('data-1p-ignore', 'true');
    expect(control).toHaveAttribute('data-bwignore', 'true');
    expect(control).toHaveAttribute('data-form-type', 'other');
    expect(control).toHaveAttribute('data-lpignore', 'true');
    expect(control).toHaveAttribute('data-protonpass-ignore', 'true');
  });
});
