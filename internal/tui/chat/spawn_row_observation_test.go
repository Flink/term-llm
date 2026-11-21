package chat

import (
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/subagentview"
	"github.com/term-llm/term-llm/internal/ui"
)

func TestDetachedSpawnRowsFinishIndependentlyOfGroupWait(t *testing.T) {
	m, store := newTurnLeaseTestModel(t)
	m.streaming = true
	m.width = 100
	const child = "child-short"
	putBarrierChild(t, store, m.SessionID(), child, "completed")
	m.tracker.HandleToolStart("one", "spawn_agent", "short child", nil)
	end := ui.ToolEndEvent("one", "spawn_agent", "short child", true)
	end.ToolOutput = `{"agent_id":"child-short","status":"queued"}`
	var state streamCommandBuffer
	m.handleStreamToolEnd(streamEventMsg{}, end, &state)
	if len(state.cmds) != 1 {
		t.Fatalf("detached receipt scheduled %d observers, want 1", len(state.cmds))
	}
	m.tracker.HandleToolStart("two", "spawn_agent", "long child", nil)
	m.tracker.HandleToolEndWithResult("two", true, "long child", `{"agent_id":"child-long","status":"running"}`, nil)
	m.tracker.HandleToolStart("wait-both", "wait_agent", "", nil)
	m.persistedSubagents = map[string]subagentview.CompletedRun{"one": {AgentID: child, SubagentStatus: "queued"}}

	circle := func(id string) string {
		return ui.RenderToolSegment(ui.FindSegmentByCallID(m.tracker, id), -1, 100, false)
	}
	if !strings.HasPrefix(circle("one"), ui.WorkingCircle()) {
		t.Fatal("successful spawn receipt looked complete")
	}
	m.Update(state.cmds[0]())
	if !strings.HasPrefix(circle("one"), ui.SuccessCircle()) || !strings.HasPrefix(circle("two"), ui.WorkingCircle()) || !m.tracker.HasPending() {
		t.Fatal("first child's completion should update only its spawn, not its sibling or the group wait")
	}
	if run := m.persistedSubagents["one"]; run.SubagentStatus != "completed" || run.Fingerprint == 0 {
		t.Fatal("historical row kept its stale receipt")
	}
	row, err := store.GetAgentRun(t.Context(), child)
	if err != nil || !row.CollectedAt.IsZero() {
		t.Fatalf("UI observation affected collection: %v, %v", row.CollectedAt, err)
	}
}
