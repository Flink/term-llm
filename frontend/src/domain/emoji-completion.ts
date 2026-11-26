import type { EmojiSearch } from './emoji-search';

// Match emoji endings, not the Emoji property alone: plain digits, # and * also have it.
const emojiBoundary =
  /(?:\s|\p{Extended_Pictographic}\uFE0F?|\p{Emoji_Modifier}|\p{Regional_Indicator}{2}|[#*0-9]\uFE0F?\u20E3|\u{1F3F4}[\u{E0020}-\u{E007E}]+\u{E007F})$/u;

/**
 * Finds a `:shortcode` ending at the caret. Suggestions need two characters;
 * a shorter minimum lets callers start loading the catalog at the colon.
 */
export function activeEmojiAtCaret(value: string, caret: number, minLength = 2) {
  const prefix = value.slice(0, caret);
  const match = /:([a-z0-9_+-]*)$/i.exec(prefix);
  if (!match || match[1].length < minLength) return null;
  if (match.index > 0 && !emojiBoundary.test(prefix.slice(0, match.index))) return null;
  const query = match[1].toLowerCase();
  // A shortcode can continue to the right of the caret and end in a colon.
  const suffix = /^[a-z0-9_+-]*:?/i.exec(value.slice(caret))![0];
  return { query, start: match.index, end: caret + suffix.length };
}

let loading: Promise<EmojiSearch> | undefined;

/**
 * Loads the deferred catalog and its index once per page. Concurrent callers
 * share the in-flight import; a rejected chunk fetch clears the cache so a
 * later shortcode can retry instead of disabling suggestions until reload.
 */
export function loadEmojiSearch(): Promise<EmojiSearch> {
  if (!loading) {
    const promise = import('./emoji-search').then(({ searchEmoji }) => searchEmoji);
    loading = promise;
    void promise.catch(() => {
      if (loading === promise) loading = undefined;
    });
  }
  return loading;
}
