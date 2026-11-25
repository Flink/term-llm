package composer

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/term-llm/term-llm/internal/emoji"
	"github.com/term-llm/term-llm/internal/ui"
)

func TestEmojiViewFramedColumns(t *testing.T) {
	p := EmojiPopup{matches: []emoji.Entry{{Shortcode: "rocket", Glyph: "🚀"}, {Shortcode: "rosette", Glyph: "🏵️"}, {Shortcode: "polar_bear", Glyph: "🐻‍❄️"}, {Shortcode: "metal", Glyph: "🤘"}, {Shortcode: "metal_light", Glyph: "🤘🏻"}, {Shortcode: "victory_light", Glyph: "✌🏻"}}, selected: 1}
	theme := ui.DefaultTheme()
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		view := p.View(theme, 80, 24)
		lines := strings.Split(ansi.Strip(view), "\n")
		if len(lines) != len(p.matches)+3 || !strings.HasPrefix(lines[0], "╭") || !strings.HasSuffix(lines[len(lines)-1], "╯") {
			t.Fatalf("missing frame: %q", lines)
		}
		panelWidth := method.StringWidth(lines[0])
		labelColumn := -1
		for i, e := range p.matches {
			// Measure the preview with the same grapheme width as the textarea,
			// independently of the popup's sizing code.
			placeholder := strings.Repeat("X", uniseg.StringWidth(e.Glyph))
			row := strings.Replace(lines[i+1], e.Glyph, placeholder, 1)
			prefix, _, ok := strings.Cut(row, ":"+e.Shortcode+":")
			if !ok || !strings.Contains(prefix, placeholder) || !strings.HasSuffix(row, "│") {
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
			for _, line := range strings.Split(ansi.Strip(p.View(theme, w, 24)), "\n") {
				for i := len(p.matches) - 1; i >= 0; i-- {
					line = strings.ReplaceAll(line, p.matches[i].Glyph, strings.Repeat("X", uniseg.StringWidth(p.matches[i].Glyph)))
				}
				if n := method.StringWidth(line); n > w {
					t.Fatalf("method %d: width %d overflow: %q", method, w, line)
				}
			}
		}
	}
}
