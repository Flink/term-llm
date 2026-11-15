package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/session"
	"github.com/term-llm/term-llm/internal/ui"
)

func TestMessageBlockScriptReceiptsMatchStreaming(t *testing.T) {
	for _, name := range []string{"spawn_script", "wait_script", "cancel_script"} {
		for _, expanded := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/collapsed", true: "/expanded"}[expanded], func(t *testing.T) {
				args := json.RawMessage(`{"code":"text('hello');","cell_id":"cell-1"}`)
				call := core.ToolCall{ID: "call-1", Name: name, Arguments: args}
				const envelope = `{"cell_id":"cell-1","status":"yielded","elapsed_ms":1500,"state_committed":false,"max_wait":0,"has_more_output":true}`
				result := core.ToolResult{ID: call.ID, Name: name, Content: envelope, ContentParts: []core.ToolContentPart{
					{Type: core.ToolContentPartText, Text: "observed output"},
					{Type: core.ToolContentPartText, Text: "\n" + envelope},
				}}
				messages := []session.Message{
					{ID: 1, Role: core.RoleAssistant, Parts: []core.Part{{Type: core.PartToolCall, ToolCall: &call}}},
					{ID: 2, Role: core.RoleTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &result}}},
				}
				renderer := NewMessageBlockRendererWithContext(100, nil, messages, 0, expanded)
				history := strings.TrimSpace(ui.StripANSI(renderer.Render(&messages[0]).Rendered))
				tracker := ui.NewToolTracker()
				tracker.SetExpandHintShown(true)
				tracker.HandleToolStart(call.ID, name, "generic description", args)
				tracker.HandleToolEndWithResult(call.ID, true, "generic description", envelope, result.ContentParts)
				seg := ui.FindSegmentByCallID(tracker, call.ID)
				live := strings.TrimSpace(ui.StripANSI(ui.RenderToolSegment(seg, -1, 100, expanded)))
				if history != live {
					t.Fatalf("history/live differ:\nhistory:\n%s\nlive:\n%s", history, live)
				}
				for _, want := range []string{name, "cell-1", "running"} {
					if !strings.Contains(history, want) {
						t.Errorf("missing %q in %q", want, history)
					}
				}
				if strings.Contains(history, "state_committed") {
					t.Fatal("machine-readable envelope leaked into normal chat")
				}
				if expanded {
					if !strings.Contains(history, "observed output") {
						t.Fatal("expanded history did not reveal output")
					}
					if name == "spawn_script" && !strings.Contains(history, "text('hello');") {
						t.Fatal("expanded history did not reveal JavaScript")
					}
				} else if strings.Contains(history, "observed output") || strings.Contains(history, "text('hello');") {
					t.Fatal("collapsed receipt revealed body")
				}
			})
		}
	}
}

func TestRenderEventScriptReceiptReachesStreamingBlock(t *testing.T) {
	const envelope = `{"cell_id":"cell-1","status":"completed","state_committed":true,"max_wait":0}`
	parts := []core.ToolContentPart{{Type: core.ToolContentPartText, Text: "render event output"}, {Type: core.ToolContentPartText, Text: "\n" + envelope}}
	renderer := NewRenderer(100, 30)
	renderer.SetToolsExpanded(true)
	renderer.HandleEvent(NewStreamStartEvent())
	renderer.HandleEvent(NewStreamToolStartEvent("call-1", "spawn_script", "", json.RawMessage(`{"code":"text(42);"}`)))
	end := FromStreamEvent(ui.StreamEvent{Type: ui.StreamEventToolEnd, ToolCallID: "call-1", ToolName: "spawn_script", ToolSuccess: true, ToolOutput: envelope, ToolContentParts: parts})
	renderer.HandleEvent(end)
	got := ui.StripANSI(renderer.streaming.Render(-1, false, false))
	for _, want := range []string{"text(42);", "render event output", "completed", "cell-1"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}
