package chat

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/session"
	"github.com/term-llm/term-llm/internal/tools"
	"github.com/term-llm/term-llm/internal/ui"
)

func persistedSpawnFixture(parentID, childID, callID, output string) []session.Message {
	assistant := session.NewMessage(parentID, core.Message{Role: core.RoleAssistant, Parts: []core.Part{{
		Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: callID, Name: tools.SpawnAgentToolName, Arguments: json.RawMessage(`{"agent_name":"reviewer","prompt":"review carefully"}`)},
	}}}, 0)
	content, _ := json.Marshal(tools.SpawnAgentResult{AgentName: "reviewer", Output: output, Duration: 250, SessionID: childID})
	result := session.NewMessage(parentID, core.ToolResultMessage(callID, tools.SpawnAgentToolName, string(content), nil), 1)
	return []session.Message{*assistant, *result}
}

func TestPersistedSubagentHydrationRestoresOutputAfterReload(t *testing.T) {
	m := newTestChatModel(true)
	parentID := m.sess.ID
	childID := "child-output"
	store := &mockStore{
		sessions: map[string]*session.Session{childID: {
			ID: childID, ParentID: parentID, Provider: "mock", Model: "review-model",
			Status: session.StatusComplete, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0),
		}},
		messages: map[string][]session.Message{childID: {}},
	}
	m.store = store
	m.messages = persistedSpawnFixture(parentID, childID, "spawn-output", "first line\nfinal durable output")

	cmd := m.loadPersistedSubagentsCmd()
	firstPaint := ui.StripANSI(m.renderHistory())
	if !strings.Contains(firstPaint, "final durable output") {
		t.Fatalf("first paint omitted synchronous parent-result fallback:\n%s", firstPaint)
	}
	loaded, ok := cmd().(persistedSubagentsLoadedMsg)
	if !ok {
		t.Fatalf("hydration returned %T", cmd())
	}
	m.applyPersistedSubagents(loaded)
	if run := m.persistedSubagents["spawn-output"]; !run.ChildAvailable || run.Output == "" {
		t.Fatalf("hydrated run = %#v", run)
	}
	plain := ui.StripANSI(m.renderHistory())
	if !strings.Contains(plain, "review carefully") || !strings.Contains(plain, "final durable output") {
		t.Fatalf("reloaded history omitted subagent output:\n%s", plain)
	}
	if strings.Contains(plain, `"session_id"`) {
		t.Fatalf("raw spawn result JSON leaked into history:\n%s", plain)
	}
}

func TestPersistedSubagentHydrationRestoresChildToolActivity(t *testing.T) {
	m := newTestChatModel(true)
	parentID := m.sess.ID
	childID := "child-tools"
	childCall := session.NewMessage(childID, core.Message{Role: core.RoleAssistant, Parts: []core.Part{{
		Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "read-1", Name: "read_file", ToolInfo: "important.go"},
	}}}, 0)
	childResult := session.NewMessage(childID, core.ToolResultMessage("read-1", "read_file", "ok", nil), 1)
	store := &mockStore{
		sessions: map[string]*session.Session{childID: {
			ID: childID, ParentID: parentID, Provider: "mock", Model: "review-model", Status: session.StatusComplete,
			ToolCalls: 1, InputTokens: 10, OutputTokens: 5, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0),
		}},
		messages: map[string][]session.Message{childID: {*childCall, *childResult}},
	}
	m.store = store
	m.messages = persistedSpawnFixture(parentID, childID, "spawn-tools", "review complete")
	loaded := m.loadPersistedSubagentsCmd()().(persistedSubagentsLoadedMsg)
	m.applyPersistedSubagents(loaded)

	plain := ui.StripANSI(m.renderHistory())
	if !strings.Contains(plain, "read_file") || !strings.Contains(plain, "important.go") || !strings.Contains(plain, "1 call") || !strings.Contains(plain, "review complete") {
		t.Fatalf("reloaded history omitted child activity:\n%s", plain)
	}
}

func TestPersistedSubagentHydrationRendersStructuredErrorWithPartialOutput(t *testing.T) {
	m := newTestChatModel(true)
	parentID := m.sess.ID
	assistant := session.NewMessage(parentID, core.Message{Role: core.RoleAssistant, Parts: []core.Part{{
		Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "spawn-error", Name: tools.SpawnAgentToolName, Arguments: json.RawMessage(`{"agent_name":"reviewer","prompt":"review carefully"}`)},
	}}}, 0)
	content, _ := json.Marshal(tools.SpawnAgentResult{Output: "partial finding", Error: "timed out", Type: "timeout"})
	result := session.NewMessage(parentID, core.ToolResultMessage("spawn-error", tools.SpawnAgentToolName, string(content), nil), 1)
	m.messages = []session.Message{*assistant, *result}
	loaded := m.loadPersistedSubagentsCmd()().(persistedSubagentsLoadedMsg)
	m.applyPersistedSubagents(loaded)
	plain := ui.StripANSI(m.renderHistory())
	if !strings.Contains(plain, "partial finding") || !strings.Contains(plain, "reviewer") {
		t.Fatalf("structured error lost partial result:\n%s", plain)
	}
	if run := m.persistedSubagents["spawn-error"]; !run.Partial || run.Error != "timed out" {
		t.Fatalf("structured error projection = %#v", run)
	}
}

func TestPersistedSubagentHydrationRejectsStaleGeneration(t *testing.T) {
	m := newTestChatModel(true)
	m.messages = persistedSpawnFixture(m.sess.ID, "", "spawn-old", "old output")
	old := m.loadPersistedSubagentsCmd()().(persistedSubagentsLoadedMsg)
	m.messages = persistedSpawnFixture(m.sess.ID, "", "spawn-new", "new output")
	current := m.loadPersistedSubagentsCmd()().(persistedSubagentsLoadedMsg)
	m.applyPersistedSubagents(current)
	m.applyPersistedSubagents(old)
	if _, stale := m.persistedSubagents["spawn-old"]; stale {
		t.Fatalf("stale hydration replaced current map: %#v", m.persistedSubagents)
	}
	if run := m.persistedSubagents["spawn-new"]; run.Output != "new output" {
		t.Fatalf("current hydration lost: %#v", m.persistedSubagents)
	}
}

func TestPersistedSubagentHydrationRejectsUnlinkedAndSelfChildren(t *testing.T) {
	for _, tc := range []struct {
		name     string
		childID  string
		parentID string
	}{
		{name: "unlinked root", childID: "unlinked-child", parentID: ""},
		{name: "self reference", childID: "self", parentID: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestChatModel(true)
			if tc.childID == "self" {
				tc.childID = m.sess.ID
			}
			m.store = &mockStore{
				sessions: map[string]*session.Session{tc.childID: {ID: tc.childID, ParentID: tc.parentID, Status: session.StatusComplete}},
				messages: map[string][]session.Message{tc.childID: {*session.NewMessage(tc.childID, core.AssistantText("foreign transcript"), 0)}},
			}
			m.messages = persistedSpawnFixture(m.sess.ID, tc.childID, "spawn-untrusted", "safe fallback")
			loaded := m.loadPersistedSubagentsCmd()().(persistedSubagentsLoadedMsg)
			m.applyPersistedSubagents(loaded)
			run := m.persistedSubagents["spawn-untrusted"]
			if run.ChildAvailable || run.Output != "safe fallback" || len(run.Activities) != 0 {
				t.Fatalf("untrusted child was accepted: %#v", run)
			}
		})
	}
}

func TestPersistedSubagentHydrationRejectsWrongParent(t *testing.T) {
	m := newTestChatModel(true)
	childID := "foreign-child"
	m.store = &mockStore{
		sessions: map[string]*session.Session{childID: {ID: childID, ParentID: "different-parent", Status: session.StatusComplete}},
		messages: map[string][]session.Message{childID: {}},
	}
	m.messages = persistedSpawnFixture(m.sess.ID, childID, "spawn-foreign", "safe fallback")
	loaded := m.loadPersistedSubagentsCmd()().(persistedSubagentsLoadedMsg)
	m.applyPersistedSubagents(loaded)
	if run := m.persistedSubagents["spawn-foreign"]; run.ChildAvailable || run.Output != "safe fallback" {
		t.Fatalf("foreign child was accepted or fallback lost: %#v", run)
	}
}

func TestPersistedDetachedReceiptRefreshesCachedStatusWithoutCollecting(t *testing.T) {
	m, store := newTurnLeaseTestModel(t)
	child := "detached-child"
	m.messages = persistedSpawnFixture(m.sess.ID, child, "spawn", "original output")
	result := m.messages[1].Parts[0].ToolResult
	// Detached receipts may identify the child before a child session exists.
	result.Content = `{"agent_id":"detached-child","status":"running","output":"original output"}`
	first := m.loadPersistedSubagentsCmd()
	if got := m.persistedSubagents["spawn"].SubagentStatus; got != "running" {
		t.Fatalf("first paint = %q", got)
	}
	row := putBarrierChild(t, store, m.sess.ID, child, "running")
	m.applyPersistedSubagents(first().(persistedSubagentsLoadedMsg))
	running := m.persistedSubagents["spawn"]
	row.Status = "turn_limit"
	if err := store.PutAgentRun(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	m.applyPersistedSubagents(m.loadPersistedSubagentsCmd()().(persistedSubagentsLoadedMsg))
	got := m.persistedSubagents["spawn"]
	if got.AgentID != child || got.SubagentStatus != "turn_limit" || got.Fingerprint == running.Fingerprint || got.Output != "original output" {
		t.Fatalf("resolved row = %+v", got)
	}
	stored, err := store.GetAgentRun(t.Context(), child)
	if err != nil || !stored.CollectedAt.IsZero() || !stored.NotifiedAt.IsZero() {
		t.Fatalf("hydration consumed receipt: %+v, %v", stored, err)
	}
}

func TestPersistedDetachedReceiptRejectsForeignAgentRun(t *testing.T) {
	m, store := newTurnLeaseTestModel(t)
	putBarrierChild(t, store, "another-parent", "child", "completed")
	m.messages = persistedSpawnFixture(m.sess.ID, "child", "spawn", "")
	m.messages[1].Parts[0].ToolResult.Content = `{"session_id":"child","status":"queued"}`
	got := m.loadPersistedSubagentsCmd()().(persistedSubagentsLoadedMsg).runs["spawn"]
	if got.SubagentStatus != "queued" || got.AgentID != "child" {
		t.Fatalf("foreign row altered receipt: %+v", got)
	}
}

func TestPersistedDetachedReceiptClosedByLaterMatchingLifecycleResult(t *testing.T) {
	m := newTestChatModel(true)
	spawn := persistedSpawnFixture(m.sess.ID, "child", "spawn", "")
	spawn[1].Parts[0].ToolResult.Content = `{"agent_id":"child","status":"running"}`
	receipt := func(tool, body string) session.Message {
		return *session.NewMessage(m.sess.ID, core.ToolResultMessage("result", tool, body, nil), 0)
	}
	m.messages = append([]session.Message{receipt(tools.CancelAgentToolName, `{"agent_id":"child","status":"cancelled"}`)}, spawn...)
	m.messages = append(m.messages, receipt(tools.WaitAgentToolName, `[{"agent_id":"other","status":"failed"},{"agent_id":"child","status":"turn_limit"}]`), receipt(tools.ContinueAgentToolName, `{"agent_id":"child","status":"completed"}`))
	m.loadPersistedSubagentsCmd()
	if got := m.persistedSubagents["spawn"].SubagentStatus; got != "turn_limit" {
		t.Fatalf("status = %q", got)
	}
}

func TestPersistedHydrationCannotReopenClosedRow(t *testing.T) {
	m := newTestChatModel(true)
	m.messages = persistedSpawnFixture(m.sess.ID, "child", "spawn", "")
	m.messages[1].Parts[0].ToolResult.Content = `{"session_id":"child","status":"running"}`
	load := m.loadPersistedSubagentsCmd()
	m.persistedSubagents["spawn"] = m.persistedSubagents["spawn"].WithSubagentStatus("cancelled")
	m.persistedSubagents["unrelated"] = m.persistedSubagents["spawn"]
	snapshot := load().(persistedSubagentsLoadedMsg)
	if _, aliased := snapshot.runs["unrelated"]; aliased {
		t.Fatal("hydration command shared its fallback map with the model")
	}
	m.applyPersistedSubagents(snapshot)
	if got := m.persistedSubagents["spawn"].SubagentStatus; got != "cancelled" {
		t.Fatalf("late hydration reopened row: %q", got)
	}
	load = m.loadPersistedSubagentsCmd()
	if got := m.persistedSubagents["spawn"].SubagentStatus; got != "cancelled" {
		t.Fatalf("old receipt reopened first paint: %q", got)
	}
	if got := load().(persistedSubagentsLoadedMsg).runs["spawn"].SubagentStatus; got != "cancelled" {
		t.Fatalf("old receipt reopened refreshed row: %q", got)
	}
}
