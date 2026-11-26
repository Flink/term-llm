import { expect, it } from 'vitest';
import { applyCompletion, completedCursor, type Completion } from './completions';

it.each(['', '!', ' next'])('inserts an emoji verbatim before %j', (suffix) => {
  const prefix = '🌍 says ';
  const token = ':woman_astronaut:';
  const draft = prefix + token + suffix;
  const completion: Completion = {
    kind: 'emoji',
    label: ':woman_astronaut:',
    description: '',
    value: '👩🏽‍🚀',
    replacement: { start: prefix.length, end: prefix.length + token.length },
  };
  expect(applyCompletion(draft, completion)).toBe(prefix + '👩🏽‍🚀' + suffix);
  expect(completedCursor(draft, completion)).toBe((prefix + '👩🏽‍🚀').length);
});

it.each([
  ['!', '@reviewer !', '@reviewer '],
  [' next', '@reviewer next', '@reviewer'],
])('keeps word completion spacing and caret before %j', (suffix, text, beforeCaret) => {
  const completion: Completion = {
    kind: 'agent',
    label: '@reviewer',
    description: 'Agent',
    value: '@reviewer',
    replacement: { start: 0, end: 3 },
  };
  const draft = '@re' + suffix;
  expect(applyCompletion(draft, completion)).toBe(text);
  expect(completedCursor(draft, completion)).toBe(beforeCaret.length);
});
