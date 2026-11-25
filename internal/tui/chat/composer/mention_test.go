package composer

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/term-llm/term-llm/internal/mentions"
	"github.com/term-llm/term-llm/internal/ui"
)

func TestContiguousPositionsUseOnlyUnselectedMatches(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value string
		query string
		want  string
	}{
		{name: "substring", value: "internal/reflow/margin/margin.go", query: "margin", want: "margin"},
		{name: "case insensitive", value: "Internal/Render/Markdown.go", query: "markdown", want: "Markdown"},
		{name: "fuzzy subsequence", value: "internal/render/markdown/testdata/heading_width80.golden", query: "margin"},
		{name: "empty query", value: "internal/reflow/margin", query: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			positions := ContiguousPositions(tt.value, tt.query)
			if tt.want == "" {
				if len(positions) != 0 {
					t.Fatalf("positions = %v, want none", positions)
				}
				return
			}
			start := strings.Index(tt.value, tt.want)
			if len(positions) != len(tt.want) || positions[0] != start || positions[len(positions)-1] != start+len(tt.want)-1 {
				t.Fatalf("positions = %v, want contiguous span [%d,%d]", positions, start, start+len(tt.want)-1)
			}
		})
	}
}

func TestTruncatePathKeepsMatchedBasename(t *testing.T) {
	path := "very/deep/project/path/internal/session/types.go"
	position := strings.LastIndex(path, "types.go")
	display, positions := TruncatePath(path, []int{position}, 20)
	if !strings.Contains(display, "types.go") || len(positions) != 1 || positions[0] < 0 || positions[0] >= len(display) {
		t.Fatalf("truncation display=%q positions=%v", display, positions)
	}
}

func TestMentionPanelKeepsFixedHeightAndSelectedMarker(t *testing.T) {
	theme := ui.DefaultTheme()
	panel := MentionPanel{Empty: "no matching models"}
	empty := panel.View(theme, 80)

	panel.Rows = []MentionRow{
		{Label: "claude-bin:opus", Detail: "latest  effort: low, medium", Highlight: "opus"},
		{Label: "cmd/claude.go", Path: true, Positions: ContiguousPositions("cmd/claude.go", "claude")},
	}
	panel.Selected = 1
	populated := panel.View(theme, 80)
	if lipgloss.Height(empty) != lipgloss.Height(populated) {
		t.Fatalf("panel height changed with rows: empty=%d populated=%d", lipgloss.Height(empty), lipgloss.Height(populated))
	}
	plain := ui.StripANSI(populated)
	for _, want := range []string{"claude-bin:opus", "latest", "cmd/claude.go", "› cmd/claude.go", "enter/tab select"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("panel %q missing %q", plain, want)
		}
	}
}

func TestMentionPanelSelectedRowsDropHighlight(t *testing.T) {
	theme := ui.DefaultTheme()
	panel := MentionPanel{Rows: []MentionRow{{Label: "claude-bin:opus", Highlight: "opus"}}, Selected: -1}
	unselected := panel.View(theme, 80)
	panel.Selected = 0
	selected := panel.View(theme, 80)
	if unselected == selected {
		t.Fatal("selected row did not change styling")
	}
}

func TestReplaceMentionSpacingAndSpan(t *testing.T) {
	for _, tt := range []struct {
		name       string
		text       string
		start, end int
		insert     string
		continuing bool
		want       string
		wantCursor int
	}{
		{name: "final adds space", text: "@m:opus", start: 0, end: 7, insert: "claude-bin:opus-medium", want: "claude-bin:opus-medium ", wantCursor: 23},
		{name: "final before space", text: "@m:opus tail", start: 0, end: 7, insert: "claude-bin:opus", want: "claude-bin:opus tail", wantCursor: 15},
		{name: "continuing keeps token open", text: "猫 @m:opus", start: 4, end: 11, insert: "@model:claude-bin:opus#", continuing: true, want: "猫 @model:claude-bin:opus#", wantCursor: 27},
		{name: "full span replaces suffix", text: "@m:opus-stale", start: 0, end: 13, insert: "claude-bin:opus", want: "claude-bin:opus ", wantCursor: 16},
		{name: "invalid span", text: "@m:opus", start: 3, end: 3, insert: "x", want: "@m:opus", wantCursor: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, cursor := ReplaceMention(tt.text, tt.start, tt.end, tt.insert, tt.continuing)
			if got != tt.want || cursor != tt.wantCursor {
				t.Fatalf("ReplaceMention = %q, %d; want %q, %d", got, cursor, tt.want, tt.wantCursor)
			}
		})
	}
}

func TestMentionLayoutCategoryOrder(t *testing.T) {
	for _, tt := range []struct {
		name   string
		layout MentionLayout
		want   []MentionKind
	}{
		{
			name:   "bare prefix keeps files before models",
			layout: MentionLayout{Agents: 1, Files: 2, Models: 3},
			want:   []MentionKind{MentionAgent, MentionFile, MentionFile, MentionModel, MentionModel, MentionModel},
		},
		{
			name:   "qualified query puts models first",
			layout: MentionLayout{Agents: 1, Files: 2, Models: 3, ModelsFirst: true},
			want:   []MentionKind{MentionModel, MentionModel, MentionModel, MentionAgent, MentionFile, MentionFile},
		},
		{
			name:   "scoped query only has models",
			layout: MentionLayout{Models: 2, ModelsFirst: true},
			want:   []MentionKind{MentionModel, MentionModel},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.layout.Count(); got != len(tt.want) {
				t.Fatalf("Count = %d, want %d", got, len(tt.want))
			}
			for i, want := range tt.want {
				kind, offset := tt.layout.RowAt(i)
				if kind != want || offset < 0 {
					t.Fatalf("RowAt(%d) = %v, %d; want %v", i, kind, offset, want)
				}
			}
		})
	}
	if kind, offset := (MentionLayout{}).RowAt(0); offset != -1 || kind != MentionFile {
		t.Fatalf("out-of-range RowAt = %v, %d", kind, offset)
	}
}

func TestMentionDetailSplitsEffortSuffix(t *testing.T) {
	detail, tail := MentionDetail(mentions.ModelRow{Badge: "latest", Description: "Opus 5.5 · effort: low, medium"})
	if detail != "latest  Opus 5.5" || tail != "effort: low, medium" {
		t.Fatalf("MentionDetail = %q, %q", detail, tail)
	}
	detail, tail = MentionDetail(mentions.ModelRow{Description: "effort: high"})
	if detail != "" || tail != "effort: high" {
		t.Fatalf("effort-only MentionDetail = %q, %q", detail, tail)
	}
	detail, tail = MentionDetail(mentions.ModelRow{Description: "Opus 4.8"})
	if detail != "Opus 4.8" || tail != "" {
		t.Fatalf("plain MentionDetail = %q, %q", detail, tail)
	}
}

func TestMentionPanelKeepsEffortTailOnNarrowWidth(t *testing.T) {
	panel := MentionPanel{Rows: []MentionRow{{
		Label: "gpt:opus", Detail: "Very Long Display Name", Tail: "effort: low, high",
	}}}
	view := ui.StripANSI(panel.View(ui.DefaultTheme(), 40))
	if !strings.Contains(view, "effort: low, high") {
		t.Fatalf("narrow panel dropped the effort suffix: %q", view)
	}
}
