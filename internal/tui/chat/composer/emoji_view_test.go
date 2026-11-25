package composer

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/term-llm/term-llm/internal/emoji"
	"github.com/term-llm/term-llm/internal/ui"
)

func TestEmojiViewFramedColumns(t *testing.T) {
	p := EmojiPopup{matches: []emoji.Entry{{Shortcode: "rocket", Glyph: "🚀"}, {Shortcode: "rosette", Glyph: "🏵️"}, {Shortcode: "polar_bear", Glyph: "🐻‍❄️"}, {Shortcode: "metal", Glyph: "🤘"}, {Shortcode: "metal_light", Glyph: "🤘🏻"}, {Shortcode: "victory_light", Glyph: "✌🏻"}}, selected: 1}
	theme := ui.DefaultTheme()
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		view := p.View(theme, 80)
		lines := strings.Split(ansi.Strip(view), "\n")
		if len(lines) != len(p.matches)+3 || !strings.HasPrefix(lines[0], "╭") || !strings.HasSuffix(lines[len(lines)-1], "╯") {
			t.Fatalf("missing frame: %q", lines)
		}
		panelWidth := method.StringWidth(lines[0])
		labelColumn := -1
		for i, e := range p.matches {
			// The terminal draws one wide glyph. Model that observation independently
			// of the string-width library used by the implementation.
			row := strings.Replace(lines[i+1], e.Glyph, "XX", 1)
			prefix, _, ok := strings.Cut(row, ":"+e.Shortcode+":")
			if !ok || !strings.Contains(prefix, "XX") || !strings.HasSuffix(row, "│") {
				t.Fatal(row)
			}
			if i == 0 {
				labelColumn = method.StringWidth(prefix)
			}
			if method.StringWidth(prefix) != labelColumn || method.StringWidth(row) != panelWidth || strings.Contains(prefix, "›") != (i == p.selected) {
				t.Fatalf("mode %d: glyph %s shifted label or border: %q", method, e.Glyph, row)
			}
		}
		accent := lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
		if !strings.Contains(view, accent.Render(":rosette:")) {
			t.Error("selected shortcode should use bold accent text without a background")
		}
		for w := 1; w < panelWidth; w++ {
			for _, line := range strings.Split(ansi.Strip(p.View(theme, w)), "\n") {
				for i := len(p.matches) - 1; i >= 0; i-- {
					line = strings.ReplaceAll(line, p.matches[i].Glyph, "XX")
				}
				if n := method.StringWidth(line); n > w {
					t.Fatalf("method %d: width %d overflow: %q", method, w, line)
				}
			}
		}
	}
}
