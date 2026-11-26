import { expect, it } from 'vitest';
import parity from '../../../internal/emoji/testdata/search_parity.json';
import { searchEmoji } from './emoji-search';

// internal/emoji's TestSearchParityFixture writes these from the terminal
// composer's Search, so both composers rank the shipped catalog alike.
it.each(parity)('ranks $query like the terminal composer', ({ query, results }) => {
  expect(searchEmoji(query).map(({ glyph, shortcode }) => [glyph, shortcode])).toEqual(results);
});
