import { describe, expect, it } from 'vitest';
import { activeEmojiAtCaret } from './emoji-completion';

describe('emoji shortcode at the caret', () => {
  it.each(['', 'draft\n', '😀', '🇬🇧', '❤️', '👩‍💻', '👍🏽', '1️⃣', '#⃣'])(
    'recognizes a shortcode immediately after %j',
    (before) => {
      const prefix = `${before}:He`;
      expect(activeEmojiAtCaret(`${prefix}art:!`, prefix.length)).toEqual({
        query: 'he',
        start: before.length,
        end: prefix.length + 'art:'.length,
      });
    },
  );

  it.each(['42:he', '#:he', '*:he', 'word:he', 'https://example.org/:he', ':h', ':+'])(
    'does not activate for %j',
    (draft) => expect(activeEmojiAtCaret(draft, draft.length)).toBeNull(),
  );

  it.each(['+1', '-1', 'A_B'])('accepts %j and replaces the entire signed suffix', (prefix) => {
    const left = `say :${prefix}`;
    const token = activeEmojiAtCaret(`${left}-a+b_c2:: next`, left.length);
    expect(token).toEqual({ query: prefix.toLowerCase(), start: 4, end: left.length + 8 });
  });
});
