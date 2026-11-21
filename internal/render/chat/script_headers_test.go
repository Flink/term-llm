package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/session"
	"github.com/term-llm/term-llm/internal/tools"
	"github.com/term-llm/term-llm/internal/ui"
)

func scriptHistoryMessages(t *testing.T) []session.Message {
	t.Helper()
	execArgs, _ := json.Marshal(map[string]string{"code": "text('alpha');", "cell_id": "cell-a"})
	waitArgs, _ := json.Marshal(map[string]string{"cell_id": "cell-b"})
	return []session.Message{
		{
			ID: 1, Role: core.RoleUser, Sequence: 0,
			TextContent: "run scripts",
			Parts:       []core.Part{{Type: core.PartText, Text: "run scripts"}},
		},
		{
			ID: 2, Role: core.RoleAssistant, Sequence: 1,
			Parts: []core.Part{{
				Type:     core.PartToolCall,
				ToolCall: &core.ToolCall{ID: "call-a", Name: tools.CodeExecToolName, Arguments: execArgs},
			}},
		},
		{
			ID: 3, Role: core.RoleTool, Sequence: 2,
			Parts: []core.Part{{
				Type: core.PartToolResult,
				ToolResult: &core.ToolResult{
					ID: "call-a", Name: tools.CodeExecToolName,
					Content:      `{"cell_id":"cell-a","status":"completed"}`,
					ContentParts: []core.ToolContentPart{{Type: core.ToolContentPartText, Text: "alpha observed output"}},
				},
			}},
		},
		{
			ID: 4, Role: core.RoleAssistant, Sequence: 3,
			Parts: []core.Part{{
				Type:     core.PartToolCall,
				ToolCall: &core.ToolCall{ID: "call-b", Name: tools.CodeWaitToolName, Arguments: waitArgs},
			}},
		},
		{
			ID: 5, Role: core.RoleTool, Sequence: 4,
			Parts: []core.Part{{
				Type: core.PartToolResult,
				ToolResult: &core.ToolResult{
					ID: "call-b", Name: tools.CodeWaitToolName,
					Content: `{"cell_id":"cell-b","status":"completed"}`,
				},
			}},
		},
		{
			ID: 6, Role: core.RoleAssistant, Sequence: 5,
			TextContent: "▸ spawn_script decoy text, not a call",
			Parts:       []core.Part{{Type: core.PartText, Text: "▸ spawn_script decoy text, not a call"}},
		},
	}
}

func renderScriptHistory(t *testing.T, renderer *Renderer, overrides map[string]bool) (string, map[int]ui.ScriptHeaderHit) {
	t.Helper()
	output := renderer.Render(RenderState{
		Messages:                 scriptHistoryMessages(t),
		Viewport:                 ViewportState{Height: 40, AtBottom: true},
		Mode:                     RenderModeAltScreen,
		Width:                    100,
		Height:                   40,
		ScriptExpansionOverrides: overrides,
	})
	return output, renderer.ScriptLineHitsSnapshot()
}

func TestRendererScriptHeaderHitsAnchorHistoryToolCallIDs(t *testing.T) {
	renderer := NewRenderer(100, 40)
	output, hits := renderScriptHistory(t, renderer, nil)
	lines := strings.Split(ui.StripANSI(output), "\n")
	if len(hits) != 2 {
		t.Fatalf("hits = %#v, want two script headers\noutput:\n%s", hits, strings.Join(lines, "\n"))
	}
	byID := map[string]ui.ScriptHeaderHit{}
	for line, hit := range hits {
		if hit.Line != line {
			t.Fatalf("snapshot line %d disagrees with hit line %d", line, hit.Line)
		}
		byID[hit.ToolCallID] = hit
	}
	for _, id := range []string{"call-a", "call-b"} {
		hit, ok := byID[id]
		if !ok {
			t.Fatalf("missing script header hit for %s: %#v", id, hits)
		}
		if hit.Line < 0 || hit.Line >= len(lines) || !strings.Contains(lines[hit.Line], "▸") {
			t.Fatalf("hit for %s does not point at a rendered header: line=%d %q", id, hit.Line, lines)
		}
	}
	for _, line := range lines {
		if strings.Contains(line, "decoy text") && strings.Contains(line, "▸") {
			for _, hit := range hits {
				if hit.Line >= 0 && hit.Line < len(lines) && lines[hit.Line] == line {
					t.Fatal("decoy plain text line became a script header target")
				}
			}
		}
	}
}

func TestRendererScriptExpansionOverridesArePerCallWithGlobalFallback(t *testing.T) {
	renderer := NewRenderer(100, 40)
	_, hits := renderScriptHistory(t, renderer, nil)
	for _, hit := range hits {
		if hit.Expanded {
			t.Fatalf("default render should be collapsed: %#v", hit)
		}
	}

	output, hits := renderScriptHistory(t, renderer, map[string]bool{"call-b": true})
	lines := strings.Split(ui.StripANSI(output), "\n")
	byID := map[string]ui.ScriptHeaderHit{}
	for _, hit := range hits {
		byID[hit.ToolCallID] = hit
	}
	first, second := byID["call-a"], byID["call-b"]
	if first.Expanded || !strings.Contains(lines[first.Line], "▸") || strings.Contains(lines[first.Line], "▾") {
		t.Fatalf("first call should stay collapsed: %#v %q", first, lines[first.Line])
	}
	if !second.Expanded || !strings.Contains(lines[second.Line], "▾") || !strings.Contains(strings.Join(lines, "\n"), "Observed output") {
		t.Fatalf("second call should expand independently: %#v\n%s", second, strings.Join(lines, "\n"))
	}

	renderer.SetToolsExpanded(true)
	output, hits = renderScriptHistory(t, renderer, map[string]bool{"call-a": false})
	lines = strings.Split(ui.StripANSI(output), "\n")
	byID = map[string]ui.ScriptHeaderHit{}
	for _, hit := range hits {
		byID[hit.ToolCallID] = hit
	}
	first, second = byID["call-a"], byID["call-b"]
	if first.Expanded || !strings.Contains(lines[first.Line], "▸") {
		t.Fatalf("global expansion should remain overridable per call: %#v %q", first, lines[first.Line])
	}
	if !second.Expanded || !strings.Contains(lines[second.Line], "▾") {
		t.Fatalf("global expansion should fall back for calls without overrides: %#v %q", second, lines[second.Line])
	}
}

func TestRendererReusesExpandedScriptBlockAcrossHistoryRenders(t *testing.T) {
	messages := scriptHistoryMessages(t)
	messages[1].Parts = append([]core.Part{{Type: core.PartText, Text: "cache probe"}}, messages[1].Parts...)
	renderer := NewRenderer(100, 40)
	var probeRenders int
	renderer.SetMarkdownRenderer(func(markdown string, _ int) string {
		if markdown == "cache probe" {
			probeRenders++
		}
		return markdown
	})
	state := RenderState{Messages: messages, Mode: RenderModeAltScreen, Width: 100, Height: 40,
		ScriptExpansionOverrides: map[string]bool{"call-a": true}}
	first := renderer.Render(state)
	second := renderer.Render(state)
	if first != second || probeRenders != 1 {
		t.Fatalf("expanded script block rendered %d times (want one)", probeRenders)
	}
	state.ScriptExpansionOverrides = nil
	if collapsed := renderer.Render(state); collapsed == first || strings.Contains(collapsed, "alpha observed output") {
		t.Fatal("clearing the override reused the expanded block")
	}
}
