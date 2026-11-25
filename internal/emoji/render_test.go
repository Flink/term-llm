package emoji

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// Exercise the real cell decoder with the shipped Unicode catalog, not just the
// popup's width helper. Verify cell positions against the terminal's |emoji|X probe.
func TestWholeCatalogDrawsWithComposerWidths(t *testing.T) {
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		screen := uv.NewScreenBuffer(5, 1)
		screen.Method = method
		screen.EmojiWidth = CellWidth
		for _, entry := range index().entries {
			uv.NewStyledString("|\x1b[33m"+entry.Glyph+"\x1b[0m|X").Draw(screen, screen.Bounds())
			c := screen.CellAt(1, 0)
			width := uniseg.StringWidth(entry.Glyph)
			if c == nil || c.Content != entry.Glyph || c.Width != width || screen.CellAt(1+width, 0).Content != "|" || screen.CellAt(2+width, 0).Content != "X" {
				t.Fatalf("mode %d: %q (%s) did not occupy %d cells: %+v", method, entry.Glyph, entry.Name, width, c)
			}
		}
	}
}
