import type { EmojiCatalog } from './emoji-catalog';

export function activeEmojiAtCaret(value: string, caret: number) {
  const prefix = value.slice(0, caret);
  const match = /:([a-z0-9_+-]{2,})$/i.exec(prefix);
  if (!match) return null;
  // Match emoji endings, not the Emoji property alone: plain digits, # and * also have it.
  const boundary =
    /(?:\s|\p{Extended_Pictographic}\uFE0F?|\p{Emoji_Modifier}|\p{Regional_Indicator}{2}|[#*0-9]\uFE0F?\u20E3|\u{1F3F4}[\u{E0020}-\u{E007E}]+\u{E007F})$/u;
  if (match.index > 0 && !boundary.test(prefix.slice(0, match.index))) return null;
  const query = match[1].toLowerCase();
  // A shortcode can continue to the right of the caret and end in a colon.
  const suffix = /^[a-z0-9_+-]*:?/i.exec(value.slice(caret))![0];
  return { query, start: match.index, end: caret + suffix.length };
}

export function indexEmoji(catalog: EmojiCatalog) {
  return catalog.map(([glyph, name, keywords, aliases]) => {
    const normalized =
      name
        .toLowerCase()
        .match(/[a-z0-9]+/g)
        ?.join('_') ?? '';
    const codes = aliases ? aliases.split(' ') : [];
    return { glyph, normalized, keywords, codes, shortcode: codes[0] ?? normalized };
  });
}

export type EmojiIndex = ReturnType<typeof indexEmoji>;

type Entry = EmojiIndex[number];

function matchRank(entry: Entry, query: string) {
  const { normalized, codes, keywords } = entry;
  if (normalized === query || codes.includes(query)) return 0;
  if (normalized.startsWith(query) || codes.some((code) => code.startsWith(query))) return 1;
  if (normalized.includes(`_${query}`)) return 2;
  if (keywords.startsWith(query) || keywords.includes(` ${query}`)) return 3;
  return -1;
}

export function searchEmoji(index: EmojiIndex, query: string) {
  const limit = 8;
  const ranks: EmojiIndex[] = [[], [], [], []];
  for (const entry of index) {
    const rank = matchRank(entry, query);
    if (rank === -1 || ranks[rank].length === limit) continue;
    ranks[rank].push(
      rank === 0 && entry.codes.includes(query) ? { ...entry, shortcode: query } : entry,
    );
  }
  return ranks.flat().slice(0, limit);
}
