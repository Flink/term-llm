package chat

import (
	"strings"

	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/session"
	"github.com/term-llm/term-llm/internal/ui"
)

// recordScriptHeader appends a rendered script header at the current row of a
// block being built. rendered is the script tool-call output whose first row is
// the header; current is the block content before it was written.
func (r *MessageBlockRenderer) recordScriptHeader(current, toolCallID, rendered string, expanded bool) {
	if strings.TrimSpace(toolCallID) == "" {
		return
	}
	start, end, ok := ui.ScriptHeaderSpan(rendered, r.width)
	if !ok {
		return
	}
	r.scriptHeaders = append(r.scriptHeaders, ui.ScriptHeaderHit{
		Line:        strings.Count(current, "\n"),
		ToolCallID:  toolCallID,
		Expanded:    expanded,
		ColumnStart: start,
		ColumnEnd:   end,
	})
}

// scriptExpansionSignature fingerprints only overrides that change this block.
func scriptExpansionSignature(msg *session.Message, overrides map[string]bool, fallback bool) uint64 {
	if len(overrides) == 0 || msg.Role != core.RoleAssistant {
		return 0
	}
	var signature uint64
	for _, part := range msg.Parts {
		call := part.ToolCall
		if part.Type != core.PartToolCall || call == nil || !ui.IsScriptTool(call.Name) {
			continue
		}
		if expanded, ok := overrides[call.ID]; ok && expanded != fallback {
			if signature == 0 {
				signature = fnv64Offset
			}
			signature = writeStringHash(signature, call.ID)
		}
	}
	return signature
}

// recordScriptBlockHeaders expands block-relative script headers into absolute
// history lines for the most recent render.
func (r *Renderer) recordScriptBlockHeaders(block *MessageBlock, blockStartLine int) {
	for _, header := range block.ScriptHeaders {
		header.Line += blockStartLine
		r.lastScriptLineHits[header.Line] = header
	}
}

// ScriptLineHitsSnapshot returns a copy of the absolute history line to script
// header map from the most recent Render call.
func (r *Renderer) ScriptLineHitsSnapshot() map[int]ui.ScriptHeaderHit {
	if r == nil || len(r.lastScriptLineHits) == 0 {
		return nil
	}
	out := make(map[int]ui.ScriptHeaderHit, len(r.lastScriptLineHits))
	for line, hit := range r.lastScriptLineHits {
		out[line] = hit
	}
	return out
}
