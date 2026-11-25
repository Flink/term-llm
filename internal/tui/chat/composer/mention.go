package composer

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/term-llm/term-llm/internal/mentions"
	"github.com/term-llm/term-llm/internal/ui"
)

// MentionDisplayLimit is how many suggestion rows the mention popup reserves so
// its height stays fixed between empty, pending, and populated states.
const MentionDisplayLimit = 8

const mentionStatus = "↑↓ navigate  enter/tab select  esc close"

// MentionDetail splits a model suggestion's muted suffix into its moving-alias
// badge/description and an effort suffix the panel keeps visible when narrow.
func MentionDetail(row mentions.ModelRow) (detail, tail string) {
	description := row.Description
	if at := strings.LastIndex(description, "effort: "); at >= 0 && (at == 0 || strings.HasSuffix(description[:at], " · ")) {
		tail = description[at:]
		description = strings.TrimSuffix(description[:at], " · ")
	}
	switch {
	case row.Badge == "":
		return description, tail
	case description == "":
		return row.Badge, tail
	default:
		return row.Badge + "  " + description, tail
	}
}

// MentionKind identifies a suggestion category. The popup orders categories by
// query kind: models first for scoped or provider-qualified queries, and after
// files for bare provider prefixes so file suggestions keep the default slot.
type MentionKind uint8

const (
	MentionAgent MentionKind = iota
	MentionModel
	MentionFile
)

// MentionLayout maps popup row indexes onto category offsets.
type MentionLayout struct {
	Agents, Models, Files int
	ModelsFirst           bool
}

// Count is the number of selectable rows.
func (l MentionLayout) Count() int { return l.Agents + l.Models + l.Files }

// ModelsActionable reports whether synchronized model rows are usable while
// the file and agent search is still pending.
func (l MentionLayout) ModelsActionable() bool { return l.ModelsFirst && l.Models > 0 }

// SelectedModel reports whether index is one of the leading actionable model
// rows.
func (l MentionLayout) SelectedModel(index int) bool {
	kind, _ := l.RowAt(index)
	return kind == MentionModel && l.ModelsActionable()
}

// RowAt returns the category at index and its offset within that category.
func (l MentionLayout) RowAt(index int) (MentionKind, int) {
	if index < 0 || index >= l.Count() {
		return MentionFile, -1
	}
	if l.ModelsFirst {
		if index < l.Models {
			return MentionModel, index
		}
		index -= l.Models
		if index < l.Agents {
			return MentionAgent, index
		}
		return MentionFile, index - l.Agents
	}
	if index < l.Agents {
		return MentionAgent, index
	}
	index -= l.Agents
	if index < l.Files {
		return MentionFile, index
	}
	return MentionModel, index - l.Files
}

// MentionHighlightQuery is the typed part of an open model token to emphasize
// in its rows. Effort rows emphasize only the effort fragment.
func MentionHighlightQuery(token mentions.ModelToken) string {
	if token.EffortStep {
		return token.EffortQuery
	}
	return token.Query
}

// MentionRow is one composer mention suggestion. Detail and Tail are muted text
// after the label; Highlight is the typed query to emphasize in the label; Path
// rows truncate from the left so a matched basename stays visible.
type MentionRow struct {
	Label     string
	Detail    string
	Tail      string
	Highlight string
	Path      bool
	Positions []int
}

// MentionPanel is the @ suggestion list. Rows are ordered by the caller; the
// panel owns only presentation and the fixed footprint.
type MentionPanel struct {
	Rows      []MentionRow
	Selected  int
	Searching bool
	Empty     string
}

// View renders the framed suggestion list at the given outer width.
func (p MentionPanel) View(theme *ui.Theme, width int) string {
	muted := lipgloss.NewStyle().Foreground(theme.Muted)
	selected := lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	match := lipgloss.NewStyle().Foreground(theme.Primary)
	width = max(24, min(width-2, 88))
	popupStyle := lipgloss.NewStyle().Width(width).MaxWidth(width).Border(lipgloss.RoundedBorder()).BorderForeground(theme.Muted)
	contentWidth := max(1, width-popupStyle.GetHorizontalFrameSize())

	rows := make([]string, 0, MentionDisplayLimit+1)
	if count := len(p.Rows); count > 0 {
		start, end := ui.VisibleRange(count, p.Selected, MentionDisplayLimit)
		for i := start; i < end; i++ {
			rows = append(rows, p.rowLine(i, contentWidth, muted, selected, match))
		}
	} else if p.Empty != "" {
		rows = append(rows, muted.Render("  "+p.Empty))
	}
	// Reserve the full result area from the first visible frame. Searching and
	// short result sets otherwise make the bottom-anchored popup grow upward as
	// rows arrive.
	for len(rows) < MentionDisplayLimit {
		rows = append(rows, "")
	}
	rows = append(rows, muted.Render(TruncateText("  "+mentionStatus, contentWidth)))
	return popupStyle.Render(strings.Join(rows, "\n"))
}

func (p MentionPanel) rowLine(index, width int, muted, selected, match lipgloss.Style) string {
	row := p.Rows[index]
	prefix := "  "
	base := muted
	isSelected := index == p.Selected && !p.Searching
	if isSelected {
		prefix = "› "
		base = selected
	}
	available := max(4, width-lipgloss.Width(prefix))
	label, positions := row.Label, row.Positions
	if row.Path {
		label, positions = TruncatePath(label, positions, available)
	} else {
		label = TruncateText(label, available)
	}
	switch {
	case isSelected:
		positions = nil
	case positions == nil && row.Highlight != "":
		positions = ContiguousPositions(label, row.Highlight)
	}
	line := base.Render(prefix) + HighlightPath(label, positions, base, match)
	if detail := mentionDetail(row, available-lipgloss.Width(label)-2); detail != "" {
		line += muted.Render(detail)
	}
	return line
}

// mentionDetail renders the muted suffix within width, keeping Tail (a model's
// effort choices) visible by shortening Detail first when it alone still fits.
func mentionDetail(row MentionRow, width int) string {
	if width < 2 {
		return ""
	}
	if row.Tail != "" && lipgloss.Width(row.Tail)+2 <= width {
		lead := width - lipgloss.Width(row.Tail) - 2
		if row.Detail == "" || lead < 2 {
			return "  " + row.Tail
		}
		return "  " + TruncateText(row.Detail, lead) + "  " + row.Tail
	}
	text := row.Detail
	if row.Tail != "" {
		if text != "" {
			text += "  "
		}
		text += row.Tail
	}
	if text == "" {
		return ""
	}
	return "  " + TruncateText(text, width)
}

// TruncateText cuts value to width display columns, appending an ellipsis.
func TruncateText(value string, width int) string {
	if lipgloss.Width(value) <= width {
		return value
	}
	runes := []rune(value)
	for len(runes) > 1 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// TruncatePath keeps the end of a path visible, adjusting highlight byte
// offsets to the shortened value.
func TruncatePath(value string, positions []int, width int) (string, []int) {
	if lipgloss.Width(value) <= width {
		return value, positions
	}
	runes := []rune(value)
	startRune := len(runes)
	for startRune > 0 && lipgloss.Width("…"+string(runes[startRune-1:])) <= width {
		startRune--
	}
	if startRune < len(runes) && lipgloss.Width("…"+string(runes[startRune:])) > width {
		startRune++
	}
	if startRune >= len(runes) {
		return "…", nil
	}
	byteStart := len(string(runes[:startRune]))
	display := "…" + string(runes[startRune:])
	adjusted := make([]int, 0, len(positions))
	for _, position := range positions {
		if position >= byteStart {
			adjusted = append(adjusted, len("…")+position-byteStart)
		}
	}
	return display, adjusted
}

// ContiguousPositions returns the byte offsets of query inside value, or nil.
func ContiguousPositions(value, query string) []int {
	if query == "" {
		return nil
	}
	start := strings.Index(lowerASCII(value), lowerASCII(query))
	if start < 0 {
		return nil
	}
	positions := make([]int, 0, len(query))
	for i := start; i < start+len(query); i++ {
		positions = append(positions, i)
	}
	return positions
}

func lowerASCII(value string) string {
	lower := []byte(value)
	for i, b := range lower {
		if b >= 'A' && b <= 'Z' {
			lower[i] = b + ('a' - 'A')
		}
	}
	return string(lower)
}

// HighlightPath styles the byte ranges in positions, leaving the rest in the
// normal style.
func HighlightPath(path string, positions []int, normal, highlight lipgloss.Style) string {
	hits := make(map[int]bool, len(positions))
	for _, position := range positions {
		hits[position] = true
	}
	var output, run strings.Builder
	highlighted := false
	flush := func() {
		if run.Len() == 0 {
			return
		}
		style := normal
		if highlighted {
			style = highlight
		}
		output.WriteString(style.Render(run.String()))
		run.Reset()
	}
	first := true
	for offset, r := range path {
		hit := false
		for i := 0; i < utf8.RuneLen(r); i++ {
			if hits[offset+i] {
				hit = true
				break
			}
		}
		if !first && hit != highlighted {
			flush()
		}
		first = false
		highlighted = hit
		run.WriteRune(r)
	}
	flush()
	return output.String()
}

// ReplaceMention replaces a full mention span and returns the new text with the
// caret offset after the insertion. A continuing mention keeps its token open,
// so it never adds a trailing space; a final mention separates from following
// text unless the suffix already starts with whitespace.
func ReplaceMention(text string, start, end int, insert string, continuing bool) (string, int) {
	if start < 0 || end <= start || end > len(text) || insert == "" {
		return text, start
	}
	separator := ""
	if !continuing {
		separator = " "
		if end < len(text) {
			if r, _ := utf8.DecodeRuneInString(text[end:]); unicode.IsSpace(r) {
				separator = ""
			}
		}
	}
	return text[:start] + insert + separator + text[end:], start + len(insert) + len(separator)
}

// MentionEmptyState describes why the suggestion list has no selectable row.
type MentionEmptyState struct {
	ModelOwned bool
	EffortStep bool
	AgentsOnly bool
	Searching  bool
	EmptyQuery bool
	Indexing   bool
	AgentErr   error
	IndexErr   error
}

// Message is the muted line shown for an empty list. Model namespaces take
// precedence so a scoped query never surfaces an unrelated file or agent error.
func (s MentionEmptyState) Message() string {
	switch {
	case s.ModelOwned && s.EffortStep:
		return "no matching effort choices for this model"
	case s.ModelOwned:
		return "no matching models"
	case s.AgentsOnly && s.AgentErr != nil:
		return "agent completion unavailable: " + s.AgentErr.Error()
	case s.AgentsOnly && s.Searching:
		return "searching permitted agents…"
	case s.AgentsOnly && s.EmptyQuery:
		return "type to search permitted agents"
	case s.AgentsOnly:
		return "no matching permitted agents"
	case s.Indexing:
		return "indexing project files…"
	case s.IndexErr != nil:
		return "file index unavailable: " + s.IndexErr.Error()
	case s.Searching:
		return "searching agents and project files…"
	default:
		return "no matching agents, models or project files"
	}
}
