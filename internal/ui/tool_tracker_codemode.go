package ui

import "github.com/term-llm/term-llm/internal/llm/core"

// HandleToolEndWithResult preserves the normal tool completion semantics and
// attaches a bounded script observation only to the exact lifecycle family.
// Receipt status is display metadata: yielded cells do not leave this tool call
// pending, and successful cancellation does not change ToolStatus semantics.
// Like the generic status updater, receipts target the newest matching pending
// invocation, never a terminal row on duplicate or orphaned completions.
// Empty IDs support only the first pending invocation: HandleToolStart dedupes
// subsequent empty-ID starts. Distinct invocations require distinct call IDs;
// this helper cannot reconstruct identity from cell IDs or result content.
func (t *ToolTracker) HandleToolEndWithResult(callID string, success bool, info, output string, parts []core.ToolContentPart) {
	pending := -1
	for i := len(t.Segments) - 1; i >= 0; i-- {
		seg := &t.Segments[i]
		if seg.Type == SegmentTool && seg.ToolCallID == callID && seg.ToolStatus == ToolPending {
			pending = i
			break
		}
	}
	t.HandleToolEndWithInfo(callID, success, info)
	if pending >= 0 {
		seg := &t.Segments[pending]
		if isCodeModeTool(seg.ToolName) {
			seg.CodeMode = codeModeResultReceipt(seg.ToolName, output, parts)
		}
	}
}
