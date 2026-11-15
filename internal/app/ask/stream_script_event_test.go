package ask

import (
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/codemode"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/ui"
)

func TestAskScriptReceiptAtCompletion(t *testing.T) {
	for _, name := range []string{codemode.ExecToolName, codemode.WaitToolName, codemode.CancelToolName, "read_file"} {
		t.Run(name, func(t *testing.T) {
			model := newAskStreamModel()
			model.width = 100
			model.tracker.HandleToolStart("script-call", name, "initial info", nil)
			event := ui.StreamEvent{
				Type: ui.StreamEventToolEnd, ToolCallID: "script-call", ToolSuccess: true,
				ToolInfo: "completion info", ToolOutput: `{"cell_id":"ask-cell","status":"completed"}`,
				ToolContentParts: []core.ToolContentPart{{Type: core.ToolContentPartText, Text: "script observed output"}},
			}
			updated, _ := model.Update(askStreamEventMsg{event: event})
			model = updated.(askStreamModel)
			if model.tracker.HasPending() || len(model.tracker.Segments) != 1 {
				t.Fatal("completion changed pending or segment semantics")
			}
			seg := &model.tracker.Segments[0]
			if seg.ToolStatus != ui.ToolSuccess || seg.ToolInfo != event.ToolInfo {
				t.Fatalf("completion status/info lost: %+v", seg)
			}
			rendered := ui.StripANSI(ui.RenderToolSegment(seg, -1, model.width, true))
			if name == "read_file" {
				if seg.CodeMode != nil || strings.Contains(rendered, "script observed output") || strings.Contains(rendered, "ask-cell") {
					t.Fatalf("unrelated tool acquired script receipt: %s", rendered)
				}
				return
			}
			if seg.CodeMode == nil || seg.CodeMode.CellID != "ask-cell" || seg.CodeMode.Status != "completed" {
				t.Fatalf("receipt lost at ask completion: %+v", seg.CodeMode)
			}
			if !strings.Contains(rendered, "ask-cell") || !strings.Contains(rendered, "script observed output") {
				t.Fatalf("completed receipt not visible: %s", rendered)
			}
		})
	}
}
