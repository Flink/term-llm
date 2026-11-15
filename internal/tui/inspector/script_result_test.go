package inspector

import (
	"fmt"
	"strings"
	"testing"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/session"
	"github.com/term-llm/term-llm/internal/tools"
	"github.com/term-llm/term-llm/internal/ui"
)

func scriptResultPart(name, id, content string, parts ...core.ToolContentPart) core.Part {
	return core.Part{Type: core.PartToolResult, ToolResult: &core.ToolResult{
		ID: id, Name: name, Content: content, ContentParts: parts,
	}}
}

func renderInspectorToolResult(part core.Part) string {
	r := NewContentRenderer(120, ui.DefaultStyles(), nil, nil, "", "", nil, config.ReasoningConfig{})
	content, _ := r.RenderMessages([]session.Message{{Role: core.RoleTool, Parts: []core.Part{part}}})
	return ui.StripANSI(content)
}

func TestInspectorScriptResultShowsOrderedOutputAndStatus(t *testing.T) {
	const envelope = `{"status":"completed","cell_id":"cell-1"}`
	part := scriptResultPart(tools.CodeExecToolName, "call-1", envelope,
		core.ToolContentPart{Type: core.ToolContentPartText, Text: "\nalpha output\n"},
		core.ToolContentPart{Type: core.ToolContentPartImageData, ImageData: &core.ToolImageData{
			MediaType: "image/png", Base64: "QUJDRA==",
		}},
		core.ToolContentPart{Type: core.ToolContentPartText, Text: "\nbeta output\n"},
		core.ToolContentPart{Type: core.ToolContentPartText, Text: "\n" + envelope},
	)

	content := renderInspectorToolResult(part)
	statusAt := strings.Index(content, "Status:")
	if statusAt < 0 {
		t.Fatalf("missing status envelope section:\n%s", content)
	}
	outputSection, statusSection := content[:statusAt], content[statusAt:]

	last := -1
	for _, want := range []string{"Output:", "alpha output", "[Image: image/png, 8 base64 chars]", "beta output"} {
		pos := strings.Index(outputSection, want)
		if pos < 0 || pos < last {
			t.Fatalf("output section missing or misordered %q:\n%s", want, content)
		}
		last = pos
	}
	if strings.Contains(outputSection, envelope) {
		t.Fatalf("trailing status envelope duplicated in output section:\n%s", content)
	}
	if got := strings.Count(content, envelope); got != 1 {
		t.Fatalf("status envelope rendered %d times, want once:\n%s", got, content)
	}
	if !strings.Contains(statusSection, envelope) {
		t.Fatalf("status section missing envelope:\n%s", content)
	}
	if strings.Contains(content, "QUJDRA==") {
		t.Fatalf("image base64 leaked into the text surface:\n%s", content)
	}
}

func TestInspectorScriptResultKeepsEarlierEnvelopeJSON(t *testing.T) {
	const envelope = `{"status":"completed","cell_id":"cell-2"}`
	// Projection may append the envelope with or without the tool's newline
	// separator; both recorded shapes are the duplicate status part.
	for _, separator := range []string{"\n", ""} {
		content := inspectorToolResultContent(&core.ToolResult{
			Name:    tools.CodeExecToolName,
			Content: envelope,
			ContentParts: []core.ToolContentPart{
				{Type: core.ToolContentPartText, Text: "\n" + envelope},
				{Type: core.ToolContentPartText, Text: "\nsecond emission\n"},
				{Type: core.ToolContentPartText, Text: separator + envelope},
			},
		})

		statusAt := strings.Index(content, "Status:")
		if statusAt < 0 {
			t.Fatalf("missing status envelope section:\n%s", content)
		}
		outputSection := content[:statusAt]
		if !strings.Contains(outputSection, envelope) {
			t.Fatalf("script-emitted envelope JSON was dropped from the output section:\n%s", content)
		}
		if strings.Index(outputSection, envelope) > strings.Index(outputSection, "second emission") {
			t.Fatalf("earlier emission order changed:\n%s", content)
		}
		if got := strings.Count(content, envelope); got != 2 {
			t.Fatalf("envelope occurrences = %d, want the preserved emission plus one status section:\n%s", got, content)
		}
		if got := strings.Count(content[statusAt:], envelope); got != 1 {
			t.Fatalf("status section occurrences = %d, want 1:\n%s", got, content)
		}
	}
}

func TestInspectorScriptImagePlaceholderEscapesUntrustedMediaType(t *testing.T) {
	placeholder := scriptImagePlaceholder(&core.ToolImageData{
		MediaType: "image/png\x1b[31m\nimage/gif\t",
		Base64:    "QUJDRA==",
	})
	if strings.ContainsAny(placeholder, "\x1b\n\t") {
		t.Fatalf("placeholder kept raw terminal controls: %q", placeholder)
	}
	for _, want := range []string{`\x1b[31m`, `\x0a`, `\x09`, "8 base64 chars"} {
		if !strings.Contains(placeholder, want) {
			t.Fatalf("placeholder missing %q: %q", want, placeholder)
		}
	}

	if got := scriptImagePlaceholder(nil); got != "[Image]" {
		t.Fatalf("nil image data placeholder = %q", got)
	}
	if got := scriptImagePlaceholder(&core.ToolImageData{Base64: "QUJD"}); got != "[Image: 4 base64 chars]" {
		t.Fatalf("empty media type placeholder = %q", got)
	}
}

func TestInspectorScriptResultWithoutPartsFallsBackToContent(t *testing.T) {
	for _, name := range []string{tools.CodeExecToolName, tools.CodeWaitToolName, tools.CodeCancelToolName} {
		for _, parts := range [][]core.ToolContentPart{nil, {}} {
			tr := &core.ToolResult{Name: name, Content: `{"status":"yielded","cell_id":"cell-fallback"}`, ContentParts: parts}
			if got := inspectorToolResultContent(tr); got != tr.Content {
				t.Fatalf("%s with %d parts = %q, want unchanged content %q", name, len(parts), got, tr.Content)
			}
		}
	}

	rendered := renderInspectorToolResult(scriptResultPart(tools.CodeWaitToolName, "call-1", `{"status":"yielded"}`))
	if strings.Contains(rendered, "Output:") || strings.Contains(rendered, "Status:") {
		t.Fatalf("part-less script result invented sections:\n%s", rendered)
	}

	// A part-less script result still escapes the raw envelope it renders.
	hostile := "{\"status\":\"failed\",\"error\":\"\x1b[31mboom\x07\"}"
	escaped := inspectorToolResultContent(&core.ToolResult{Name: tools.CodeCancelToolName, Content: hostile})
	if strings.ContainsAny(escaped, "\x1b\x07") || !strings.Contains(escaped, `\x1b[31mboom`) {
		t.Fatalf("part-less script envelope was not escaped: %q", escaped)
	}
	// Unrelated tools keep their stored content byte-identical.
	if got := inspectorToolResultContent(&core.ToolResult{Name: "shell", Content: hostile}); got != hostile {
		t.Fatalf("unrelated tool result was rewritten: %q", got)
	}
}

func TestInspectorScriptResultEnvelopeOnlyParts(t *testing.T) {
	const envelope = `{"status":"yielded","cell_id":"cell-3"}`
	content := inspectorToolResultContent(&core.ToolResult{
		Name:         tools.CodeExecToolName,
		Content:      envelope,
		ContentParts: []core.ToolContentPart{{Type: core.ToolContentPartText, Text: "\n" + envelope}},
	})
	if !strings.Contains(content, "Output: (no recorded output)") {
		t.Fatalf("empty observation should be labelled, got:\n%s", content)
	}
	if !strings.HasSuffix(content, "Status:\n"+envelope) {
		t.Fatalf("status envelope must close the block, got:\n%s", content)
	}
}

func TestInspectorScriptResultLeavesOtherToolsPlain(t *testing.T) {
	const envelope = `{"status":"completed"}`
	parts := []core.ToolContentPart{
		{Type: core.ToolContentPartText, Text: "\nnot shown output\n"},
		{Type: core.ToolContentPartImageData, ImageData: &core.ToolImageData{MediaType: "image/png", Base64: "QUJDRA=="}},
		{Type: core.ToolContentPartText, Text: "\n" + envelope},
	}
	for _, name := range []string{"", "shell", "code_mode", "spawn_script_extra", "spawn-script", "Spawn_Script", "wait_script_agent"} {
		tr := &core.ToolResult{Name: name, Content: envelope, ContentParts: parts}
		if got := inspectorToolResultContent(tr); got != envelope {
			t.Fatalf("%q must keep plain stored content, got %q", name, got)
		}
	}
}

func TestInspectorScriptResultEscapesTerminalControls(t *testing.T) {
	hostile := "\x1b[31mred\x1b[0m\rbell\x07\u009b31m"
	envelope := `{"status":"completed","error":"` + "\x1b]0;pwned\x07" + `"}`
	content := inspectorToolResultContent(&core.ToolResult{
		Name:    tools.CodeExecToolName,
		Content: envelope,
		ContentParts: []core.ToolContentPart{
			{Type: core.ToolContentPartText, Text: "\n" + hostile},
			{Type: core.ToolContentPartText, Text: "\n" + envelope},
		},
	})

	if strings.ContainsAny(content, "\x1b\x07\r\u009b") {
		t.Fatalf("raw terminal controls survived escaping:\n%q", content)
	}
	for _, want := range []string{`\x1b[31m`, "red", `\x0d`, "bell", `\x07`, `\x9b31m`, `\x1b]0;pwned`} {
		if !strings.Contains(content, want) {
			t.Fatalf("escaped control %q missing from:\n%s", want, content)
		}
	}

	// Plain tool results keep the pre-existing behaviour untouched.
	plain := "\x1b[31mplain\x1b[0m"
	if got := inspectorToolResultContent(&core.ToolResult{Name: "shell", Content: plain}); got != plain {
		t.Fatalf("plain tool result was rewritten: %q", got)
	}
}

func TestInspectorScriptResultUnsupportedPartPlaceholder(t *testing.T) {
	const envelope = `{"status":"completed","cell_id":"cell-6"}`
	content := inspectorToolResultContent(&core.ToolResult{
		Name:    tools.CodeExecToolName,
		Content: envelope,
		ContentParts: []core.ToolContentPart{
			{Type: core.ToolContentPartText, Text: "\nbefore\n"},
			{Type: core.ToolContentPartType("audio"), Text: "unrendered payload", ImageData: &core.ToolImageData{
				MediaType: "audio/wav", Base64: "U0VDUkVU",
			}},
			{Type: core.ToolContentPartText, Text: "\nafter\n"},
			{Type: core.ToolContentPartText, Text: "\n" + envelope},
		},
	})

	last := -1
	for _, want := range []string{"before", "[Unsupported output part: audio]", "after"} {
		pos := strings.Index(content, want)
		if pos < 0 || pos < last {
			t.Fatalf("unsupported part placeholder missing or misordered %q:\n%s", want, content)
		}
		last = pos
	}
	for _, leaked := range []string{"unrendered payload", "U0VDUkVU", "audio/wav"} {
		if strings.Contains(content, leaked) {
			t.Fatalf("unsupported part payload %q leaked:\n%s", leaked, content)
		}
	}
}

func TestInspectorScriptUnsupportedPartTypeEscaped(t *testing.T) {
	placeholder := scriptUnsupportedPartPlaceholder(core.ToolContentPartType("evil\x1b[31m\n\u202etype"))
	if strings.ContainsAny(placeholder, "\x1b\n") {
		t.Fatalf("placeholder kept raw terminal controls: %q", placeholder)
	}
	if strings.ContainsFunc(placeholder, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) {
		t.Fatalf("placeholder kept raw format controls: %q", placeholder)
	}
	for _, want := range []string{"[Unsupported output part: ", `\x1b[31m`, `\x0a`, `\u202e`} {
		if !strings.Contains(placeholder, want) {
			t.Fatalf("placeholder missing %q: %q", want, placeholder)
		}
	}
	if got := scriptUnsupportedPartPlaceholder(core.ToolContentPartType("")); got != "[Unsupported output part]" {
		t.Fatalf("empty type placeholder = %q", got)
	}
}

func TestInspectorScriptResultEscapesFormatControls(t *testing.T) {
	const envelope = "{\"status\":\"completed\",\"error\":\"bad\u202eerror\"}"
	content := inspectorToolResultContent(&core.ToolResult{
		Name:    tools.CodeExecToolName,
		Content: envelope,
		ContentParts: []core.ToolContentPart{
			{Type: core.ToolContentPartText, Text: "\nleft\u202Eright\u2066isolate\n"},
			{Type: core.ToolContentPartImageData, ImageData: &core.ToolImageData{MediaType: "image/\u202Epng", Base64: "QUJD"}},
			{Type: core.ToolContentPartText, Text: "\n" + envelope},
		},
	})
	if strings.ContainsFunc(content, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) {
		t.Fatalf("format controls survived escaping: %q", content)
	}
	for _, want := range []string{`left\u202eright`, `\u2066isolate`, `image/\u202epng`, `bad\u202eerror`} {
		if !strings.Contains(content, want) {
			t.Fatalf("escaped format control %q missing from:\n%s", want, content)
		}
	}

	// Non-BMP format characters keep their exact code point.
	if got := scriptSafeText("tag:\U000E0001"); !strings.Contains(got, `\U000e0001`) {
		t.Fatalf("non-BMP format control not escaped exactly: %q", got)
	}

	// Part-less fallback and script output both escape; other tools do not.
	fallback := inspectorToolResultContent(&core.ToolResult{
		Name: tools.CodeWaitToolName, Content: "{\"status\":\"failed\",\"error\":\"\u202eboom\"}",
	})
	if strings.ContainsFunc(fallback, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) ||
		!strings.Contains(fallback, `\u202eboom`) {
		t.Fatalf("part-less script envelope kept format controls: %q", fallback)
	}
	plain := "left\u202Eright"
	if got := inspectorToolResultContent(&core.ToolResult{Name: "shell", Content: plain}); got != plain {
		t.Fatalf("unrelated tool result was rewritten: %q", got)
	}
}

func TestInspectorScriptResultDistinguishesRetainedOutput(t *testing.T) {
	for _, tt := range []struct {
		name     string
		envelope string
		want     string
	}{
		{name: "genuinely empty", envelope: `{"status":"yielded","cell_id":"cell-7"}`, want: "no recorded output"},
		{name: "truncated", envelope: `{"status":"yielded","output_truncated":true}`, want: "no retained output"},
		{name: "limited", envelope: `{"status":"yielded","output_limited":true}`, want: "no retained output"},
		{name: "more queued", envelope: `{"status":"yielded","has_more_output":true}`, want: "no retained output"},
		{name: "omitted bytes", envelope: `{"status":"completed","output_omitted_bytes":512}`, want: "no retained output"},
		{name: "omitted images", envelope: `{"status":"completed","output_omitted_images":1}`, want: "no retained output"},
		{
			name:     "projection fallback",
			envelope: `{"status":"failed","error":"model_output_limit","state_committed":false,"omitted_output":true}`,
			want:     "no retained output",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			content := inspectorToolResultContent(&core.ToolResult{
				Name:    tools.CodeWaitToolName,
				Content: tt.envelope,
				ContentParts: []core.ToolContentPart{
					{Type: core.ToolContentPartText, Text: "\n" + tt.envelope},
				},
			})
			if !strings.Contains(content, "Output: ("+tt.want+")") {
				t.Fatalf("empty output label = %q, want %q:\n%s", content, tt.want, content)
			}
		})
	}

	// Retained text still renders when the status reports truncation.
	const envelope = `{"status":"yielded","output_truncated":true,"has_more_output":true}`
	content := inspectorToolResultContent(&core.ToolResult{
		Name:    tools.CodeExecToolName,
		Content: envelope,
		ContentParts: []core.ToolContentPart{
			{Type: core.ToolContentPartText, Text: "\nkept output\n"},
			{Type: core.ToolContentPartText, Text: "\n" + envelope},
		},
	})
	if !strings.Contains(content, "kept output") || strings.Contains(content, "no retained output") {
		t.Fatalf("retained output was mislabelled:\n%s", content)
	}
}

func TestInspectorScriptResultConcatenatesConsecutiveTextParts(t *testing.T) {
	const envelope = `{"status":"completed","cell_id":"cell-8"}`
	got := scriptResultOutput([]core.ToolContentPart{
		{Type: core.ToolContentPartText, Text: "hel"},
		{Type: core.ToolContentPartText, Text: "lo"},
		{Type: core.ToolContentPartText, Text: "\nworld"},
		{Type: core.ToolContentPartImageData, ImageData: &core.ToolImageData{MediaType: "image/png", Base64: "QUJD"}},
		{Type: core.ToolContentPartText, Text: "tail"},
		{Type: core.ToolContentPartText, Text: "\ntext after newline"},
		{Type: core.ToolContentPartText, Text: "\n" + envelope},
	}, envelope)

	want := "hello\nworld\n[Image: image/png, 4 base64 chars]\ntail\ntext after newline"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestWrapToolResultLineLeavesOtherToolsOnWrapLine(t *testing.T) {
	long := strings.Repeat("word ", 20) + strings.Repeat("x", 80)
	if got, want := wrapToolResultLine("shell", long, 24), wrapLine(long, 24); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("non-family wrapping changed: %q, want %q", got, want)
	}

	unbroken := strings.Repeat("x", 80)
	family := wrapToolResultLine(tools.CodeExecToolName, unbroken, 24)
	for _, line := range family {
		if width := lipgloss.Width(line); width > 24 {
			t.Fatalf("family line width %d exceeds 24: %q", width, line)
		}
	}
	if strings.Join(family, "") != unbroken {
		t.Fatalf("hard wrap lost content: %q", family)
	}
}

func TestInspectorScriptResultWrapsUnbrokenTextAtNarrowWidth(t *testing.T) {
	envelope := `{"status":"completed","cell_id":"cell-9","calls":[{"name":"read_file","status":"ok","path":"/a/very/long/path/without/spaces"}]}`
	cjk := strings.Repeat("漢", 80)
	part := scriptResultPart(tools.CodeExecToolName, "call-1", envelope,
		core.ToolContentPart{Type: core.ToolContentPartText, Text: "\n" + cjk + "\n"},
		core.ToolContentPart{Type: core.ToolContentPartText, Text: "\n" + envelope},
	)
	m := New([]session.Message{{Role: core.RoleTool, Parts: []core.Part{part}}}, 40, 24, ui.DefaultStyles())

	var joined strings.Builder
	for i, line := range m.contentLines {
		visible := ui.StripANSI(line)
		if width := lipgloss.Width(visible); width > 40 {
			t.Fatalf("line %d width %d exceeds 40: %q", i, width, visible)
		}
		joined.WriteString(strings.TrimPrefix(visible, "│ "))
	}
	rendered := joined.String()
	if got := strings.Count(rendered, "漢"); got != 80 {
		t.Fatalf("unbroken CJK run kept %d of 80 characters", got)
	}
	if !strings.Contains(rendered, envelope) {
		t.Fatalf("compact status envelope did not survive hard wrapping:\n%s", rendered)
	}
}

func TestInspectorScriptResultExpandsFullRecordedOutput(t *testing.T) {
	var output strings.Builder
	for i := 0; i < 120; i++ {
		output.WriteString(fmt.Sprintf("output-line-%03d\n", i))
	}
	const envelope = `{"status":"completed","cell_id":"cell-4"}`
	part := scriptResultPart(tools.CodeExecToolName, "call-1", envelope,
		core.ToolContentPart{Type: core.ToolContentPartText, Text: "\n" + output.String()},
		core.ToolContentPart{Type: core.ToolContentPartText, Text: "\n" + envelope},
	)

	m := New([]session.Message{{Role: core.RoleTool, Parts: []core.Part{part}}}, 120, 24, ui.DefaultStyles())
	collapsed := ui.StripANSI(strings.Join(m.contentLines, "\n"))
	if !strings.Contains(collapsed, "Output:") {
		t.Fatalf("collapsed inspector missing output section:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "output-line-119") {
		t.Fatalf("collapsed inspector already shows the whole recording:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "more lines, press 'e' to expand") {
		t.Fatalf("collapsed inspector missing expand hint:\n%s", collapsed)
	}

	m.expandAllItems()
	expanded := ui.StripANSI(strings.Join(m.contentLines, "\n"))
	if !strings.Contains(expanded, "output-line-119") {
		t.Fatalf("expanded inspector missing recorded output tail:\n%s", expanded)
	}
	if !strings.Contains(expanded, "Status:") || !strings.Contains(expanded, envelope) {
		t.Fatalf("expanded inspector missing status envelope:\n%s", expanded)
	}
}
