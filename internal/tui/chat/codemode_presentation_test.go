package chat

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/ui"
)

func TestChatToolEndRetainsScriptReceiptForExpansion(t *testing.T) {
	for _, altScreen := range []bool{false, true} {
		t.Run(map[bool]string{false: "inline", true: "alternate-screen"}[altScreen], func(t *testing.T) {
			m := newTestChatModel(altScreen)
			m.width = 100
			m.streaming = true
			m.toolExpandHintShown = true
			m.toolsExpanded = true
			const envelope = `{"cell_id":"cell-1","status":"yielded","state_committed":false,"max_wait":0}`
			state := &streamCommandBuffer{}
			m.handleStreamToolStart(streamEventMsg{}, ui.ToolStartEvent("script-1", "spawn_script", "fresh async JavaScript cell", json.RawMessage(`{"code":"text('hello');"}`)), state)
			end := ui.ToolEndEvent("script-1", "spawn_script", "fresh async JavaScript cell", true)
			end.ToolOutput = envelope
			end.ToolContentParts = []core.ToolContentPart{{Type: core.ToolContentPartText, Text: "first output"}, {Type: core.ToolContentPartText, Text: "\n" + envelope}}
			m.handleStreamToolEnd(streamEventMsg{}, end, state)
			seg := ui.FindSegmentByCallID(m.tracker, "script-1")
			if seg == nil || seg.CodeMode == nil {
				t.Fatal("chat tool-end handler dropped the script receipt")
			}
			if m.tracker.HasPending() {
				t.Fatal("yielded cell must not keep the tool invocation pending")
			}
			got := ui.StripANSI(ui.RenderToolSegment(seg, -1, m.width, true))
			for _, want := range []string{"cell-1", "running", "text('hello');", "first output"} {
				if !strings.Contains(got, want) {
					t.Errorf("expanded call missing %q: %q", want, got)
				}
			}
			if strings.Contains(got, "fresh async") || strings.Contains(got, "state_committed") {
				t.Fatalf("generic label or status envelope in expanded call: %q", got)
			}
		})
	}
}

func TestChatCtrlETogglesScriptSourceAndObservedOutput(t *testing.T) {
	for _, altScreen := range []bool{false, true} {
		t.Run(map[bool]string{false: "inline", true: "alternate-screen"}[altScreen], func(t *testing.T) {
			m := newTestChatModel(altScreen)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 45})
			m = updated.(*Model)
			m.streaming = true
			m.toolExpandHintShown = true
			const source = "const marker = 'source-only';\ntext(marker);"
			args, err := json.Marshal(map[string]string{"code": source})
			if err != nil {
				t.Fatal(err)
			}
			updated, _ = m.Update(streamEventMsg{event: ui.ToolStartEvent("toggle-script", "spawn_script", "fresh async JavaScript cell", args)})
			m = updated.(*Model)
			const envelope = `{"cell_id":"cell-toggle","status":"yielded","elapsed_ms":15000,"max_wait":60}`
			end := ui.ToolEndEvent("toggle-script", "spawn_script", "fresh async JavaScript cell", true)
			end.ToolOutput = envelope
			end.ToolContentParts = []core.ToolContentPart{
				{Type: core.ToolContentPartText, Text: "unique observed output"},
				{Type: core.ToolContentPartText, Text: "\n" + envelope},
			}
			updated, _ = m.Update(streamEventMsg{event: end})
			m = updated.(*Model)

			collapsed := ui.StripANSI(m.View().Content)
			if m.toolsExpanded || !strings.Contains(collapsed, "spawn_script") || !strings.Contains(collapsed, "running") {
				t.Fatalf("script should start collapsed with observed lifecycle: %q", collapsed)
			}
			for _, hidden := range []string{"JavaScript source", "const marker", "unique observed output"} {
				if strings.Contains(collapsed, hidden) {
					t.Fatalf("collapsed script leaked expanded detail %q: %q", hidden, collapsed)
				}
			}

			updated, _ = m.Update(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
			m = updated.(*Model)
			expanded := ui.StripANSI(m.View().Content)
			if !m.toolsExpanded || !m.tracker.Expanded() {
				t.Fatal("Ctrl+E did not propagate expansion to the streaming tracker")
			}
			for _, want := range []string{"JavaScript source", "const marker = 'source-only';", "text(marker);", "Observed output", "unique observed output", "cell-toggle"} {
				if !strings.Contains(expanded, want) {
					t.Fatalf("Ctrl+E expansion missing %q: %q", want, expanded)
				}
			}
			if strings.Contains(expanded, "max_wait") || strings.Contains(expanded, "fresh async") {
				t.Fatalf("expanded script exposed transport envelope or boilerplate: %q", expanded)
			}

			updated, _ = m.Update(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
			m = updated.(*Model)
			recollapsed := ui.StripANSI(m.View().Content)
			if m.toolsExpanded || m.tracker.Expanded() || !strings.Contains(recollapsed, "spawn_script") {
				t.Fatalf("second Ctrl+E did not restore the collapsed row: %q", recollapsed)
			}
			for _, hidden := range []string{"JavaScript source", "const marker", "unique observed output"} {
				if strings.Contains(recollapsed, hidden) {
					t.Fatalf("second Ctrl+E retained expanded detail %q: %q", hidden, recollapsed)
				}
			}
		})
	}
}
