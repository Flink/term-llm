package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/mattn/go-runewidth"
	"github.com/term-llm/term-llm/internal/llm/core"
)

func scriptTestCall(name, source, target string) *core.ToolCall {
	args, _ := json.Marshal(map[string]string{"code": source, "cell_id": target})
	return &core.ToolCall{ID: "invocation", Name: name, Arguments: args}
}

func scriptTestResult(status string) *core.ToolResult {
	return &core.ToolResult{Content: fmt.Sprintf(`{"cell_id":"cell_long_opaque_identifier_0123456789","status":%q,"elapsed_ms":15000,"max_wait":60}`, status)}
}

func renderScriptTest(t *testing.T, tc *core.ToolCall, result *core.ToolResult, width int, expanded bool, status ToolStatus) string {
	t.Helper()
	rendered, ok := RenderCodeModeToolCall(tc, result, width, expanded, status)
	if !ok {
		t.Fatal("script call not handled")
	}
	return StripANSI(rendered)
}

func TestCodeModeReceiptNameAndContentGate(t *testing.T) {
	valid := scriptTestResult("completed").Content
	for _, tc := range []struct{ name, content string }{
		{"shell", valid}, {"code_mode", valid}, {"spawn_script_extra", valid},
		{"spawn_script", ""}, {"wait_script", "null"}, {"cancel_script", "{"},
		{"spawn_script", `{"cell_id":"c"}`},
		{"spawn_script", `{"status":"completed","output":["legacy"]}`},
		{"spawn_script", `{"cell_id":"c","status":123}`},
		{"spawn_script", strings.Repeat("x", codeModeEnvelopeBytes+1)},
	} {
		t.Run(tc.name+tc.content[:min(len(tc.content), 30)], func(t *testing.T) {
			parts := []core.ToolContentPart{{Type: core.ToolContentPartText, Text: valid}}
			if got := ParseCodeModeReceipt(tc.name, tc.content, parts); got != nil {
				t.Fatalf("unexpected parsed receipt: %+v", got)
			}
		})
	}
	for _, name := range []string{"spawn_script", "wait_script", "cancel_script"} {
		if got := ParseCodeModeReceipt(name, valid, nil); got == nil || !got.Structured {
			t.Fatalf("valid %s receipt not parsed", name)
		}
	}
	if _, ok := RenderCodeModeToolCall(nil, nil, 80, false, ToolPending); ok {
		t.Fatal("nil call handled")
	}
	if _, ok := RenderCodeModeToolCall(scriptTestCall("shell", "", ""), nil, 80, false, ToolPending); ok {
		t.Fatal("non-family call handled")
	}
}

func TestCodeModeReceiptObservedStatus(t *testing.T) {
	call := scriptTestCall("spawn_script", "text('hi');", "")
	for _, tc := range []struct {
		status, want, circle string
	}{
		{"yielded", "running (observed)", WorkingCircle()},
		{"running", "running (observed)", WorkingCircle()},
		{"cancelling", "cancelling (observed)", WorkingCircle()},
		{"completed", "completed", SuccessCircle()},
		{"cancelled", "cancelled", codeModeStoppedIndicator},
		{"failed", "failed", ErrorCircle()},
		{"unknown", "unknown", ErrorCircle()},
		{"expired", "expired", ErrorCircle()},
		{"busy", "busy", ErrorCircle()},
		{"outcome_unknown", "outcome_unknown", ErrorCircle()},
		{"future_paused", "future_paused", PendingCircle()},
	} {
		t.Run(tc.status, func(t *testing.T) {
			result := scriptTestResult(tc.status)
			got, _ := RenderCodeModeToolCall(call, result, 160, false, ToolSuccess)
			if !strings.HasPrefix(got, tc.circle) {
				t.Errorf("wrong indicator for %s: %q", tc.status, got)
			}
			plain := StripANSI(got)
			if !strings.Contains(plain, tc.want) || !strings.Contains(plain, "observed at 15s") {
				t.Errorf("missing observed state/age: %s", plain)
			}
			if strings.Contains(plain, "waited") || strings.Contains(plain, "60s") || strings.Contains(plain, "0 output parts") || strings.Contains(plain, "0 calls") {
				t.Errorf("misleading/empty boilerplate: %s", plain)
			}
			if tc.status != "outcome_unknown" && strings.Count(plain, "\n") != 0 {
				t.Errorf("ordinary collapsed receipt must be one visual line: %q", plain)
			}
			expanded := renderScriptTest(t, call, result, 80, true, ToolSuccess)
			if !strings.Contains(expanded, "cell_long_opaque_identifier_0123456789") {
				t.Errorf("expanded cell ID lost: %s", expanded)
			}
		})
	}
	short := renderScriptTest(t, call, scriptTestResult("yielded"), 40, false, ToolSuccess)
	if !strings.Contains(short, "running (observed)") {
		t.Fatalf("lifecycle hidden by opaque ID: %q", short)
	}
}

func TestCodeModeReceiptOutputOrderAndEnvelopeExclusion(t *testing.T) {
	content := `{"cell_id":"c","status":"completed"}`
	parts := []core.ToolContentPart{
		{Type: core.ToolContentPartText, Text: content}, // Intentionally emitted JSON.
		{Type: core.ToolContentPartText, Text: "first"},
		{Type: core.ToolContentPartImageData, ImageData: &core.ToolImageData{Base64: "DO_NOT_RETAIN_IMAGE_BYTES"}},
		{Type: core.ToolContentPartText, Text: "last"},
		{Type: core.ToolContentPartText, Text: "\n" + content},
	}
	r := ParseCodeModeReceipt("spawn_script", content, parts)
	if r == nil || r.OutputCount != 4 || r.ImageCount != 1 {
		t.Fatalf("wrong projection: %+v", r)
	}
	if strings.Count(r.Output, content) != 1 || strings.Contains(fmt.Sprintf("%+v", r), "DO_NOT_RETAIN_IMAGE_BYTES") {
		t.Fatalf("earlier emitted JSON lost or media retained: %+v", r)
	}
	if a, b, c := strings.Index(r.Output, "first"), strings.Index(r.Output, "[inline image"), strings.Index(r.Output, "last"); !(a < b && b < c) {
		t.Fatalf("output order lost: %q", r.Output)
	}
	result := &core.ToolResult{Content: content, ContentParts: parts}
	got := renderScriptTest(t, scriptTestCall("spawn_script", "text('first');", ""), result, 100, true, ToolSuccess)
	if strings.Count(got, content) != 1 || !strings.Contains(got, "Ctrl+O to inspect") {
		t.Fatalf("incorrect inline output: %s", got)
	}
	// A matching envelope not at the end is output, and near-matches are not
	// removed merely because they parse as the same JSON object.
	for _, tail := range []core.ToolContentPart{
		{Type: core.ToolContentPartText, Text: content + " "},
		{Type: core.ToolContentPartImageData},
	} {
		got := ParseCodeModeReceipt("wait_script", content, []core.ToolContentPart{{Type: core.ToolContentPartText, Text: content}, tail})
		if got.OutputCount != 2 || !strings.Contains(got.Output, content) {
			t.Fatalf("non-trailing/non-exact output removed: %+v", got)
		}
	}
}

func TestCodeModePendingAndFallback(t *testing.T) {
	call := scriptTestCall("spawn_script", "const greeting = 'hello';\ntext(greeting);", "")
	pending := renderScriptTest(t, call, nil, 80, false, ToolPending)
	if !strings.Contains(pending, "const greeting") || strings.Contains(pending, "fresh async") {
		t.Fatalf("pending source preview missing: %s", pending)
	}
	expanded := RenderToolCallFromPartWithStatus(call, 100, true, ToolPending)
	if !strings.Contains(StripANSI(expanded), "JavaScript source") || !strings.Contains(StripANSI(expanded), "text(greeting);") || !strings.Contains(expanded, "\x1b[38;2;") {
		t.Fatalf("standalone source expansion/highlighting missing: %q", expanded)
	}
	if !strings.Contains(StripANSI(expanded), "No receipt recorded yet.") {
		t.Fatalf("no-output scope missing: %s", StripANSI(expanded))
	}
	for _, name := range []string{"wait_script", "cancel_script"} {
		got := renderScriptTest(t, scriptTestCall(name, "", "cell_target_123"), nil, 80, false, ToolPending)
		if !strings.Contains(got, "cell_target_123") {
			t.Fatalf("pending target missing: %s", got)
		}
	}
	for _, content := range []string{"permission denied", `{"cell_id":"x","status":`} {
		result := &core.ToolResult{Content: content, IsError: true}
		got := renderScriptTest(t, call, result, 100, true, ToolError)
		if !strings.Contains(got, content) || strings.Contains(got, "cell failed") || strings.Contains(got, "completed") {
			t.Fatalf("fallback pretends lifecycle or loses error: %s", got)
		}
	}
}

func TestCodeModeWarningsAndCompactProjection(t *testing.T) {
	for _, suffix := range []string{
		`,"unsettled_calls":[{"call_id":"a","arguments":{"secret":"not retained"}}]`,
		`,"calls":[{"status":"executing","outcome":"outcome_unknown"}]`,
		`,"unsettled_call_count":2,"outcome_unknown":true,"audit_omitted":true,"omitted_output":true`,
		`,"outcome_unknown":true`,
	} {
		content := `{"cell_id":"c","status":"completed"` + suffix + `}`
		r := ParseCodeModeReceipt("wait_script", content, nil)
		if r == nil || strings.Contains(fmt.Sprintf("%+v", r), "not retained") {
			t.Fatalf("bad compact receipt: %+v", r)
		}
		result := &core.ToolResult{Content: content}
		got, _ := RenderCodeModeToolCall(scriptTestCall("wait_script", "", "c"), result, 100, false, ToolSuccess)
		if strings.HasPrefix(got, SuccessCircle()) || !strings.Contains(StripANSI(got), "uncertain external effects") {
			t.Fatalf("uncertain effects hidden or green: %q", got)
		}
	}
	content := `{"cell_id":"c","status":"failed","error":"boom","error_code":"engine_closed","error_truncated":true,"output_truncated":true,"output_omitted_bytes":42,"output_omitted_images":2,"has_more_output":true,"calls":[{"status":"failed","isError":true}]}`
	result := &core.ToolResult{Content: content}
	got := renderScriptTest(t, scriptTestCall("wait_script", "", "c"), result, 160, true, ToolSuccess)
	for _, want := range []string{"error_code: engine_closed", "boom", "error truncated by engine", "42 bytes / 2 images", "more output available", "1 call", "1 failed call"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "Inline omissions") {
		t.Errorf("engine-lost output incorrectly claimed recoverable by inspector: %s", got)
	}
	collapsed := renderScriptTest(t, scriptTestCall("wait_script", "", "c"), result, 100, false, ToolError)
	if !strings.Contains(collapsed, "engine_closed") {
		t.Fatalf("collapsed error hidden: %s", collapsed)
	}
}

func TestCodeModeWidthAndControlSafety(t *testing.T) {
	attack := "中文\ttext('ok');\x1b[2J\x1b]52;c;ATTACK\a\r\u009b31m\u202e evil"
	args, _ := json.Marshal(map[string]string{"code": attack, "cell_id": attack})
	envelope, _ := json.Marshal(map[string]any{"cell_id": attack, "status": "yielded", "error": attack, "error_code": attack})
	parts := []core.ToolContentPart{{Type: core.ToolContentPartText, Text: attack}}
	for _, width := range []int{1, 2, 3, 4, 10, 20, 40, 80} {
		for _, expanded := range []bool{false, true} {
			t.Run(fmt.Sprintf("width%d/expanded%v", width, expanded), func(t *testing.T) {
				tracker := NewToolTracker() // First row has the one-time expansion hint.
				tracker.HandleToolStart("id", "spawn_script", "untrusted boilerplate", args)
				if !tracker.Segments[0].ToolExpandHint {
					t.Fatal("test did not exercise fresh-row hint")
				}
				for _, finished := range []bool{false, true} {
					if finished {
						tracker.HandleToolEndWithResult("id", true, "", string(envelope), parts)
					}
					got := RenderToolSegment(&tracker.Segments[0], -1, width, expanded)
					plain := StripANSI(got)
					for _, line := range strings.Split(plain, "\n") {
						if cells := runewidth.StringWidth(line); cells > width {
							t.Fatalf("width overflow %d > %d: %q", cells, width, line)
						}
					}
					for _, r := range plain {
						if r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
							t.Fatalf("unescaped control %U in %q", r, plain)
						}
					}
				}
			})
		}
	}
}

func TestCodeModeIndependentCollapsedWarnings(t *testing.T) {
	result := &core.ToolResult{Content: `{"cell_id":"c","status":"failed","error_code":"permission_denied","error":"boom","outcome_unknown":true}`}
	got := renderScriptTest(t, scriptTestCall("cancel_script", "", "c"), result, 100, false, ToolError)
	if !strings.Contains(got, "uncertain external effects") || !strings.Contains(got, "permission_denied") {
		t.Fatalf("uncertainty hid tool error or vice versa: %s", got)
	}
}

func TestCodeModeSourceAndPartBounds(t *testing.T) {
	call := scriptTestCall("spawn_script", "const x = '"+strings.Repeat("a", codeModeInlineBytes*2)+"';", "")
	got := renderScriptTest(t, call, nil, 80, true, ToolPending)
	if !strings.Contains(got, "JavaScript source") || !strings.Contains(got, "Ctrl+O to inspect") || strings.Count(got, "\n") > codeModeInlineLines+20 {
		t.Fatal("source byte/visual bounds missing")
	}
	parts := make([]core.ToolContentPart, codeModeMaxParts+1)
	for i := range parts {
		parts[i] = core.ToolContentPart{Type: core.ToolContentPartImageData, ImageData: &core.ToolImageData{Base64: "not retained"}}
	}
	r := ParseCodeModeReceipt("wait_script", scriptTestResult("completed").Content, parts)
	if !r.InlineOmitted || r.OutputCount != len(parts) || len(r.Output) > codeModeInlineBytes || strings.Contains(r.Output, "not retained") {
		t.Fatalf("part count/byte bound or media retention regression: %+v", r)
	}
}

func TestCodeModeInlineBounds(t *testing.T) {
	call := scriptTestCall("spawn_script", strings.Repeat("const x = '中文';\n", codeModeInlineBytes), "")
	parts := []core.ToolContentPart{{Type: core.ToolContentPartText, Text: strings.Repeat("very long output\n", codeModeInlineBytes)}}
	result := scriptTestResult("completed")
	result.ContentParts = parts
	r := ParseCodeModeReceipt(call.Name, result.Content, parts)
	if r == nil || len(r.Output) > codeModeInlineBytes || !r.InlineOmitted {
		t.Fatalf("unbounded output projection: %+v", r)
	}
	got := renderScriptTest(t, call, result, 80, true, ToolSuccess)
	if !strings.Contains(got, "Inline omissions — Ctrl+O to inspect") || strings.Count(got, "\n") > codeModeInlineLines*3+20 {
		t.Fatalf("unbounded inline display or missing inspector footer: %d lines", strings.Count(got, "\n"))
	}
	// Visual lines are bounded even when byte input is below the byte cap.
	result.ContentParts = []core.ToolContentPart{{Type: core.ToolContentPartText, Text: strings.Repeat("x\n", codeModeInlineLines*4)}}
	got = renderScriptTest(t, scriptTestCall("wait_script", "", "c"), result, 80, true, ToolSuccess)
	if !strings.Contains(got, "Ctrl+O to inspect") || strings.Count(got, "\n") > codeModeInlineLines+20 {
		t.Fatal("visual output bound missing")
	}
}
