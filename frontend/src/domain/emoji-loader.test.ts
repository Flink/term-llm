import { expect, it, vi } from 'vitest';
import { loadEmojiSearch } from './emoji-completion';

const chunk = vi.hoisted(() => ({ fail: true }));

vi.mock('./emoji-search', () => {
  if (chunk.fail) throw new Error('emoji chunk unavailable');
  return { searchEmoji: () => [{ glyph: '🚀', shortcode: 'rocket' }] };
});

it('retries the deferred catalog after a failed chunk load and then shares it', async () => {
  await expect(loadEmojiSearch()).rejects.toThrow();
  chunk.fail = false;
  vi.resetModules();
  const search = await loadEmojiSearch();
  expect(search('rock')).toEqual([{ glyph: '🚀', shortcode: 'rocket' }]);
  expect(loadEmojiSearch()).toBe(loadEmojiSearch());
  await expect(loadEmojiSearch()).resolves.toBe(search);
});
