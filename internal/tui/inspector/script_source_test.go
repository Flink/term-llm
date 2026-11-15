package inspector

import (
	"encoding/json"
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/tools"
	"github.com/term-llm/term-llm/internal/ui"
)

func TestInspectorFullScriptSourceWrapsAndEscapes(t *testing.T) {
	const width = 40
	r := NewContentRenderer(width, ui.DefaultStyles(), nil, nil, "", "", nil, config.ReasoningConfig{})
	// This source exceeds the chat preview's byte cap and is one long JSON
	// string after formatting; whitespace-only wrapping would hide the tail.
	args, _ := json.Marshal(map[string]string{"code": strings.Repeat("a", 20<<10) + "\nSOURCE_TAIL\u202e\U000e0001"})
	call := &core.ToolCall{ID: "script", Name: tools.CodeExecToolName, Arguments: args}
	_, item := r.renderToolCallWithItem(call, "full-source", 0)
	if item == nil || !item.IsTruncated {
		t.Fatal("full source was not recognized as expandable")
	}
	r.expandedIDs["full-source"] = true
	rendered, _ := r.renderToolCallWithItem(call, "full-source", 0)
	plain := ui.StripANSI(rendered)
	var restored strings.Builder
	for _, line := range strings.Split(plain, "\n") {
		if xansi.StringWidth(line) > width {
			t.Fatalf("script source overflowed inspector: %q", line)
		}
		if strings.HasPrefix(line, "│ ") {
			restored.WriteString(strings.TrimPrefix(line, "│ "))
		}
	}
	for _, want := range []string{"SOURCE_TAIL", `\u202e`, `\U000e0001`} {
		if !strings.Contains(restored.String(), want) {
			t.Fatalf("full source lost %q", want)
		}
	}
	if strings.ContainsAny(plain, "\u202e\U000e0001") {
		t.Fatal("script source exposed raw format controls")
	}
	other := &core.ToolCall{Name: "shell", Arguments: args}
	if r.formatToolCallArgs(other) != r.formatJSON(args) {
		t.Fatal("script source formatting changed unrelated tool arguments")
	}
}
