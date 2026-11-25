package emoji

import "testing"

func TestSearchEnglishCatalog(t *testing.T) {
	for _, tc := range []struct{ query, name, glyph string }{
		{"ROCKET", "rocket", "🚀"},
		{"polar_be", "polar bear", "🐻‍❄️"},    // Keep the full ZWJ sequence and variation selector.
		{"terrestrial", "flying saucer", "🛸"}, // English CLDR keyword, not a word in the name.
	} {
		t.Run(tc.query, func(t *testing.T) {
			got := Search(tc.query, 1)
			if len(got) != 1 || got[0].Name != tc.name || got[0].Glyph != tc.glyph {
				t.Fatalf("Search(%q, 1) = %v; want %s %s", tc.query, got, tc.glyph, tc.name)
			}
		})
	}
}

func TestSearchLimit(t *testing.T) {
	for _, tc := range []struct {
		query        string
		limit, count int
	}{{"sm", 4, 4}, {"", 8, 0}, {"heart", 0, 0}, {"not_an_emoji_name", 8, 0}} {
		if got := len(Search(tc.query, tc.limit)); got != tc.count {
			t.Errorf("Search(%q, %d) returned %d results; want %d", tc.query, tc.limit, got, tc.count)
		}
	}
}

func TestConventionalShortcodes(t *testing.T) {
	for _, tc := range []struct{ query, glyph, code string }{
		{"sweat_smile", "😅", "sweat_smile"}, {"sweat", "😓", "sweat"}, {"grinning_face_with_sweat", "😅", "sweat_smile"}, {"thumbsup", "👍", "thumbsup"}, {"-1", "👎", "-1"}, {"heart", "❤️", "heart"},
	} {
		got := Search(tc.query, 1)
		if len(got) != 1 || got[0].Glyph != tc.glyph || got[0].Shortcode != tc.code {
			t.Errorf("Search(%q) = %v; want %s :%s:", tc.query, got, tc.glyph, tc.code)
		}
	}
}

func TestEndsWithGlyph(t *testing.T) {
	for _, tail := range []string{"🚀🪨", "🐻‍❄️", "🏵️", "👨🏽‍💻", "🇳🇿", "1️⃣"} {
		if !EndsWithGlyph("Ready" + tail) {
			t.Errorf("did not recognize %q", tail)
		}
	}
	for _, tail := range []string{"", "12", "https", "#", "plain©", "word\uFE0F", "❤️."} {
		if EndsWithGlyph(tail) {
			t.Errorf("incorrectly recognized %q", tail)
		}
	}
}
