package emoji

import (
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"testing"
)

// Exercise the real cell decoder with the shipped Unicode catalog, not just the
// popup's width helper. Verify cell positions against the terminal's |emoji|X probe.
func TestWholeCatalogDrawsAsWideGlyphs(t *testing.T) {
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		screen := uv.NewScreenBuffer(5, 1)
		screen.Method = method
		screen.WideEmoji = IsGlyph
		for _, entry := range index().entries {
			uv.NewStyledString("|\x1b[33m"+entry.Glyph+"\x1b[0m|X").Draw(screen, screen.Bounds())
			c := screen.CellAt(1, 0)
			if c == nil || c.Content != entry.Glyph || c.Width != 2 || screen.CellAt(3, 0).Content != "|" || screen.CellAt(4, 0).Content != "X" {
				t.Fatalf("mode %d: %q (%s) did not occupy exactly two cells: %+v", method, entry.Name, entry.Glyph, c)
			}
		}
	}
}
