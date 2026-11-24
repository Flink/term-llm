// Package composer provides interactive emoji completion for the terminal chat editor.
package composer

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/textarea"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/term-llm/term-llm/internal/emoji"
	"github.com/term-llm/term-llm/internal/ui"
)

const emojiMatchLimit = 8

type emojiToken struct {
	start, end int // Byte offsets in the draft.
	query      string
}

// EmojiPopup tracks matches and the currently highlighted emoji.
type EmojiPopup struct {
	token    emojiToken
	matches  []emoji.Entry
	selected int
}

func emojiShortcodeChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '+' || b == '-'
}

func emojiTokenAt(value string, cursor int) (emojiToken, bool) {
	if cursor < 0 || cursor > len(value) {
		return emojiToken{}, false
	}
	start := cursor
	for start > 0 && emojiShortcodeChar(value[start-1]) {
		start--
	}
	if cursor-start < 2 || start == 0 || value[start-1] != ':' {
		return emojiToken{}, false
	}
	colon := start - 1
	if colon > 0 {
		previous, _ := utf8.DecodeLastRuneInString(value[:colon])
		if !unicode.IsSpace(previous) && !emoji.EndsWithGlyph(value[:colon]) {
			return emojiToken{}, false
		}
	}

	// Match the prefix to the left of the cursor, but replace the full
	// shortcode, including an optional closing colon, when accepted.
	end := cursor
	for end < len(value) && emojiShortcodeChar(value[end]) {
		end++
	}
	if end < len(value) && value[end] == ':' {
		end++
	}
	return emojiToken{start: colon, end: end, query: strings.ToLower(value[start:cursor])}, true
}

// Refresh updates matches for the draft and its caret. A negative cursor dismisses the popup.
func (p *EmojiPopup) Refresh(draft string, cursor int) {
	token, ok := emojiTokenAt(draft, cursor)
	if !ok {
		p.Hide()
	} else if p.token != token {
		*p = EmojiPopup{token: token, matches: emoji.Search(token.query, emojiMatchLimit)}
	}
}

// HasMatches reports whether the popup has any rows.
func (p *EmojiPopup) HasMatches() bool { return len(p.matches) > 0 }

// Active reports whether the popup's matches still correspond to the draft and caret.
func (p *EmojiPopup) Active(draft string, cursor int) bool {
	if !p.HasMatches() {
		return false
	}
	token, ok := emojiTokenAt(draft, cursor)
	return ok && token == p.token
}

// Hide dismisses the popup until the input is refreshed.
func (p *EmojiPopup) Hide() { *p = EmojiPopup{} }

// HandleKey navigates or accepts an active popup. If the draft was changed it
// returns the new caret byte offset; otherwise it returns a negative offset.
func (p *EmojiPopup) HandleKey(input *textarea.Model, key string, step int) (bool, int, error) {
	if !p.HasMatches() {
		return false, -1, nil
	}
	if step != 0 {
		count := len(p.matches)
		p.selected = (p.selected + step + count) % count
		return true, -1, nil
	}
	switch key {
	case "esc":
		p.Hide()
		return true, -1, nil
	case "tab", "enter":
		offset, err := p.accept(input)
		return true, offset, err
	default:
		return false, -1, nil
	}
}

func (p *EmojiPopup) accept(input *textarea.Model) (int, error) {
	draft := input.Value()
	token := p.token
	glyph := p.matches[p.selected].Glyph
	replacement := draft[:token.start] + glyph + draft[token.end:]
	// SetValue may truncate at the textarea's character or line limits.
	// Try on a copy (SetValue resets its backing text buffer) so a rejected
	// completion preserves the original draft, caret, and selection.
	next := *input
	next.SetValue(replacement)
	if next.Value() != replacement {
		return -1, errors.New("That emoji does not fit within the composer's text limits.")
	}
	*input = next
	p.Hide()
	return token.start + len(glyph), nil
}

// View lays out each complete catalog glyph as one wide preview. Never remeasure
// a composed glyph as part of the styled row: a legacy wcwidth can sum its runes.
func (p *EmojiPopup) View(theme *ui.Theme, width int) string {
	if width < 5 || !p.HasMatches() {
		return ""
	}
	const help = "↑↓ navigate  enter/tab insert  esc close"
	markerWidth := ansi.StringWidth("› ")
	const previewWidth = 2
	nameWidth := 0
	for _, entry := range p.matches {
		nameWidth = max(nameWidth, len(entry.Shortcode)+2)
	}
	inner := min(width-4, max(ansi.StringWidth(help), markerWidth+previewWidth+2+nameWidth))
	edge := lipgloss.NewStyle().Foreground(theme.Border)
	normal := lipgloss.NewStyle().Foreground(theme.Secondary)
	active := lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	side := edge.Render("│")
	frame := func(row string, used int) string {
		return side + " " + row + strings.Repeat(" ", inner-used) + " " + side
	}
	rule := strings.Repeat("─", inner+2)
	rows := []string{edge.Render("╭" + rule + "╮")}
	for i, entry := range p.matches {
		prefix, style := strings.Repeat(" ", markerWidth), normal
		if i == p.selected {
			prefix = "› "
			style = active
		}
		prefix = ansi.Truncate(prefix, inner, "")
		used := ansi.StringWidth(prefix)
		if inner >= markerWidth+previewWidth+5 {
			prefix += entry.Glyph + "  "
			used += previewWidth + 2
		}
		label := ansi.Truncate(":"+entry.Shortcode+":", inner-used, "…")
		rows = append(rows, frame(style.Render(prefix)+style.Render(label), used+ansi.StringWidth(label)))
	}
	muted := lipgloss.NewStyle().Foreground(theme.Muted)
	hint := ansi.Truncate(help, inner, "…")
	rows = append(rows, frame(muted.Render(hint), ansi.StringWidth(hint)), edge.Render("╰"+rule+"╯"))
	return strings.Join(rows, "\n")
}

// InlinePopups lays out non-empty composer panels on separate lines.
func InlinePopups(panels ...string) string {
	var b strings.Builder
	for _, panel := range panels {
		if panel != "" {
			b.WriteString(panel)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
