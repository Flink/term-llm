package streamview

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/term-llm/term-llm/internal/ui"
)

func TestHandleClickBoundariesAndFallback(t *testing.T) {
	var state State
	host := ClickHost{AltScreen: true, Width: 80, ViewportHeight: 5, ViewportOffset: 10}
	hit := ui.ScriptHeaderHit{Line: 11, ColumnStart: 2, ColumnEnd: 8, ToolCallID: "live"}
	click := tea.MouseClickMsg{X: 7, Y: 1, Button: tea.MouseLeft}
	for _, tc := range []struct {
		name         string
		alt, blocked bool
		x            int
		want         bool
	}{
		{"inside", true, false, 7, true},
		{"right edge", true, false, 8, false},
		{"left of span", true, false, 1, false},
		{"inline", false, false, 7, false},
		{"blocked", true, true, 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state.Clear()
			state.Capture(map[int]ui.ScriptHeaderHit{11: hit}, 20, false, false)
			h, msg := host, click
			h.AltScreen, h.Blocked, msg.X = tc.alt, tc.blocked, tc.x
			if got := state.HandleClick(h, msg); got != tc.want || state.Overrides()["live"] != tc.want {
				t.Fatalf("consumed = %v, overrides = %v, want %v", got, state.Overrides(), tc.want)
			}
			if tc.want {
				hit := hit
				hit.Expanded = true
				state.Capture(map[int]ui.ScriptHeaderHit{11: hit}, 20, false, false)
				if !state.HandleClick(h, msg) || state.Overrides() != nil {
					t.Fatalf("second click should restore fallback: %v", state.Overrides())
				}
			}
		})
	}
}
