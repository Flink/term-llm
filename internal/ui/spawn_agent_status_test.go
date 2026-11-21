package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
)

func TestSpawnAgentStatusRendering(t *testing.T) {
	for _, tc := range []struct{ status, circle string }{
		{"queued", WorkingCircle()},
		{"running", WorkingCircle()},
		{"awaiting_approval", WorkingCircle()},
		{"running_elsewhere", WorkingCircle()},
		{"completed", SuccessCircle()},
		{"failed", ErrorCircle()},
		{"cancelled", ErrorCircle()},
		{"interrupted", ErrorCircle()},
		{"turn_limit", ErrorCircle()},
		{"unknown", SuccessCircle()},
	} {
		t.Run(tc.status, func(t *testing.T) {
			tr := NewToolTracker()
			tr.HandleToolStart("call", "spawn_agent", "@agent", nil)
			seg := &tr.Segments[0]
			for _, progress := range []bool{false, true} {
				seg.SubagentHasProgress = progress
				got := (StreamingIndicator{Segments: tr.ActiveSegments(), Width: 80}).Render(DefaultStyles())
				if !strings.Contains(got, WorkingCircle()) {
					t.Fatalf("pending (progress=%t): %q", progress, got)
				}
			}
			output := fmt.Sprintf(`{"agent_id":"child","status":%q}`, tc.status)
			tr.HandleToolEndWithResult("call", true, "", output, nil)
			if tr.HasPending() || seg.ToolStatus != ToolSuccess {
				t.Fatal("child state changed tool bookkeeping")
			}
			hist := Segment{ToolName: "spawn_agent", ToolStatus: ToolSuccess}
			ApplySpawnAgentReceipt(&hist, output)
			for _, progress := range []bool{false, true} {
				seg.SubagentHasProgress = progress
				hist.SubagentHasProgress = progress
				for _, row := range []*Segment{seg, &hist} {
					if got := RenderToolSegment(row, -1, 80, false); !strings.HasPrefix(got, tc.circle) {
						t.Fatalf("%s (progress=%t): %q", tc.status, progress, got)
					}
				}
			}
			hist.ToolStatus = ToolError
			if got := RenderToolSegment(&hist, -1, 80, false); !strings.HasPrefix(got, ErrorCircle()) {
				t.Fatalf("failed tool: %q", got)
			}
		})
	}
}

func TestSpawnAgentStatusUpdatesByReturnedID(t *testing.T) {
	tr := NewToolTracker()
	end := func(id, name, out string) {
		tr.HandleToolStart(id, name, "", nil)
		tr.HandleToolEndWithResult(id, true, "", out, nil)
	}
	end("one", "spawn_agent", `{"agent_id":"a","status":"running"}`)
	end("two", "spawn_agent", `{"agent_id":"b","status":"queued"}`)
	end("list", "list_agents", `[{"agent_id":"b","status":"awaiting_approval"}]`)
	end("unrelated", "shell", `{"agent_id":"a","status":"completed"}`)
	if !slices.Equal(tr.ActiveSpawnAgentIDs(), []string{"a", "b"}) || tr.Segments[1].SubagentStatus != "awaiting_approval" {
		t.Fatal("incorrect active rows")
	}
	end("wait", "wait_agent", `[{"agent_id":"b","status":"completed"}]`)
	end("cancel", "cancel_agent", `{"agent_id":"a","status":"cancelled"}`)
	if len(tr.ActiveSpawnAgentIDs()) != 0 || tr.Segments[0].SubagentStatus != "cancelled" || tr.Segments[1].SubagentStatus != "completed" {
		t.Fatal("control receipts failed to resolve matching rows")
	}
	if tr.ResolveSpawnAgent("b", "running") {
		t.Fatal("late observation reopened a resolved spawn")
	}
}

func TestActiveSpawnLiveFlushBarrier(t *testing.T) {
	tr := NewToolTracker()
	tr.HandleToolStart("call", "spawn_agent", "", nil)
	tr.HandleToolEndWithResult("call", true, "", `{"agent_id":"child","status":"running"}`, nil)
	tr.AddTextSegment("Later text\n\nMore text", 80)
	for _, out := range []string{tr.FlushStreamingText(0, 80, RenderMarkdown).ToPrint, tr.FlushBeforeExternalUI(80, 0, 0, RenderMarkdown).ToPrint, tr.FlushToScrollback(80, 0, 0, RenderMarkdown).ToPrint, tr.FlushCompletedNow(80, RenderMarkdown).ToPrint} {
		if out != "" {
			t.Fatalf("live flush crossed spawn: %q", out)
		}
	}
	if got := tr.RenderUnflushed(80, RenderMarkdown, false); !strings.Contains(got, WorkingCircle()) || !strings.Contains(StripANSI(got), "Later text") {
		t.Fatalf("barrier hid live content: %q", got)
	}
	version := tr.Version
	if !tr.ResolveSpawnAgent("child", "failed") || tr.Version <= version {
		t.Fatal("resolution did not invalidate live row")
	}
	if got := tr.FlushCompletedNow(80, RenderMarkdown).ToPrint; !strings.Contains(got, ErrorCircle()) {
		t.Fatalf("resolved row not flushed: %q", got)
	}
}

func TestSpawnAgentStatusStreamAdapterToTracker(t *testing.T) {
	for _, name := range []string{"spawn_agent", "cancel_agent", "continue_agent", "wait_agent", "list_agents"} {
		t.Run(name, func(t *testing.T) {
			tr := NewToolTracker()
			tr.HandleToolStart("spawn", "spawn_agent", "", nil)
			id := "spawn"
			status := "running"
			if name != "spawn_agent" {
				tr.HandleToolEndWithResult(id, true, "", `{"agent_id":"child","status":"queued"}`, nil)
				id = "observe"
				tr.HandleToolStart(id, name, "", nil)
				status = "completed"
			}
			receipt := fmt.Sprintf(`{"agent_id":"child","status":%q}`, status)
			out := strings.TrimSuffix(receipt, "}") + `,"output":"secret child output"}`
			if name == "list_agents" || name == "wait_agent" {
				out = "[" + out + "]"
				receipt = "[" + receipt + "]"
			}
			adapter := NewStreamAdapter(10)
			go adapter.ProcessStream(context.Background(), &testStream{events: []core.Event{{Type: core.EventToolExecEnd, ToolName: name, ToolCallID: id, ToolSuccess: true, ToolOutput: out, ToolContentParts: []core.ToolContentPart{{Type: core.ToolContentPartText, Text: "private"}}}}})
			for ev := range adapter.Events() {
				if ev.Type == StreamEventToolEnd {
					if ev.ToolOutput != receipt || len(ev.ToolContentParts) != 0 {
						t.Fatalf("expected only compact receipt, got %+v", ev)
					}
					tr.HandleToolEndWithResult(ev.ToolCallID, ev.ToolSuccess, ev.ToolInfo, ev.ToolOutput, ev.ToolContentParts)
				}
			}
			if row := tr.Segments[0]; row.SubagentID != "child" || row.SubagentStatus != status {
				t.Fatalf("lost child metadata: %s/%s", row.SubagentID, row.SubagentStatus)
			}
		})
	}
}

func TestAgentReceiptsRequireQualifiedToolEnd(t *testing.T) {
	for _, tc := range []struct{ name, parent, output string }{
		{"spawn_agent", "script-call", `{"agent_id":"child","status":"running"}`},
		{"spawn_agent", "", `{"status":"running"}`},
		{"spawn_agent", "", `{"agent_id":"child","status":"made_up"}`},
		{"shell", "", `{"agent_id":"child","status":"running"}`},
	} {
		adapter := NewStreamAdapter(2)
		end := core.Event{Type: core.EventToolExecEnd, ToolName: tc.name, ToolCallID: "call", ParentToolCallID: tc.parent, ToolOutput: tc.output}
		go adapter.ProcessStream(context.Background(), &testStream{events: []core.Event{end}})
		for ev := range adapter.Events() {
			if ev.Type == StreamEventToolEnd && ev.ToolOutput != "" {
				t.Fatalf("unexpected receipt for %+v: %s", tc, ev.ToolOutput)
			}
		}
	}
}

func TestNestedSpawnAgentTypedReceipt(t *testing.T) {
	tr := NewToolTracker()
	tr.HandleToolStart("nested", "spawn_agent", "", nil)
	adapter := NewStreamAdapter(4)
	end := core.Event{Type: core.EventToolExecEnd, ToolCallID: "nested", ToolName: "spawn_agent", ParentToolCallID: "cell", ToolSuccess: true,
		SpawnAgentReceipt: &core.SpawnAgentReceipt{ID: "child", Status: "running"},
		ToolOutput:        `{"agent_id":"impostor","status":"completed","output":"private"}`, ToolContentParts: []core.ToolContentPart{{Type: core.ToolContentPartText, Text: "private"}}}
	go adapter.ProcessStream(context.Background(), &testStream{events: []core.Event{end}})
	for ev := range adapter.Events() {
		if ev.Type != StreamEventToolEnd {
			continue
		}
		if ev.ToolOutput != `{"agent_id":"child","status":"running"}` || len(ev.ToolContentParts) != 0 {
			t.Fatalf("unexpected nested output: %+v", ev)
		}
		tr.HandleToolEndWithResult(ev.ToolCallID, ev.ToolSuccess, ev.ToolInfo, ev.ToolOutput, ev.ToolContentParts)
	}
	if row := tr.Segments[0]; row.SubagentID != "child" || row.SubagentStatus != "running" {
		t.Fatalf("nested spawn status = %q/%q", row.SubagentID, row.SubagentStatus)
	}
}
