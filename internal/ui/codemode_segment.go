package ui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/terminaltext"
	"github.com/term-llm/term-llm/internal/tools"
)

const (
	codeModeEnvelopeBytes    = 256 << 10
	codeModeInlineBytes      = 16 << 10
	codeModeInlineLines      = 64
	codeModeMaxParts         = 256
	codeModeStoppedIndicator = "\033[38;5;245m■\033[0m"
)

// CodeModeReceipt is a bounded presentation-only observation, not current cell
// state. Output contains ordered text and image placeholders, never media bytes,
// call arguments, or a retained envelope. Full results belong to the inspector.
// Structured is false only for the bounded raw-result fallback used by renderers.
type CodeModeReceipt struct {
	Structured                                  bool
	OutcomeUnknown, AuditOmitted, OmittedOutput bool
	CellID, Status, Error, ErrorCode            string
	ElapsedMS                                   *int64
	CallCount, FailedCalls, UnsettledCalls      int
	UnknownEffects, InFlightCalls               int
	OutputCount, ImageCount                     int
	Output                                      string
	HasMoreOutput, OutputLimited                bool
	OutputTruncated, ErrorTruncated             bool
	OmittedBytes, OmittedImages                 int
	InlineOmitted                               bool
}

func isCodeModeTool(name string) bool {
	return name == tools.CodeExecToolName || name == tools.CodeWaitToolName || name == tools.CodeCancelToolName
}

// ParseCodeModeReceipt parses only Content of the three script lifecycle tools.
// Parts supply ordered observed output, never an alternate source of metadata.
// Malformed, legacy, and unrelated results return nil. Only the final text part
// exactly matching Content (optionally prefixed by the transport newline) is
// excluded; identical JSON deliberately emitted earlier remains visible output.
func ParseCodeModeReceipt(name, content string, parts []core.ToolContentPart) *CodeModeReceipt {
	if !isCodeModeTool(name) || len(content) > codeModeEnvelopeBytes {
		return nil
	}
	var envelope struct {
		CellID          string `json:"cell_id"`
		Status          string `json:"status"`
		Error           string `json:"error"`
		ErrorCode       string `json:"error_code"`
		ElapsedMS       *int64 `json:"elapsed_ms"`
		HasMoreOutput   bool   `json:"has_more_output"`
		OutputLimited   bool   `json:"output_limited"`
		OutputTruncated bool   `json:"output_truncated"`
		ErrorTruncated  bool   `json:"error_truncated"`
		OmittedBytes    int    `json:"output_omitted_bytes"`
		OmittedImages   int    `json:"output_omitted_images"`
		Calls           []struct {
			Status  string `json:"status"`
			Outcome string `json:"outcome"`
			IsError bool   `json:"isError"`
		} `json:"calls"`
		UnsettledCallCount int        `json:"unsettled_call_count"`
		OutcomeUnknown     bool       `json:"outcome_unknown"`
		AuditOmitted       bool       `json:"audit_omitted"`
		OmittedOutput      bool       `json:"omitted_output"`
		UnsettledCalls     []struct{} `json:"unsettled_calls"`
	}
	if json.Unmarshal([]byte(content), &envelope) != nil || strings.TrimSpace(envelope.Status) == "" {
		return nil
	}
	if envelope.CellID == "" && envelope.Error == "" && envelope.ErrorCode == "" {
		return nil
	}
	r := &CodeModeReceipt{
		Structured: true, ElapsedMS: envelope.ElapsedMS,
		CallCount: len(envelope.Calls), UnsettledCalls: len(envelope.UnsettledCalls),
		HasMoreOutput: envelope.HasMoreOutput, OutputLimited: envelope.OutputLimited,
		OutputTruncated: envelope.OutputTruncated, ErrorTruncated: envelope.ErrorTruncated,
		OmittedBytes: envelope.OmittedBytes, OmittedImages: envelope.OmittedImages,
	}
	r.CellID, r.Status = r.field(envelope.CellID), r.field(envelope.Status)
	r.Error, r.ErrorCode = r.field(envelope.Error), r.field(envelope.ErrorCode)
	r.UnsettledCalls = max(r.UnsettledCalls, envelope.UnsettledCallCount)
	r.OutcomeUnknown, r.AuditOmitted, r.OmittedOutput = envelope.OutcomeUnknown, envelope.AuditOmitted, envelope.OmittedOutput
	for _, call := range envelope.Calls {
		if call.IsError || call.Status == "failed" || call.Outcome == "failed" {
			r.FailedCalls++
		}
		if call.Status == "executing" && codeModeLiveObservation(r.Status) {
			// The runtime initially records every active child as outcome_unknown.
			// It is ordinary in-flight work until the cell stops or the envelope
			// explicitly reports unsettled/unknown effects.
			r.InFlightCalls++
		} else if call.Status == "outcome_unknown" || call.Outcome == "outcome_unknown" {
			r.UnknownEffects++
		}
	}
	if len(parts) > 0 {
		last := parts[len(parts)-1]
		if last.Type == core.ToolContentPartText && (last.Text == content || last.Text == "\n"+content) {
			parts = parts[:len(parts)-1]
		}
	}
	codeModeCollectOutput(r, parts)
	return r
}

// RenderCodeModeToolCall renders a single chronological script invocation and its
// observation. A nil result still supports source-only expansion. The bool is
// false for non-family tools so callers can retain their normal rendering path.
func RenderCodeModeToolCall(tc *core.ToolCall, result *core.ToolResult, width int, expanded bool, status ToolStatus) (string, bool) {
	if tc == nil || !isCodeModeTool(tc.Name) {
		return "", false
	}
	seg := &Segment{ToolName: tc.Name, ToolArgs: tc.Arguments, ToolInfo: tc.ToolInfo, ToolStatus: status}
	if result != nil {
		seg.CodeMode = codeModeResultReceipt(tc.Name, result.Content, result.ContentParts)
		if result.IsError {
			seg.ToolStatus = ToolError
		}
	}
	return renderCodeModeSegment(seg, width, expanded), true
}

func codeModeResultReceipt(name, content string, parts []core.ToolContentPart) *CodeModeReceipt {
	if receipt := ParseCodeModeReceipt(name, content, parts); receipt != nil {
		return receipt
	}
	r := &CodeModeReceipt{}
	if len(parts) == 0 && content != "" {
		parts = []core.ToolContentPart{{Type: core.ToolContentPartText, Text: content}}
	}
	codeModeCollectOutput(r, parts)
	return r
}

func codeModeCollectOutput(r *CodeModeReceipt, parts []core.ToolContentPart) {
	r.OutputCount = len(parts)
	var b strings.Builder
	previousText := false
	for i, part := range parts {
		if i >= codeModeMaxParts || b.Len() >= codeModeInlineBytes {
			r.InlineOmitted = true
			break
		}
		var text string
		switch part.Type {
		case core.ToolContentPartText:
			text = part.Text
		case core.ToolContentPartImageData:
			r.ImageCount++
			text = "[inline image — Ctrl+O to inspect]"
		default:
			text = "[unsupported output — Ctrl+O to inspect]"
		}
		if text == "" {
			continue
		}
		isText := part.Type == core.ToolContentPartText
		// Text parts are already delimited by the runtime; a drained chunk may
		// even be a continuation of the preceding part. Only placeholders need
		// invented display separators.
		if b.Len() > 0 && (!isText || !previousText) && !strings.HasSuffix(b.String(), "\n") && !strings.HasPrefix(text, "\n") {
			b.WriteByte('\n')
		}
		previousText = isText
		text, omitted := codeModeBound(text, codeModeInlineBytes-b.Len())
		r.InlineOmitted = r.InlineOmitted || omitted
		b.WriteString(text)
	}
	r.Output = b.String()
}

// codeModeBound clones even short strings, so receipt fields cannot pin a large
// input allocation. Bound before escaping (which can expand controls sixfold).
func codeModeBound(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return strings.Clone(s), false
	}
	if limit < 0 {
		limit = 0
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return strings.Clone(s[:limit]), true
}

func (r *CodeModeReceipt) field(s string) string {
	bounded, omitted := codeModeBound(s, codeModeInlineBytes)
	r.InlineOmitted = r.InlineOmitted || omitted
	return codeModeSafe(bounded)
}

func codeModeSafe(s string) string {
	bounded, _ := codeModeBound(s, codeModeInlineBytes)
	safe := terminaltext.EscapeControlsPreserveLayout(bounded)
	// Escape format controls (notably bidi overrides) without silently
	// changing the recorded source into replacement characters.
	var out strings.Builder
	for _, r := range safe {
		if unicode.Is(unicode.Cf, r) {
			if r > 0xFFFF {
				fmt.Fprintf(&out, "\\U%08x", r)
			} else {
				fmt.Fprintf(&out, "\\u%04x", r)
			}
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func codeModeArgs(seg *Segment) (source, target string, omitted bool) {
	if len(seg.ToolArgs) > codeModeEnvelopeBytes {
		return "", "", true
	}
	var args struct {
		Code   string `json:"code"`
		CellID string `json:"cell_id"`
	}
	if json.Unmarshal(seg.ToolArgs, &args) != nil {
		return "", "", len(seg.ToolArgs) > 0
	}
	if seg.ToolName == tools.CodeExecToolName {
		source, omitted = codeModeBound(args.Code, codeModeInlineBytes)
	}
	return source, codeModeSafe(args.CellID), omitted
}

func codeModeObservation(r *CodeModeReceipt) string {
	status := r.Status
	switch status {
	case "yielded", "running", "cancelling":
		status = "running (observed)"
		if r.Status == "cancelling" {
			status = "cancelling (observed)"
		}
	}
	if r.ElapsedMS != nil && *r.ElapsedMS >= 0 {
		status += " · observed at " + strconv.FormatFloat(float64(*r.ElapsedMS)/1000, 'f', -1, 64) + "s"
	}
	return status
}

func codeModeLiveObservation(status string) bool {
	return status == "yielded" || status == "running" || status == "cancelling"
}

func codeModeUncertain(r *CodeModeReceipt) bool {
	return r != nil && (r.OutcomeUnknown || r.UnsettledCalls > 0 || r.UnknownEffects > 0 || r.Status == "outcome_unknown")
}

func codeModeSummary(r *CodeModeReceipt) string {
	var items []string
	if r.OutputCount > 0 {
		items = append(items, codeModeCount(r.OutputCount, "output part"))
	}
	if r.CallCount > 0 {
		items = append(items, codeModeCount(r.CallCount, "call"))
	}
	if r.InFlightCalls > 0 {
		items = append(items, codeModeCount(r.InFlightCalls, "call")+" in flight")
	}
	if r.HasMoreOutput {
		items = append(items, "more output available")
	}
	if r.OutputLimited {
		items = append(items, "observation output limited")
	}
	if r.OutputTruncated {
		items = append(items, "output truncated by engine")
	}
	if r.OmittedBytes > 0 || r.OmittedImages > 0 {
		items = append(items, fmt.Sprintf("engine omitted %d bytes / %d images", r.OmittedBytes, r.OmittedImages))
	}
	if r.OmittedOutput {
		items = append(items, "output omitted by engine")
	}
	if r.AuditOmitted {
		items = append(items, "call audit omitted by engine")
	}
	if r.FailedCalls > 0 {
		items = append(items, codeModeCount(r.FailedCalls, "failed call"))
	}
	if codeModeUncertain(r) {
		items = append(items, fmt.Sprintf("WARNING: uncertain external effects (outcome_unknown; %d unsettled calls, %d unknown outcomes); reconcile before retry", r.UnsettledCalls, r.UnknownEffects))
	}
	if r.ErrorCode != "" {
		items = append(items, "error_code: "+r.ErrorCode)
	}
	if r.Error != "" {
		items = append(items, "error: "+r.Error)
	}
	if r.ErrorTruncated {
		items = append(items, "error truncated by engine")
	}
	return strings.Join(items, " · ")
}

func codeModeIndicator(seg *Segment) string {
	if r := seg.CodeMode; r != nil && r.Structured && r.Status == "cancelled" && !codeModeUncertain(r) {
		return codeModeStoppedIndicator
	}
	if seg.ToolStatus == ToolError {
		return ErrorCircle()
	}
	if r := seg.CodeMode; r != nil && r.Structured {
		if codeModeUncertain(r) || r.Error != "" || r.ErrorCode != "" || r.FailedCalls > 0 {
			return ErrorCircle()
		}
		switch r.Status {
		case "running", "yielded", "cancelling":
			return WorkingCircle()
		case "completed":
			return SuccessCircle()
		case "failed", "unknown", "expired", "busy", "outcome_unknown":
			return ErrorCircle()
		default:
			return PendingCircle()
		}
	}
	if seg.ToolStatus == ToolSuccess && seg.CodeMode != nil {
		return SuccessCircle()
	}
	return PendingCircle()
}

// ToolInfo deliberately stays out of script rows: native previews describe the
// API rather than this invocation. Source/target and the receipt are authoritative.
func codeModeHeader(seg *Segment, source, target string) string {
	header := seg.ToolName
	if r := seg.CodeMode; r != nil && r.Structured {
		header += " · " + codeModeObservation(r)
		if r.InFlightCalls > 0 {
			header += " · " + codeModeCount(r.InFlightCalls, "call") + " in flight"
		}
		if target != "" {
			header += " · " + target
		}
		if r.OutputCount > 0 {
			header += " · " + codeModeCount(r.OutputCount, "output part")
		}
		if r.CallCount > 0 {
			header += " · " + codeModeCount(r.CallCount, "call")
		}
		if r.HasMoreOutput {
			header += " · more output"
		}
		return header
	}
	if target != "" {
		header += " " + target
	}
	if seg.ToolName == tools.CodeExecToolName && strings.TrimSpace(source) != "" {
		preview, _ := codeModeBound(strings.TrimSpace(source), 120)
		header += " · " + strings.Join(strings.Fields(preview), " ")
	}
	switch seg.ToolStatus {
	case ToolPending:
		header += " · pending"
	case ToolError:
		header += " · tool failed; no lifecycle receipt"
	default:
		header += " · no lifecycle receipt"
	}
	return header
}

func codeModeWarning(seg *Segment) string {
	r := seg.CodeMode
	if codeModeUncertain(r) {
		return "WARNING: uncertain external effects (outcome_unknown); reconcile before retry"
	}
	if r == nil {
		return ""
	}
	if r.ErrorCode != "" {
		return codeModeError(r)
	}
	if r.Error != "" {
		return "Error: " + r.Error
	}
	if r.FailedCalls > 0 {
		return "Warning: " + codeModeCount(r.FailedCalls, "failed call")
	}
	if !r.Structured && seg.ToolStatus == ToolError && r.Output != "" {
		return "Tool error: " + r.Output
	}
	if r.OutputTruncated || r.OmittedBytes > 0 || r.OmittedImages > 0 || r.OmittedOutput {
		return "Warning: output omitted/truncated by engine"
	}
	if r.OutputLimited || r.HasMoreOutput {
		return "More output available in another observation"
	}
	return ""
}

func renderCodeModeSegment(seg *Segment, width int, expanded bool) string {
	return renderCodeModeSegmentWithWave(seg, width, expanded, -1)
}

func renderCodeModeSegmentWithWave(seg *Segment, width int, expanded bool, wavePos int) string {
	source, target, omitted := codeModeArgs(seg)
	r := seg.CodeMode
	if r != nil && r.CellID != "" {
		target = r.CellID
	}
	header := codeModeHeader(seg, source, target)
	// Hints are inside the width boundary, never appended after clipping.
	if !expanded {
		if seg.ToolExpandHint && runewidth.StringWidth(codeModeSafe(header)+" (CTRL+e to expand)") <= codeModeContentWidth(width)-2 {
			header += " (CTRL+e to expand)"

		}
	}
	lines := []string{codeModeMainRow(seg, header, width, expanded, wavePos)}
	if expanded {
		if target != "" {
			block, _ := codeModeBlock("Cell: "+target, width, false)
			lines = append(lines, block...)
		}
		if r != nil && r.Structured {
			block, cut := codeModeBlock(codeModeJoin(codeModeObservation(r), codeModeSummary(r)), width, false)
			lines, omitted = append(lines, block...), omitted || cut
		}
		block, cut := codeModeExpandedBody(seg.ToolName, source, r, width)
		lines, omitted = append(lines, block...), omitted || cut
	} else if warning := codeModeWarning(seg); warning != "" {
		lines = append(lines, codeModeWarningRow(warning, width))
		// Uncertain effects and an actual tool error are independently important.
		// Keep both visible rather than letting one hide behind the other's text.
		if codeModeUncertain(r) && (r.ErrorCode != "" || r.Error != "") {
			lines = append(lines, codeModeWarningRow(codeModeError(r), width))
		}
	}
	if expanded && (omitted || (r != nil && r.InlineOmitted)) {
		footer, _ := codeModeBlock("Inline omissions — Ctrl+O to inspect", width, false)
		lines = append(lines, footer...)
	}
	return strings.Join(lines, "\n")
}

func codeModeJoin(parts ...string) string {
	nonempty := parts[:0]
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			nonempty = append(nonempty, part)
		}
	}
	return strings.Join(nonempty, " · ")
}

func codeModeError(r *CodeModeReceipt) string {
	if r.Status == "cancelled" && r.ErrorCode == "cancelled" {
		return "Stopped: " + codeModeJoin(r.ErrorCode, r.Error)
	}
	return "Error: " + codeModeJoin(r.ErrorCode, r.Error)
}

func codeModeCount(count int, label string) string {
	if count != 1 {
		label += "s"
	}
	return fmt.Sprintf("%d %s", count, label)
}

func codeModeEmptyOutput(r *CodeModeReceipt) string {
	switch {
	case r == nil:
		return "No receipt recorded yet."
	case !r.Structured:
		return "No result recorded."
	case r.OutputTruncated || r.OmittedOutput || r.OmittedBytes > 0 || r.OmittedImages > 0:
		return "No retained output in this observation; output was omitted/truncated by the engine."
	case r.OutputLimited || r.HasMoreOutput:
		return "No output returned in this observation; more output was available when observed."
	case r.InlineOmitted || r.OutputCount > 0:
		return "No inline output retained — Ctrl+O to inspect the recorded receipt."
	default:
		return "No output in this observation."
	}
}

func codeModeWarningRow(warning string, width int) string {
	return codeModePrefix(planGutterStyle.Render("│")+" ", codeModeEllipsis(warning, codeModeContentWidth(width)), width)
}

func codeModeMainRow(seg *Segment, header string, width int, expanded bool, wavePos int) string {
	prefix := codeModeIndicator(seg)
	if width == 1 || width == 2 {
		return prefix
	}
	if width == 3 {
		return prefix + " " + codeModeEllipsis(header, 1)
	}
	disclosure := "▸"
	if expanded {
		disclosure = "▾"
	}
	prefix += " " + disclosure + " "
	if width == 4 {
		return prefix
	}
	contentWidth := codeModeInlineBytes
	if width > 0 {
		contentWidth = width - 4
	}
	content := codeModeEllipsis(header, contentWidth)
	if seg.ToolStatus == ToolPending {
		content = RenderWaveText(content, wavePos)
	}
	return prefix + content
}

func codeModeExpandedBody(name, source string, r *CodeModeReceipt, width int) ([]string, bool) {
	var lines []string
	omitted := false
	if name == tools.CodeExecToolName && strings.TrimSpace(source) != "" {
		lines = append(lines, codeModeGutter("JavaScript source", width))
		block, cut := codeModeBlock(source, width, true)
		lines, omitted = append(lines, block...), cut
	}
	label := "Observed output"
	if r == nil || !r.Structured {
		label = "Tool result"
	}
	lines = append(lines, codeModeGutter(label, width))
	output := codeModeEmptyOutput(r)
	if r != nil && r.Output != "" {
		output = r.Output
	}
	block, cut := codeModeBlock(output, width, false)
	return append(lines, block...), omitted || cut
}

func codeModeEllipsis(text string, width int) string {
	text = strings.Join(strings.Fields(codeModeSafe(text)), " ")
	if runewidth.StringWidth(text) <= width {
		return text
	}
	var b strings.Builder
	col := 0
	for _, r := range text {
		cells := runewidth.RuneWidth(r)
		if col+cells > width-1 {
			break
		}
		b.WriteRune(r)
		col += cells
	}
	b.WriteRune('…')
	return b.String()
}

func codeModeContentWidth(width int) int {
	if width <= 0 {
		return codeModeInlineBytes
	}
	if width <= 2 {
		return 1
	}
	return width - 2
}

func codeModePrefix(prefix, line string, width int) string {
	if width == 1 || width == 2 {
		return line
	}
	return prefix + line
}

func codeModeGutter(label string, width int) string {
	lines, _ := codeModeBlock(label, width, false)
	return strings.Join(lines, "\n")
}

func codeModeBlock(text string, width int, source bool) ([]string, bool) {
	lines, omitted := codeModeLines(text, codeModeContentWidth(width), codeModeInlineLines)
	var highlighter *Highlighter
	if source {
		highlighter = NewHighlighter("cell.js")
	}
	for i, line := range lines {
		if highlighter != nil {
			line = highlighter.HighlightLine(line)
		}
		lines[i] = codeModePrefix(planGutterStyle.Render("│")+" ", line, width)
	}
	return lines, omitted
}

// codeModeLines bounds byte work and visual lines, expands tabs, and wraps on
// terminal cells before styling. A wide rune in a one-cell viewport becomes a
// visible replacement rather than overflowing. Combining marks stay attached.
func codeModeLines(text string, width, limit int) ([]string, bool) {
	text, omitted := codeModeBound(text, codeModeInlineBytes)
	text = codeModeSafe(text)
	lines := make([]string, 0, limit)
	var line strings.Builder
	col := 0
	flush := func() bool {
		lines = append(lines, line.String())
		line.Reset()
		col = 0
		return len(lines) >= limit
	}
	for offset, r := range text {
		if r == '\n' {
			if flush() {
				return lines, omitted || offset+1 < len(text)
			}
			continue
		}
		if r == '\t' {
			spaces := tabWidth - col%tabWidth
			for range spaces {
				if col == width && flush() {
					return lines, true
				}
				line.WriteByte(' ')
				col++
			}
			continue
		}
		cells := runewidth.RuneWidth(r)
		if cells > width {
			r, cells = '\uFFFD', 1
		}
		if col+cells > width && flush() {
			return lines, true
		}
		line.WriteRune(r)
		col += cells
	}
	if line.Len() > 0 || len(lines) == 0 {
		lines = append(lines, line.String())
	}
	return lines, omitted
}
