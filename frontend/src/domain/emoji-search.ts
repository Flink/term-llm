// Deferred entry: loadEmojiSearch imports this module, and with it the Unicode
// and gemoji catalog, only once a composer shortcode starts.
import catalog from '../../../internal/emoji/catalog.json';

export type EmojiCatalog = readonly (readonly [
  glyph: string,
  name: string,
  keywords: string,
  aliases?: string,
])[];

export interface EmojiMatch {
  glyph: string;
  shortcode: string;
}

export type EmojiSearch = (query: string) => EmojiMatch[];

const limit = 8;

interface Entry {
  glyph: string;
  normalized: string;
  keywords: string;
  codes: string[];
}

function matchRank({ normalized, codes, keywords }: Entry, query: string) {
  if (normalized === query || codes.includes(query)) return 0;
  if (normalized.startsWith(query) || codes.some((code) => code.startsWith(query))) return 1;
  if (normalized.includes(`_${query}`)) return 2;
  if (keywords.startsWith(query) || keywords.includes(` ${query}`)) return 3;
  return -1;
}

/**
 * Indexes a catalog once and returns its search. Mirrors internal/emoji.Search;
 * internal/emoji/testdata/search_parity.json keeps the two rankings in step.
 */
export function createEmojiSearch(rows: EmojiCatalog): EmojiSearch {
  const index = rows.map(([glyph, name, keywords, aliases]): Entry => {
    const normalized =
      name
        .toLowerCase()
        .match(/[a-z0-9]+/g)
        ?.join('_') ?? '';
    return { glyph, normalized, keywords, codes: aliases ? aliases.split(' ') : [] };
  });
  return (input) => {
    const query = input.toLowerCase();
    if (!query) return [];
    const ranks: EmojiMatch[][] = [[], [], [], []];
    for (const entry of index) {
      const rank = matchRank(entry, query);
      if (rank === -1 || ranks[rank].length === limit) continue;
      // Keep the spelling the user picked when it is an exact alternate alias.
      const shortcode =
        rank === 0 && entry.codes.includes(query) ? query : (entry.codes[0] ?? entry.normalized);
      ranks[rank].push({ glyph: entry.glyph, shortcode });
    }
    return ranks.flat().slice(0, limit);
  };
}

export const searchEmoji = createEmojiSearch(catalog as [string, string, string, string?][]);
