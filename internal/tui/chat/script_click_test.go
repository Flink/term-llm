package chat

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/session"
	"github.com/term-llm/term-llm/internal/tools"
	"github.com/term-llm/term-llm/internal/ui"
)

func scriptClickModel(t *testing.T) *Model {
	t.Helper()
	m := newTestChatModel(true)
	m.width = 100
	m.height = 40
	m.setTextareaValue("")
	m.messages = []session.Message{{
		ID:          1,
		SessionID:   "test-session",
		Role:        core.RoleUser,
		TextContent: "go",
		Parts:       []core.Part{{Type: core.PartText, Text: "go"}},
	}}
	return m
}

func updateScriptClickModel(t *testing.T, m *Model, msg tea.Msg) *Model {
	t.Helper()
	updated, _ := m.Update(msg)
	return updated.(*Model)
}

func startScriptTool(t *testing.T, m *Model, callID, name, source, cellID string) *Model {
	t.Helper()
	args, err := json.Marshal(map[string]string{"code": source, "cell_id": cellID})
	if err != nil {
		t.Fatal(err)
	}
	return updateScriptClickModel(t, m, streamEventMsg{event: ui.ToolStartEvent(callID, name, "fresh async JavaScript cell", args)})
}

func endScriptTool(t *testing.T, m *Model, callID, name, cellID, output string) *Model {
	t.Helper()
	envelope := `{"cell_id":"` + cellID + `","status":"completed"}`
	end := ui.ToolEndEvent(callID, name, "fresh async JavaScript cell", true)
	end.ToolOutput = envelope
	var parts []core.ToolContentPart
	if output != "" {
		parts = append(parts, core.ToolContentPart{Type: core.ToolContentPartText, Text: output})
	}
	parts = append(parts, core.ToolContentPart{Type: core.ToolContentPartText, Text: "\n" + envelope})
	end.ToolContentParts = parts
	return updateScriptClickModel(t, m, streamEventMsg{event: end})
}

func completeScriptTool(t *testing.T, m *Model, callID, name, source, cellID, output string) *Model {
	t.Helper()
	m = startScriptTool(t, m, callID, name, source, cellID)
	return endScriptTool(t, m, callID, name, cellID, output)
}

func showCompletedScripts(t *testing.T, m *Model) *Model {
	t.Helper()
	m.streaming = false
	m.viewCache.completedStream = m.renderCompletedSegments(m.tracker.CompletedSegments())
	m.invalidateHistoryCache()
	_ = m.View()
	return m
}

func snapshotScriptHit(t *testing.T, m *Model, callID string) ui.ScriptHeaderHit {
	t.Helper()
	for line, hit := range m.streamView.Hits() {
		if hit.ToolCallID == callID {
			if hit.Line != line {
				t.Fatalf("snapshot key %d disagrees with hit line %d", line, hit.Line)
			}
			return hit
		}
	}
	t.Fatalf("no clickable script header for %s; snapshot=%#v", callID, m.streamView.Hits())
	return ui.ScriptHeaderHit{}
}

func clickViewportLine(t *testing.T, m *Model, line, x int) *Model {
	t.Helper()
	y := line - m.viewport.YOffset()
	if y < 0 || y >= m.viewport.Height() {
		t.Fatalf("content line %d is outside viewport [%d,%d)", line, m.viewport.YOffset(), m.viewport.YOffset()+m.viewport.Height())
	}
	if x < 0 || x >= m.width {
		t.Fatalf("click x %d outside width %d", x, m.width)
	}
	return updateScriptClickModel(t, m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
}

func clickScriptHeader(t *testing.T, m *Model, callID string) *Model {
	t.Helper()
	hit := snapshotScriptHit(t, m, callID)
	return clickViewportLine(t, m, hit.Line, hit.ColumnStart)
}

func TestMouseClickScriptHeaderTogglesOnlyThatCall(t *testing.T) {
	m := scriptClickModel(t)
	m.streaming = true
	m = completeScriptTool(t, m, "script-a", tools.CodeExecToolName, "const alpha = 'alpha-source';", "cell-a", "alpha observed output")
	m = completeScriptTool(t, m, "script-b", tools.CodeWaitToolName, "", "cell-b", "beta observed output")
	m = showCompletedScripts(t, m)

	first := snapshotScriptHit(t, m, "script-a")
	second := snapshotScriptHit(t, m, "script-b")
	if first.Line == second.Line {
		t.Fatalf("distinct script calls share header line %d", first.Line)
	}

	m = clickScriptHeader(t, m, "script-a")
	if !m.streamView.Overrides()["script-a"] {
		t.Fatalf("first script override = %#v, want expanded", m.streamView.Overrides())
	}
	if _, ok := m.streamView.Overrides()["script-b"]; ok {
		t.Fatalf("click leaked to second script: %#v", m.streamView.Overrides())
	}
	_ = m.View()
	rendered := ui.StripANSI(m.viewCache.completedStream)
	if !strings.Contains(rendered, "JavaScript source") || !strings.Contains(rendered, "alpha-source") {
		t.Fatalf("clicked script did not expand: %q", rendered)
	}
	if strings.Contains(rendered, "beta observed output") {
		t.Fatalf("unclicked script expanded as well: %q", rendered)
	}

	m = clickScriptHeader(t, m, "script-b")
	_ = m.View()
	rendered = ui.StripANSI(m.viewCache.completedStream)
	if !strings.Contains(rendered, "JavaScript source") || !strings.Contains(rendered, "Observed output") {
		t.Fatalf("second click should expand the second script without collapsing the first: %q", rendered)
	}
}

func TestMouseScriptHeaderStateSurvivesPendingLiveAndHistory(t *testing.T) {
	m := scriptClickModel(t)
	m.streaming = true
	m = startScriptTool(t, m, "script-a", tools.CodeExecToolName, "const pending = 'pending-source';", "cell-a")
	_ = m.View()

	pending := snapshotScriptHit(t, m, "script-a")
	if pending.Expanded {
		t.Fatalf("pending script should start collapsed: %#v", pending)
	}
	m = clickScriptHeader(t, m, "script-a")
	if !m.streamView.Overrides()["script-a"] {
		t.Fatalf("pending click override = %#v", m.streamView.Overrides())
	}
	if !m.viewCache.historyValid {
		t.Fatal("live script click should not invalidate unchanged history")
	}
	_ = m.View()
	if rendered := ui.StripANSI(m.renderStreamingInline()); !strings.Contains(rendered, "pending-source") {
		t.Fatalf("pending script did not render expanded: %q", rendered)
	}

	m = endScriptTool(t, m, "script-a", tools.CodeExecToolName, "cell-a", "live observed output")
	_ = m.View()
	completed := snapshotScriptHit(t, m, "script-a")
	if !completed.Expanded {
		t.Fatalf("completed live hit lost the pending override: %#v", completed)
	}
	if rendered := ui.StripANSI(m.renderStreamingInline()); !strings.Contains(rendered, "pending-source") || !strings.Contains(rendered, "live observed output") {
		t.Fatalf("completed live script did not keep expanded state: %q", rendered)
	}

	// Move the same tool call into ordinary assistant history, as the next turn
	// does after the retained stream is cleared.
	args, _ := json.Marshal(map[string]string{"code": "const pending = 'pending-source';", "cell_id": "cell-a"})
	envelope := `{"cell_id":"cell-a","status":"completed"}`
	m.streaming = false
	m.viewCache.completedStream = ""
	m.messages = append(m.messages,
		session.Message{
			ID: 2, SessionID: "test-session", Role: core.RoleAssistant, Sequence: 1,
			Parts: []core.Part{{
				Type:     core.PartToolCall,
				ToolCall: &core.ToolCall{ID: "script-a", Name: tools.CodeExecToolName, Arguments: args},
			}},
		},
		session.Message{
			ID: 3, SessionID: "test-session", Role: core.RoleTool, Sequence: 2,
			Parts: []core.Part{{
				Type: core.PartToolResult,
				ToolResult: &core.ToolResult{
					ID: "script-a", Name: tools.CodeExecToolName, Content: envelope,
					ContentParts: []core.ToolContentPart{{Type: core.ToolContentPartText, Text: "live observed output"}},
				},
			}},
		},
	)
	m.invalidateHistoryCache()
	_ = m.View()
	history := snapshotScriptHit(t, m, "script-a")
	if !history.Expanded {
		t.Fatalf("history hit lost the live override: %#v", history)
	}
	if rendered := ui.StripANSI(m.viewCache.historyContent); !strings.Contains(rendered, "pending-source") || !strings.Contains(rendered, "live observed output") {
		t.Fatalf("history script did not render expanded: %q", rendered)
	}
}

func TestCtrlEClearsScriptOverridesAndUsesGlobalFallback(t *testing.T) {
	m := scriptClickModel(t)
	m.streaming = true
	m = completeScriptTool(t, m, "script-a", tools.CodeExecToolName, "const alpha = 'alpha-source';", "cell-a", "alpha observed output")
	m = completeScriptTool(t, m, "script-b", tools.CodeWaitToolName, "", "cell-b", "beta observed output")
	m = showCompletedScripts(t, m)

	m = clickScriptHeader(t, m, "script-a")
	if !m.streamView.Overrides()["script-a"] {
		t.Fatalf("setup click failed: %#v", m.streamView.Overrides())
	}

	m = updateScriptClickModel(t, m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	if !m.toolsExpanded {
		t.Fatal("ctrl+e did not enable global expansion")
	}
	if len(m.streamView.Overrides()) != 0 {
		t.Fatalf("ctrl+e should clear per-call overrides: %#v", m.streamView.Overrides())
	}
	_ = m.View()
	rendered := ui.StripANSI(m.viewCache.completedStream)
	if count := strings.Count(rendered, "▾"); count < 2 {
		t.Fatalf("ctrl+e did not expand every script row: %q", rendered)
	}
	if !strings.Contains(rendered, "alpha-source") || !strings.Contains(rendered, "Observed output") {
		t.Fatalf("ctrl+e global fallback missing expanded bodies: %q", rendered)
	}

	m = updateScriptClickModel(t, m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	if m.toolsExpanded {
		t.Fatal("second ctrl+e did not restore global collapsed mode")
	}
	_ = m.View()
	rendered = ui.StripANSI(m.viewCache.completedStream)
	if strings.Contains(rendered, "alpha-source") || strings.Contains(rendered, "Observed output") {
		t.Fatalf("second ctrl+e retained expanded bodies: %q", rendered)
	}
}

func TestMouseScriptHeaderIgnoresBodyColumnsAndBlockedFrames(t *testing.T) {
	m := scriptClickModel(t)
	m.streaming = true
	m = completeScriptTool(t, m, "script-a", tools.CodeExecToolName, "const alpha = 'alpha-source';", "cell-a", "alpha observed output")
	m = completeScriptTool(t, m, "script-b", tools.CodeWaitToolName, "", "cell-b", "beta observed output")
	m = showCompletedScripts(t, m)
	hit := snapshotScriptHit(t, m, "script-a")

	// Click a real expanded body row, not a neighboring prompt or spacer.
	m = clickViewportLine(t, m, hit.Line, hit.ColumnEnd-1)
	_ = m.View()
	hit = snapshotScriptHit(t, m, "script-a")
	if !hit.Expanded {
		t.Fatal("last visible header column should expand the receipt")
	}
	bodyLine := -1
	for y, line := range strings.Split(ui.StripANSI(m.View().Content), "\n") {
		if strings.Contains(line, "alpha-source") {
			bodyLine = m.viewport.YOffset() + y
			break
		}
	}
	if bodyLine <= hit.Line {
		t.Fatal("expanded script body not visible below the header")
	}
	m = clickViewportLine(t, m, bodyLine, hit.ColumnStart)
	if !m.streamView.Overrides()["script-a"] || !m.selection.Dragging {
		t.Fatal("body click should start selection without toggling the receipt")
	}
	if hit.ColumnEnd < m.width {
		m = clickViewportLine(t, m, hit.Line, hit.ColumnEnd)
		if !m.streamView.Overrides()["script-a"] {
			t.Fatal("click immediately after header should not collapse the receipt")
		}
	}
	m = clickScriptHeader(t, m, "script-a")
	_ = m.View()

	// A resize reflow owns the frame until it lands.
	m.resizeReflowPending = true
	m = clickScriptHeader(t, m, "script-a")
	if len(m.streamView.Overrides()) != 0 {
		t.Fatalf("resize frame accepted a stale script hit: %#v", m.streamView.Overrides())
	}
	m.resizeReflowPending = false

	// Overlays cover the viewport; the underlying row is not clickable.
	m.dialog.ShowContent("Help", "overlay")
	m = clickScriptHeader(t, m, "script-a")
	if len(m.streamView.Overrides()) != 0 {
		t.Fatalf("overlay accepted an underlying script hit: %#v", m.streamView.Overrides())
	}
	m.dialog.Close()

	m.handoverPreview = &handoverPreviewModel{}
	if m.handleScriptMouseClick(tea.MouseClickMsg{X: hit.ColumnStart, Y: hit.Line - m.viewport.YOffset(), Button: tea.MouseLeft}) {
		t.Fatal("handover confirmation must own the frame")
	}
}

func TestMouseScriptHeaderIgnoresNonScriptToolRows(t *testing.T) {
	m := scriptClickModel(t)
	m.streaming = true
	m = completeScriptTool(t, m, "script-a", tools.CodeExecToolName, "const alpha = 'alpha-source';", "cell-a", "alpha observed output")
	m = updateScriptClickModel(t, m, streamEventMsg{event: ui.ToolStartEvent("read-1", "read_file", "(a.go)", nil)})
	m = updateScriptClickModel(t, m, streamEventMsg{event: ui.ToolEndEvent("read-1", "read_file", "(a.go)", true)})
	m = showCompletedScripts(t, m)

	renderedLines := strings.Split(ui.StripANSI(m.viewCache.completedStream), "\n")
	readLine := -1
	for i, line := range renderedLines {
		if strings.Contains(line, "read_file") {
			readLine = i
			break
		}
	}
	if readLine < 0 {
		t.Fatalf("read_file row not rendered: %q", strings.Join(renderedLines, "\n"))
	}
	m = clickViewportLine(t, m, readLine, 0)
	if len(m.streamView.Overrides()) != 0 {
		t.Fatalf("non-script tool row toggled a script: %#v", m.streamView.Overrides())
	}
}

func trackerReasoningLine(t *testing.T, m *Model) int {
	t.Helper()
	lines := m.viewCache.reasoningClickSnapshot.trackerSegmentIndexByLine
	if len(lines) != 1 {
		t.Fatalf("tracker reasoning snapshot = %#v, want exactly one header", lines)
	}
	for line := range lines {
		return line
	}
	return -1
}

func trackerReasoningSegment(m *Model) *ui.ReasoningSegment {
	for i := range m.tracker.Segments {
		if m.tracker.Segments[i].Reasoning != nil {
			return m.tracker.Segments[i].Reasoning
		}
	}
	return nil
}

func TestMouseScriptExpansionShiftsFollowingReasoningHit(t *testing.T) {
	m := scriptClickModel(t)
	m.streaming = true
	m.tracker = ui.NewToolTracker()
	m = completeScriptTool(t, m, "script-a", tools.CodeExecToolName, "const shifted = 'shifted-source';", "cell-a", "script output")

	part := core.Part{
		Type:                  core.PartText,
		ReasoningContent:      "reasoning after script",
		ReasoningKind:         core.ReasoningKindRaw,
		ReasoningSummaryTitle: "After script",
	}
	m.tracker.AddReasoningSegment(ui.NormalizeReasoningSegmentRendered(m.renderReasoningPartBlock(part)), reasoningSegmentFromPart(part))
	_ = m.View()

	oldReasoningLine := trackerReasoningLine(t, m)
	m = clickScriptHeader(t, m, "script-a")
	if !m.streamView.Overrides()["script-a"] {
		t.Fatalf("script click did not expand: %#v", m.streamView.Overrides())
	}
	_ = m.View()

	newReasoningLine := trackerReasoningLine(t, m)
	if newReasoningLine == oldReasoningLine {
		t.Fatalf("expanded script did not move the following reasoning header: %d", oldReasoningLine)
	}
	if segment := trackerReasoningSegment(m); segment.Expanded != nil {
		t.Fatalf("precondition: reasoning block should still be collapsed: %#v", segment.Expanded)
	}

	// The pre-expansion row is now script body, so it must not toggle reasoning.
	m = clickViewportLine(t, m, oldReasoningLine, 0)
	if segment := trackerReasoningSegment(m); segment.Expanded != nil {
		t.Fatalf("stale reasoning row still toggled the block: %#v", segment.Expanded)
	}

	m = clickViewportLine(t, m, newReasoningLine, 0)
	segment := trackerReasoningSegment(m)
	if segment == nil || segment.Expanded == nil || !*segment.Expanded {
		t.Fatalf("shifted reasoning row did not toggle: %#v", segment)
	}
}

func TestMouseScriptHeaderClickRespectsViewportScroll(t *testing.T) {
	m := scriptClickModel(t)
	m.height = 12
	m.streaming = true
	m = updateScriptClickModel(t, m, streamEventMsg{event: ui.TextEvent(strings.Repeat("filler line\n", 20))})
	m = completeScriptTool(t, m, "script-a", tools.CodeExecToolName, "const scrolled = 'scrolled-source';", "cell-a", "scrolled output")
	m = showCompletedScripts(t, m)

	hit := snapshotScriptHit(t, m, "script-a")
	offset := max(0, hit.Line-2)
	if offset == 0 {
		t.Fatalf("test fixture did not produce a scrollable header at line %d", hit.Line)
	}
	m.viewport.SetYOffset(offset)
	_ = m.View()

	m = clickScriptHeader(t, m, "script-a")
	if !m.streamView.Overrides()["script-a"] {
		t.Fatalf("scrolled script header click did not toggle: %#v", m.streamView.Overrides())
	}
}

func TestScriptClickSnapshotIgnoresStaleHistoryAfterMessagesClear(t *testing.T) {
	m := scriptClickModel(t)
	args, _ := json.Marshal(map[string]string{"code": "text('history');", "cell_id": "cell-history"})
	m.messages = append(m.messages,
		session.Message{
			ID: 2, SessionID: "test-session", Role: core.RoleAssistant, Sequence: 1,
			Parts: []core.Part{{
				Type:     core.PartToolCall,
				ToolCall: &core.ToolCall{ID: "script-history", Name: tools.CodeExecToolName, Arguments: args},
			}},
		},
		session.Message{
			ID: 3, SessionID: "test-session", Role: core.RoleTool, Sequence: 2,
			Parts: []core.Part{{
				Type: core.PartToolResult,
				ToolResult: &core.ToolResult{
					ID: "script-history", Name: tools.CodeExecToolName,
					Content: `{"cell_id":"cell-history","status":"completed"}`,
				},
			}},
		},
	)
	m.invalidateHistoryCache()
	_ = m.View()
	if len(m.streamView.Hits()) == 0 {
		t.Fatal("precondition: history script header was not captured")
	}

	m.messages = nil
	m.viewCache.completedStream = ""
	m.invalidateHistoryCache()
	_ = m.View()
	if hits := m.streamView.Hits(); len(hits) != 0 {
		t.Fatalf("empty history reused stale script hits: %#v", hits)
	}
}

func TestMouseBlankScriptIDLeavesHeaderSelectable(t *testing.T) {
	for _, id := range []string{"", " \t"} {
		m := scriptClickModel(t)
		m.messages = append(m.messages, scriptHistoryCall(id))
		_ = m.View()
		if hits := m.streamView.Hits(); len(hits) != 0 {
			t.Fatalf("blank call ID %q should not produce hits: %#v", id, hits)
		}
		for y, line := range strings.Split(ui.StripANSI(m.View().Content), "\n") {
			if strings.Contains(line, "▸ spawn_script") {
				m = clickViewportLine(t, m, m.viewport.YOffset()+y, 2)
				if !m.selection.Dragging {
					t.Fatalf("click on blank-ID receipt %q did not start text selection", id)
				}
				break
			}
		}
		if !m.selection.Dragging {
			t.Fatalf("no visible script header for ID %q", id)
		}
	}
}

func TestScriptOverrideDoesNotLeakOnSessionLoad(t *testing.T) {
	m := scriptClickModel(t)
	m.messages = append(m.messages, scriptHistoryCall("reused-id"))
	_ = m.View()
	m = clickScriptHeader(t, m, "reused-id")
	if !m.streamView.Overrides()["reused-id"] {
		t.Fatal("first session receipt did not expand")
	}
	m = updateScriptClickModel(t, m, sessionLoadedMsg{
		sess:     &session.Session{ID: "another-session"},
		messages: []session.Message{scriptHistoryCall("reused-id")},
	})
	_ = m.View()
	if hit := snapshotScriptHit(t, m, "reused-id"); hit.Expanded {
		t.Fatal("new session receipt inherited old expansion")
	}
}

func TestResetRetainedTrackerHandsScriptReceiptBackToHistory(t *testing.T) {
	m := scriptClickModel(t)
	m.streaming = true
	m = completeScriptTool(t, m, "retained", tools.CodeExecToolName, "text(1);", "cell", "output")
	m.messages = append(m.messages, scriptHistoryCall("retained"))
	m = showCompletedScripts(t, m)
	m = clickScriptHeader(t, m, "retained")
	m.resetRetainedStreamTracker()
	_ = m.View()
	if m.viewCache.completedStream != "" || !snapshotScriptHit(t, m, "retained").Expanded {
		t.Fatal("reset should hand the expanded receipt back to history")
	}
	m = clickScriptHeader(t, m, "retained")
	_ = m.View()
	if snapshotScriptHit(t, m, "retained").Expanded {
		t.Fatal("history receipt should still be collapsible")
	}
}

func scriptHistoryCall(id string) session.Message {
	return session.Message{ID: 2, Role: core.RoleAssistant, Sequence: 1, Parts: []core.Part{{
		Type:     core.PartToolCall,
		ToolCall: &core.ToolCall{ID: id, Name: tools.CodeExecToolName, Arguments: json.RawMessage(`{"code":"text(1);"}`)},
	}}}
}
