package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
)

func TestToolTrackerCodeModeParity(t *testing.T) {
	for _, name := range []string{"spawn_script", "wait_script", "cancel_script"} {
		for _, state := range []string{"yielded", "completed", "cancelled", "failed", "unknown"} {
			for _, expanded := range []bool{false, true} {
				t.Run(name+"/"+state+map[bool]string{false: "/collapsed", true: "/expanded"}[expanded], func(t *testing.T) {
					call := scriptTestCall(name, "const x = 1;\ntext(x);", "cell_target")
					result := scriptTestResult(state)
					result.ContentParts = []core.ToolContentPart{{Type: core.ToolContentPartText, Text: "observed text"}, {Type: core.ToolContentPartText, Text: "\n" + result.Content}}
					result.IsError = state == "failed" || state == "unknown"
					tracker := NewToolTracker()
					tracker.SetExpandHintShown(true)
					tracker.HandleToolStart(call.ID, call.Name, "boilerplate", call.Arguments)
					version := tracker.Version
					tracker.HandleToolEndWithResult(call.ID, !result.IsError, "new info", result.Content, result.ContentParts)
					seg := &tracker.Segments[0]
					if tracker.HasPending() || tracker.Version <= version || seg.CodeMode == nil || seg.ToolInfo != "new info" {
						t.Fatalf("completion semantics changed: %+v", seg)
					}
					wantStatus := ToolSuccess
					if result.IsError {
						wantStatus = ToolError
					}
					if seg.ToolStatus != wantStatus {
						t.Fatalf("receipt lifecycle changed ToolStatus: %v", seg.ToolStatus)
					}
					live := RenderToolSegment(seg, -1, 80, expanded)
					historical, _ := RenderCodeModeToolCall(call, result, 80, expanded, wantStatus)
					if live != historical {
						t.Fatalf("tracker/history diverged:\ntracker: %q\nhistory: %q", live, historical)
					}
				})
			}
		}
	}
}

func TestToolTrackerCodeModeChronologicalRowsAndGate(t *testing.T) {
	tracker := NewToolTracker()
	tracker.SetExpandHintShown(true)
	for i, name := range []string{"spawn_script", "wait_script", "cancel_script", "shell"} {
		id := string(rune('a' + i))
		call := scriptTestCall(name, "text(1)", "cell")
		tracker.HandleToolStart(id, name, "info", call.Arguments)
		tracker.HandleToolEndWithResult(id, true, "", scriptTestResult("yielded").Content, nil)
	}
	if len(tracker.Segments) != 4 || tracker.HasPending() {
		t.Fatalf("rows grouped or lifecycle confused with pending tools: %+v", tracker.Segments)
	}
	for i, seg := range tracker.Segments {
		if seg.ToolCallID != string(rune('a'+i)) || seg.ToolStatus != ToolSuccess {
			t.Fatalf("order/status changed: %+v", seg)
		}
		if (seg.CodeMode != nil) != (seg.ToolName != "shell") {
			t.Fatalf("wrong family gate: %+v", seg)
		}
	}
	tracker.HandleToolStart("error", "wait_script", "", scriptTestCall("wait_script", "", "cell").Arguments)
	tracker.HandleToolEndWithResult("error", false, "", "permission denied", nil)
	got := StripANSI(RenderToolSegment(&tracker.Segments[4], -1, 100, true))
	if !strings.Contains(got, "permission denied") || strings.Contains(got, "cell failed") {
		t.Fatalf("fallback lost error or invented cell state: %s", got)
	}
}

func TestToolTrackerCodeModeCompletionDoesNotOverwriteReceipt(t *testing.T) {
	for _, callID := range []string{"script-1", ""} {
		t.Run(fmt.Sprintf("callID=%q", callID), func(t *testing.T) {
			tracker := NewToolTracker()
			call := scriptTestCall("spawn_script", "text('first')", "")
			if !tracker.HandleToolStart(callID, call.Name, "", call.Arguments) {
				t.Fatal("initial start was rejected")
			}
			tracker.HandleToolEndWithResult(callID, true, "", `{"cell_id":"first-cell","status":"yielded"}`, nil)
			first := tracker.Segments[0].CodeMode
			if first == nil || first.CellID != "first-cell" {
				t.Fatal("initial pending call did not receive its receipt")
			}

			// Duplicate completion must not replace the observed receipt, even
			// when the later completion reports another lifecycle or cell.
			tracker.HandleToolEndWithResult(callID, false, "", `{"cell_id":"other-cell","status":"failed"}`, nil)
			if tracker.Segments[0].CodeMode != first || tracker.Segments[0].ToolStatus != ToolSuccess {
				t.Fatal("duplicate completion rewrote an earlier terminal invocation")
			}

			// The generic tracker intentionally deduplicates all repeated call
			// IDs, including empty IDs. A subsequent start cannot create a new
			// invocation; its completion must not corrupt the prior receipt.
			if tracker.HandleToolStart(callID, "wait_script", "", scriptTestCall("wait_script", "", "first-cell").Arguments) {
				t.Fatal("generic repeated-ID deduplication unexpectedly changed")
			}
			tracker.HandleToolEndWithResult(callID, true, "", `{"cell_id":"first-cell","status":"completed"}`, nil)
			if len(tracker.Segments) != 1 || tracker.Segments[0].CodeMode != first || first.Status != "yielded" {
				t.Fatal("completion without an accepted start overwrote the old observation")
			}

			// A distinct pending invocation must remain independent of a late
			// result for the earlier (possibly empty-ID) invocation.
			tracker.HandleToolStart("next-script", "wait_script", "", scriptTestCall("wait_script", "", "first-cell").Arguments)
			tracker.HandleToolEndWithResult(callID, true, "", `{"cell_id":"wrong-cell","status":"completed"}`, nil)
			if tracker.Segments[1].CodeMode != nil || tracker.Segments[1].ToolStatus != ToolPending || tracker.Segments[0].CodeMode != first {
				t.Fatal("late completion attached to an unrelated pending or historic row")
			}
			tracker.HandleToolEndWithResult("next-script", true, "", `{"cell_id":"first-cell","status":"completed"}`, nil)
			if tracker.Segments[1].CodeMode == nil || tracker.Segments[1].CodeMode.Status != "completed" || tracker.Segments[0].CodeMode != first {
				t.Fatal("distinct completion failed to target its own pending invocation")
			}
		})
	}
}

func TestToolTrackerCodeModeOrphanedEmptyCompletion(t *testing.T) {
	tracker := NewToolTracker()
	tracker.HandleToolEndWithResult("", true, "", `{"cell_id":"orphan","status":"completed"}`, nil)
	if len(tracker.Segments) != 0 {
		t.Fatal("orphaned completion synthesized a tool row")
	}
	// A historical script row completed without result metadata is not a
	// pending target for an orphaned, empty-ID completion arriving later.
	tracker.HandleToolStart("", "spawn_script", "", scriptTestCall("spawn_script", "text(0)", "").Arguments)
	tracker.HandleToolEnd("", true)
	tracker.HandleToolStart("named-script", "spawn_script", "", scriptTestCall("spawn_script", "text(1)", "").Arguments)
	tracker.HandleToolEndWithResult("", true, "", `{"cell_id":"orphan","status":"completed"}`, nil)
	if tracker.Segments[0].CodeMode != nil || tracker.Segments[1].CodeMode != nil || tracker.Segments[1].ToolStatus != ToolPending {
		t.Fatal("orphaned empty-ID script result attached to unrelated rows")
	}
}
