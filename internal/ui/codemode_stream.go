package ui

import "github.com/term-llm/term-llm/internal/llm/core"

// codeModePresentationParts keeps output order without pinning image payloads in
// queued/replayed UI events. Script rows use placeholders; actual images travel
// separately through the existing media events. The engine's parts stay intact.
func codeModePresentationParts(parts []core.ToolContentPart) []core.ToolContentPart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]core.ToolContentPart, len(parts))
	for i, part := range parts {
		out[i].Type = part.Type
		if part.Type == core.ToolContentPartText {
			out[i].Text = part.Text
		}
	}
	return out
}
