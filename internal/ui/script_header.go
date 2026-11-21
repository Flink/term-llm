package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// ScriptHeaderHit identifies the clickable header row of one script lifecycle
// tool call in rendered output. Line is zero-based within the rendered string.
// ColumnStart and ColumnEnd are zero-based visual columns on that row.
type ScriptHeaderHit struct {
	Line        int
	ToolCallID  string
	Expanded    bool
	ColumnStart int
	ColumnEnd   int
}

// ScriptRenderOptions configures per-call script expansion during one render
// pass and optionally collects header hits from that exact pass. A hit is
// recorded only when the renderer actually emitted the header, so callers never
// need to match header text to find a click target.
type ScriptRenderOptions struct {
	// Width is the width used for the render pass. The script renderer omits the
	// disclosure when there is not enough room for it.
	Width int
	// Expanded overrides the global expanded fallback per tool-call ID.
	Expanded map[string]bool
	// Hits, when non-nil, receives headers in rendered order.
	Hits *[]ScriptHeaderHit
}

// IsScriptTool reports whether name is one of the script lifecycle tools whose
// rows support per-call disclosure toggling.
func IsScriptTool(name string) bool {
	return isCodeModeTool(name)
}

// ScriptHeaderSpan returns the clickable visual column span of a script row's
// header. The script renderer always places the header on the first output line
// and never wraps it. Narrow widths that cannot fit the disclosure are rejected.
func ScriptHeaderSpan(rendered string, width int) (start, end int, ok bool) {
	if rendered == "" || (width > 0 && width < codeModeDisclosureWidth) {
		return 0, 0, false
	}
	header := rendered
	if newline := strings.IndexByte(header, '\n'); newline >= 0 {
		header = header[:newline]
	}
	end = ansi.StringWidth(header)
	if width > 0 && end > width {
		end = width
	}
	if end <= 0 {
		return 0, 0, false
	}
	return 0, end, true
}

// segmentExpanded resolves the per-call override for a script segment without
// changing the global fallback used by other tools.
func (o *ScriptRenderOptions) segmentExpanded(seg *Segment, fallback bool) bool {
	if o == nil || seg == nil || !isCodeModeTool(seg.ToolName) {
		return fallback
	}
	if expanded, ok := o.Expanded[seg.ToolCallID]; ok {
		return expanded
	}
	return fallback
}

// record appends the header hit for rendered when script hit collection is
// enabled. line is the current zero-based row of the output.
func (o *ScriptRenderOptions) record(line int, seg *Segment, rendered string, expanded bool) {
	if o == nil || o.Hits == nil || seg == nil || seg.Type != SegmentTool || !isCodeModeTool(seg.ToolName) || strings.TrimSpace(seg.ToolCallID) == "" {
		return
	}
	start, end, ok := ScriptHeaderSpan(rendered, o.Width)
	if !ok {
		return
	}
	*o.Hits = append(*o.Hits, ScriptHeaderHit{
		Line:        line,
		ToolCallID:  seg.ToolCallID,
		Expanded:    expanded,
		ColumnStart: start,
		ColumnEnd:   end,
	})
}

// RenderSegmentsWithScriptOptions renders segments with per-call script
// expansion and collects script header hits from the same pass.
func RenderSegmentsWithScriptOptions(segments []*Segment, width int, wavePos int, renderMarkdown func(string, int) string, includeImages bool, expanded bool, renderer ImageArtifactRenderer, script *ScriptRenderOptions) string {
	return renderSegmentsWithLeadingState(nil, false, segments, width, wavePos, renderMarkdown, includeImages, expanded, renderer, script)
}

// RenderUnflushedWithImageRendererAndScripts renders all non-pending,
// non-flushed segments with proper leading spacing, using renderer for image
// segments and script for per-call script expansion and header hits.
func (t *ToolTracker) RenderUnflushedWithImageRendererAndScripts(width int, renderMd func(string, int) string, includeImages bool, renderer ImageArtifactRenderer, script *ScriptRenderOptions) string {
	unflushed := t.CompletedSegments()
	if len(unflushed) == 0 {
		return ""
	}

	var leading *Segment
	// Only add leading context if the FIRST unflushed segment is at its very start.
	if t.HasFlushed && unflushed[0].FlushedPos == 0 {
		leading = &Segment{Type: t.LastFlushedType}
	}

	return renderSegmentsWithLeadingState(leading, t.LastFlushedPlan, unflushed, width, -1, renderMd, includeImages, t.expanded, renderer, script)
}
