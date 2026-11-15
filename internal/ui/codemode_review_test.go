package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/tools"
)

func TestCodeModeEmptyOutputDoesNotDenyOmittedOutput(t *testing.T) {
	for _, flags := range []string{
		`,"output_truncated":true`, `,"omitted_output":true`,
		`,"output_omitted_bytes":42`, `,"output_omitted_images":2`,
		`,"output_limited":true`, `,"has_more_output":true`,
	} {
		result := &core.ToolResult{Content: `{"cell_id":"c","status":"completed"` + flags + `}`}
		got := renderScriptTest(t, scriptTestCall(tools.CodeExecToolName, "text(1);", ""), result, 120, true, ToolSuccess)
		if strings.Contains(got, "No output in this observation.") || !strings.Contains(got, "observation") {
			t.Fatalf("omission misrepresented for %s: %s", flags, got)
		}
	}
	got := renderScriptTest(t, scriptTestCall(tools.CodeWaitToolName, "", "c"), scriptTestResult("completed"), 120, true, ToolSuccess)
	if !strings.Contains(got, "No output in this observation.") {
		t.Fatalf("genuine empty observation not identified: %s", got)
	}
}

func TestCodeModeDisclosureSurvivesLongHeaders(t *testing.T) {
	call := scriptTestCall(tools.CodeExecToolName, "text(1);", "")
	for _, expanded := range []bool{false, true} {
		got := renderScriptTest(t, call, scriptTestResult("yielded"), 80, expanded, ToolSuccess)
		want := "▸"
		if expanded {
			want = "▾"
		}
		if !strings.Contains(strings.Split(got, "\n")[0], want) {
			t.Fatalf("disclosure lost to opaque cell ID: %s", got)
		}
	}
}

func TestCodeModeCancellationIsDistinctFromPending(t *testing.T) {
	result := &core.ToolResult{Content: `{"cell_id":"c","status":"cancelled","error_code":"cancelled","error":"context canceled"}`, IsError: true}
	got, _ := RenderCodeModeToolCall(scriptTestCall(tools.CodeCancelToolName, "", "c"), result, 100, false, ToolError)
	if !strings.HasPrefix(got, codeModeStoppedIndicator) || strings.HasPrefix(got, PendingCircle()) || strings.HasPrefix(got, ErrorCircle()) {
		t.Fatalf("normal cancellation looks pending or failed: %q", got)
	}
	result.Content = `{"cell_id":"c","status":"cancelled","outcome_unknown":true}`
	got, _ = RenderCodeModeToolCall(scriptTestCall(tools.CodeCancelToolName, "", "c"), result, 100, false, ToolError)
	if !strings.HasPrefix(got, ErrorCircle()) || !strings.Contains(StripANSI(got), "uncertain external effects") {
		t.Fatalf("cancellation concealed uncertain effects: %q", got)
	}
}

func TestCodeModeSummaryHasNoEmptySeparators(t *testing.T) {
	for _, envelope := range []string{
		`{"cell_id":"c","status":"completed"}`,
		`{"cell_id":"c","status":"failed","error_code":"denied"}`,
		`{"cell_id":"c","status":"failed","error":"boom","outcome_unknown":true}`,
	} {
		for _, expanded := range []bool{false, true} {
			got := renderScriptTest(t, scriptTestCall(tools.CodeExecToolName, " \n", ""), &core.ToolResult{Content: envelope}, 120, expanded, ToolSuccess)
			if strings.Contains(got, " ·  · ") || strings.Contains(got, "Error:  · ") {
				t.Fatalf("empty summary fragment: %s", got)
			}
			for _, line := range strings.Split(got, "\n") {
				if strings.HasSuffix(strings.TrimSpace(line), "·") {
					t.Fatalf("dangling summary separator: %q", line)
				}
			}
		}
	}
	got := renderScriptTest(t, scriptTestCall(tools.CodeExecToolName, " \n", ""), nil, 120, false, ToolPending)
	if strings.Contains(got, " · · ") {
		t.Fatalf("whitespace source produced empty preview: %s", got)
	}
}

func TestCodeModeInlineOmissionsAreHonest(t *testing.T) {
	for _, count := range []int{codeModeInlineLines, codeModeInlineLines + 1} {
		lines, omitted := codeModeLines(strings.Repeat("x\n", count), 80, codeModeInlineLines)
		if len(lines) != codeModeInlineLines || omitted != (count > codeModeInlineLines) {
			t.Fatalf("%d lines: rendered %d, omitted=%v", count, len(lines), omitted)
		}
	}
	call := scriptTestCall(tools.CodeExecToolName, strings.Repeat("x", codeModeInlineBytes+1), "")
	got := renderScriptTest(t, call, nil, 80, false, ToolPending)
	if strings.Contains(got, "Inline omissions") || strings.Contains(got, "\n") {
		t.Fatalf("hidden source bloated collapsed row: %s", got)
	}
	largeError, _ := json.Marshal(map[string]any{"cell_id": "c", "status": "failed", "error": strings.Repeat("x", codeModeInlineBytes+1)})
	r := ParseCodeModeReceipt(tools.CodeWaitToolName, string(largeError), nil)
	if r == nil || !r.InlineOmitted {
		t.Fatal("metadata byte omission was hidden")
	}
}

func TestCodeModeOutputJoinsMatchRecordedLayout(t *testing.T) {
	chunks := []core.ToolContentPart{{Type: core.ToolContentPartText, Text: "hel"}, {Type: core.ToolContentPartText, Text: "lo"}, {Type: core.ToolContentPartText, Text: "\nworld"}}
	joined := ParseCodeModeReceipt(tools.CodeExecToolName, `{"cell_id":"c","status":"completed"}`, chunks)
	if joined == nil || joined.Output != "hello\nworld" {
		t.Fatalf("continuation chunks acquired invented newlines: %+v", joined)
	}
	parts := []core.ToolContentPart{{Type: core.ToolContentPartText, Text: "\nalpha\n"}, {Type: core.ToolContentPartText, Text: "\nbeta\n"}}
	r := ParseCodeModeReceipt(tools.CodeExecToolName, `{"cell_id":"c","status":"completed"}`, parts)
	if r == nil || r.Output != "\nalpha\n\nbeta\n" {
		t.Fatalf("invented separator newlines: %+v", r)
	}
}

func TestCodeModeInFlightCallsAreNotUncertainEffects(t *testing.T) {
	call := scriptTestCall(tools.CodeExecToolName, "await tools.shell({});", "")
	for _, status := range []string{"running", "yielded", "cancelling", "completed", "failed", "cancelled"} {
		content, _ := json.Marshal(map[string]any{
			"cell_id": "c", "status": status,
			"calls": []map[string]string{{"status": "executing", "outcome": "outcome_unknown"}},
		})
		result := &core.ToolResult{Content: string(content)}
		got, _ := RenderCodeModeToolCall(call, result, 120, false, ToolSuccess)
		live := status == "running" || status == "yielded" || status == "cancelling"
		if live {
			if strings.HasPrefix(got, ErrorCircle()) || strings.Contains(got, "uncertain external effects") || !strings.Contains(StripANSI(got), "1 call in flight") {
				t.Errorf("ordinary %s child activity marked uncertain: %q", status, got)
			}
		} else if !strings.HasPrefix(got, ErrorCircle()) || !strings.Contains(StripANSI(got), "uncertain external effects") {
			t.Errorf("terminal %s unresolved effects hidden: %q", status, got)
		}
	}
	for _, suffix := range []string{`,"outcome_unknown":true`, `,"unsettled_call_count":1`, `,"unsettled_calls":[{}]`, `,"calls":[{"status":"completed","outcome":"outcome_unknown"}]`} {
		result := &core.ToolResult{Content: `{"cell_id":"c","status":"running"` + suffix + `}`}
		got, _ := RenderCodeModeToolCall(call, result, 120, false, ToolSuccess)
		if !strings.HasPrefix(got, ErrorCircle()) || !strings.Contains(got, "uncertain external effects") {
			t.Errorf("explicit uncertain effects suppressed during running observation: %q", got)
		}
	}
}

func TestCodeModeMissingReceiptDoesNotClaimReturn(t *testing.T) {
	call := scriptTestCall(tools.CodeWaitToolName, "", "c")
	got, _ := RenderCodeModeToolCall(call, nil, 120, false, ToolSuccess)
	if strings.Contains(StripANSI(got), "tool returned") || strings.HasPrefix(got, SuccessCircle()) {
		t.Fatalf("absent receipt claimed tool completion: %q", got)
	}
}
