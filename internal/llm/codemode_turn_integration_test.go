package llm_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/codemode/tool"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/testutil"
)

type turnGate struct {
	release chan struct{}
	entered chan struct{}
}

func (*turnGate) Spec() core.ToolSpec {
	return core.ToolSpec{Name: "turn_gate", Schema: map[string]interface{}{"type": "object"}}
}
func (*turnGate) Preview(json.RawMessage) string { return "gate" }
func (g *turnGate) Execute(ctx context.Context, _ json.RawMessage) (core.ToolOutput, error) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	select {
	case <-g.release:
		return core.TextOutput("released"), nil
	case <-ctx.Done():
		return core.ToolOutput{}, ctx.Err()
	}
}
func TestCodeModeFinalPreservesCellAndNextTurnCanWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	h := testutil.NewEngineHarness()
	t.Cleanup(h.Engine.CloseCodeMode)
	g := &turnGate{release: make(chan struct{}), entered: make(chan struct{}, 1)}
	h.Registry.Register(tool.NewExec())
	h.Registry.Register(tool.NewWait())
	h.Registry.Register(tool.NewCancel())
	h.Registry.Register(g)
	h.Provider.AddToolCall("original-spawn", "spawn_script", map[string]any{"code": `await tools.turn_gate({});store('finished',true)`, "max_wait": 0})
	h.Provider.AddTextResponse("final answer")
	req := core.Request{SessionID: "across-turn", Messages: []core.Message{core.UserText("start")}, Tools: h.Registry.AllSpecs(), MaxTurns: 5}
	// Let the cold VM start and the child enter its gate before the model can
	// finalize. The context is a watchdog, not a dispatch timing expectation.
	var entered bool
	h.Engine.SetTurnCompletedCallback(func(_ context.Context, turn int, _ []core.Message, _ core.TurnMetrics) error {
		if turn == 0 {
			select {
			case <-g.entered:
				entered = true
			case <-ctx.Done():
			}
		}
		return nil
	})
	text, err := h.Run(ctx, req)
	h.Engine.SetTurnCompletedCallback(nil)
	cells := h.Engine.CodeModeCells(req.SessionID)
	if !entered {
		t.Fatalf("cell never dispatched: cells=%+v, turn error=%v", cells, err)
	}
	if err != nil || !strings.Contains(text, "final answer") {
		t.Fatalf("first turn %q %v", text, err)
	}
	if len(cells) != 1 || !cells[0].Live {
		t.Fatalf("final killed cell: %+v", cells)
	}
	close(g.release)
	// With no intermediate output, a positive wait budget keeps the second
	// stream open until the cell settles and returns its terminal receipt.
	h.Provider.AddToolCall("next-wait", "wait_script", map[string]any{"cell_id": cells[0].CellID, "max_wait": 30})
	h.Provider.AddTextResponse("collected")
	// Simulate a compacted transcript: no spawn receipt survives in Messages.
	req.Messages = []core.Message{core.UserText("collect")}
	requestOffset := len(h.Provider.RecordedRequests())
	text, err = h.Run(ctx, req)
	if err != nil || !strings.Contains(text, "collected") {
		t.Fatalf("second turn %q %v", text, err)
	}
	requests := h.Provider.RecordedRequests()[requestOffset:]
	if len(requests) != 2 {
		t.Fatalf("second turn made %d provider requests, want 2", len(requests))
	}
	found := false
	for _, message := range requests[0].Messages {
		messageText := core.MessageText(message)
		found = found || (strings.Contains(messageText, "<live_code_cells>") && strings.Contains(messageText, cells[0].CellID))
	}
	if !found {
		t.Fatal("next-turn handle not discoverable before wait_script")
	}
	for _, message := range requests[1].Messages {
		for _, part := range message.Parts {
			if result := part.ToolResult; result != nil && result.ID == "next-wait" {
				var receipt core.CodeModeEnvelope
				if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil || result.IsError || receipt.CellID != cells[0].CellID || receipt.Status != "completed" || !receipt.StateCommitted {
					t.Fatalf("wait_script result %q (isError=%t): %v", result.Content, result.IsError, err)
				}
				return
			}
		}
	}
	t.Fatal("missing next-turn wait_script result")
}
