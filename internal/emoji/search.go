// Package emoji searches the English Unicode emoji catalog for composer completions.
package emoji

import (
	_ "embed"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

//go:generate python3 generate.py

//go:embed catalog.json
var catalog []byte

// Entry contains the CLDR short name, fully qualified glyph and preferred shortcode.
type Entry struct{ Name, Glyph, Shortcode string }

type indexedEntry struct {
	Entry
	normalized, keywords string
	aliases              []string
}

var index = sync.OnceValue(func() catalogIndex {
	var rows [][4]string
	if err := json.Unmarshal(catalog, &rows); err != nil {
		panic(err) // Invalid build-time asset, not user input.
	}
	idx := catalogIndex{entries: make([]indexedEntry, 0, len(rows)), glyphs: make(map[string]int, len(rows))}
	for _, row := range rows {
		words := strings.FieldsFunc(strings.ToLower(row[1]), func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
		})
		name := strings.Join(words, "_")
		aliases := strings.Fields(row[3])
		code := name
		if len(aliases) > 0 {
			code = aliases[0]
		}
		idx.entries = append(idx.entries, indexedEntry{Entry{row[1], row[0], code}, name, row[2], aliases})
		idx.glyphs[row[0]] = uniseg.StringWidth(row[0])
		idx.maxBytes = max(idx.maxBytes, len(row[0]))
	}
	return idx
})

type catalogIndex struct {
	entries  []indexedEntry
	glyphs   map[string]int
	maxBytes int
}

// CellWidth returns the width of a catalog glyph, or zero for unknown clusters.
// Match the grapheme widths Bubbles uses for wrapping and cursor placement.
func CellWidth(cluster string) int {
	if len(cluster) < 2 {
		return 0
	}
	return index().glyphs[cluster]
}

// EndsWithGlyph recognizes a complete Unicode emoji immediately before a colon.
// Search only rune-aligned suffixes, bounded by the longest catalog sequence.
func EndsWithGlyph(text string) bool {
	if len(text) == 0 || text[len(text)-1] < utf8.RuneSelf {
		return false
	}
	idx := index()
	for i := len(text); i > 0; {
		_, n := utf8.DecodeLastRuneInString(text[:i])
		i -= n
		if len(text)-i > idx.maxBytes {
			break
		}
		if _, ok := idx.glyphs[text[i:]]; ok {
			return true
		}
	}
	return false
}

func (e indexedEntry) matchRank(query, word, keyword string) int {
	rank := 4
	switch {
	case e.normalized == query:
		return 0
	case strings.HasPrefix(e.normalized, query):
		rank = 1
	case strings.Contains(e.normalized, word):
		rank = 2
	case strings.HasPrefix(e.keywords, query) || strings.Contains(e.keywords, keyword):
		rank = 3
	}
	for _, alias := range e.aliases {
		if alias == query {
			return 0
		}
		if strings.HasPrefix(alias, query) {
			rank = min(rank, 1)
		}
	}
	return rank
}

// Search matches shortcodes, English names and keywords, preferring exact names and
// name prefixes. Ties retain Unicode's keyboard/display order.
func Search(query string, limit int) []Entry {
	if query == "" || limit <= 0 {
		return nil
	}
	query = strings.ToLower(query)
	word, keyword := "_"+query, " "+query
	var ranks [4][]Entry
	for _, e := range index().entries {
		rank := e.matchRank(query, word, keyword)
		if rank >= len(ranks) {
			continue
		}
		if len(ranks[rank]) < limit {
			entry := e.Entry
			// Keep the spelling the user picked when it is an exact alternate alias.
			if rank == 0 && slices.Contains(e.aliases, query) {
				entry.Shortcode = query
			}
			ranks[rank] = append(ranks[rank], entry)
		}
	}
	var result []Entry
	for _, group := range ranks {
		result = append(result, group[:min(len(group), limit-len(result))]...)
	}
	return result
}
